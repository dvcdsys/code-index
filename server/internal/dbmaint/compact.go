package dbmaint

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

var (
	// ErrCompactionRunning means one is already in flight.
	ErrCompactionRunning = errors.New("a database compaction is already running")
	// ErrJobsInFlight means indexing or cloning is active. Compaction takes
	// the server read-only, which would fail those jobs mid-run.
	ErrJobsInFlight = errors.New("an indexing or clone job is in flight")
	// ErrInsufficientDisk means the copy would not fit alongside the original.
	ErrInsufficientDisk = errors.New("not enough free disk space for the compacted copy")
)

// progressInterval is how often the journal is refreshed while copying. Each
// update is an fsynced file write, so this is deliberately slow relative to a
// UI poll — the copy's size on disk is the real progress signal and the
// dashboard can read it at whatever rate it likes.
const progressInterval = 2 * time.Second

// quiesceBudget bounds waiting for background work to drain. Far more
// generous than the 10 s shutdown budget: a large index run has to be allowed
// to finish rather than be killed, and a timeout here is a post-no-return
// failure that costs a restart.
const quiesceBudget = 2 * time.Minute

// Compact starts a background rebuild and returns the initial state.
//
// Everything that can refuse happens synchronously, before anything is
// stopped: a caller that gets an error here is on a server that never left
// normal service. Past that point the operation owns the process — it will
// finish and restart, or fail and restart.
func (s *Service) Compact(ctx context.Context, enableIncremental bool) (State, error) {
	if s.d.RequestRestart == nil || s.d.Quiesce == nil {
		return State{}, errors.New("database compaction is not wired on this server")
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return State{}, ErrCompactionRunning
	}
	s.mu.Unlock()

	stats, err := s.Stats(ctx)
	if err != nil {
		return State{}, err
	}
	if s.d.ActiveJobs != nil {
		n, err := s.d.ActiveJobs(ctx)
		if err != nil {
			return State{}, fmt.Errorf("check for in-flight jobs: %w", err)
		}
		if n > 0 {
			return State{}, ErrJobsInFlight
		}
	}
	if stats.FreeDiskBytes > 0 && stats.FreeDiskBytes < stats.RequiredDiskBytes {
		return State{}, fmt.Errorf("%w: %d bytes free, %d needed",
			ErrInsufficientDisk, stats.FreeDiskBytes, stats.RequiredDiskBytes)
	}

	p := PathsFor(s.d.DBPath)
	// VACUUM INTO refuses to write to a path that already exists, and a
	// leftover from an abandoned run is not a reason to make the admin go
	// and delete a file by hand.
	if exists(p.Copy) {
		if err := os.Remove(p.Copy); err != nil {
			return State{}, fmt.Errorf("remove a leftover compacted copy: %w", err)
		}
	}

	startedAt := time.Now().UTC()
	st := State{
		RunID:             newRunID(),
		Kind:              KindCompact,
		Phase:             PhasePreparing,
		StartedAt:         &startedAt,
		PID:               os.Getpid(),
		BytesTotal:        (stats.PageCount - stats.FreelistPages) * stats.PageSize,
		EnableIncremental: enableIncremental,
		Message:           "stopping background work before the copy begins",
	}
	s.record(&st, LevelInfo, st.Message)
	if err := Save(s.d.DBPath, st); err != nil {
		return State{}, fmt.Errorf("write the maintenance journal: %w", err)
	}

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	go s.runCompaction(st)
	return st, nil
}

// runCompaction owns the operation from the point of no return onwards.
//
// It takes no request context on purpose. The admin's HTTP request is long
// gone by the time the copy finishes, and a cancelled request must not abandon
// a half-frozen server.
func (s *Service) runCompaction(st State) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	ctx := context.Background()
	p := PathsFor(s.d.DBPath)

	// Every failure from here on ends the same way: undo what can be undone,
	// then restart. Background services have been stopped and cannot be
	// restarted in place — the job queue's lifecycle is one-shot — so a clean
	// process is the only way back to normal service.
	fail := func(stage string, err error) {
		s.d.Logger.Error("database compaction failed", "stage", stage, "err", err)
		removeIfExists(p.Copy)
		if s.d.Thaw != nil {
			s.d.Thaw()
		}
		st.Phase = PhaseFailed
		st.Error = fmt.Sprintf("%s: %v", stage, err)
		st.Message = "compaction failed; the database was not modified and the server is restarting"
		now := time.Now().UTC()
		st.FinishedAt = &now
		s.record(&st, LevelError, st.Message)
		if serr := Save(s.d.DBPath, st); serr != nil {
			s.d.Logger.Error("could not record the failure", "err", serr)
		}
		s.d.RequestRestart()
	}

	if s.d.Freeze != nil {
		s.d.Freeze()
	}

	quiesceCtx, cancel := context.WithTimeout(ctx, quiesceBudget)
	err := s.d.Quiesce(quiesceCtx)
	cancel()
	if err != nil {
		fail("stopping background work", err)
		return
	}

	// The freeze that actually guarantees the snapshot. A dedicated pool, not
	// a connection from the shared one: this is held for the whole copy and
	// must not spend one of the server's eight.
	freezer, err := openDedicated(s.d.DBPath, 1)
	if err != nil {
		fail("opening the freeze connection", err)
		return
	}
	defer freezer.Close()
	conn, err := freezer.Conn(ctx)
	if err != nil {
		fail("taking the freeze connection", err)
		return
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		fail("taking the write lock", err)
		return
	}
	defer func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }()

	// Read the source identity *under* the freeze. Because nothing can commit
	// from here until the copy is finished, these numbers must match the copy
	// exactly — which is what makes them a usable proof at boot, in a process
	// that has no other way to know what the copy should contain.
	src, err := ReadFingerprint(ctx, conn)
	if err != nil {
		fail("fingerprinting the database", err)
		return
	}
	st.Source = &src
	st.Phase = PhaseCopying
	st.Message = "copying the database; the server is read-only"
	s.record(&st, LevelInfo, st.Message)
	if err := Save(s.d.DBPath, st); err != nil {
		fail("journalling the copy", err)
		return
	}

	stopProgress := s.trackProgress(&st, p.Copy)
	started := time.Now()
	err = s.copyInto(ctx, p.Copy, st.EnableIncremental)
	elapsed := time.Since(started)
	stopProgress()
	if err != nil {
		fail("copying the database", err)
		return
	}

	copied, err := FingerprintFile(ctx, p.Copy)
	if err != nil {
		fail("fingerprinting the copy", err)
		return
	}
	st.Copy = &copied
	if err := VerifyCopy(ctx, p.Copy, src); err != nil {
		fail("verifying the copy", err)
		return
	}
	if err := fsyncFile(p.Copy); err != nil {
		fail("flushing the copy to disk", err)
		return
	}
	if err := SyncDir(dirOf(s.d.DBPath)); err != nil {
		fail("flushing the directory", err)
		return
	}

	size := sizeOrZero(p.Copy)
	if secs := elapsed.Seconds(); secs > 0 {
		st.ThroughputBytesPerSec = int64(float64(size) / secs)
		s.mu.Lock()
		s.throughput = st.ThroughputBytesPerSec
		s.mu.Unlock()
	}
	st.BytesDone = size
	st.Phase = PhaseReadyToSwap
	st.Message = "the compacted database is ready; restarting to adopt it"
	s.record(&st, LevelInfo, fmt.Sprintf(
		"copy complete in %s: %d bytes, verified against the frozen source",
		elapsed.Round(time.Second), size))

	// The last durable act. Everything after this is the reconciler's job, and
	// it will do it whether this process survives the next second or not.
	if err := Save(s.d.DBPath, st); err != nil {
		fail("journalling the completed copy", err)
		return
	}
	s.d.Logger.Info("database copy complete, restarting to adopt it",
		"run_id", st.RunID, "bytes", size, "took", elapsed.Round(time.Second).String())
	s.d.RequestRestart()
}

// copyInto runs VACUUM INTO on a connection of its own.
//
// The connection matters, and it is guarded twice.
//
// modernc applies DSN pragmas to every connection it opens, and a populated
// database records a pending auto_vacuum change even though setting it is
// otherwise a no-op — which VACUUM INTO then carries into the copy. Measured:
// a mode-none database opened with auto_vacuum(INCREMENTAL) only in its DSN
// produces a copy in incremental mode. Since db.buildDSN now carries exactly
// that pragma, copying through the shared pool would silently convert a legacy
// database on every run and make enable_incremental decorative.
//
// So this pool omits the pragma *and* the mode is set explicitly below. Either
// alone is sufficient; both are cheap, and TestCompact_CopyKeepsTheSourceMode-
// UnlessAsked fails only when both are removed, which is the honest statement
// of what is protecting what.
func (s *Service) copyInto(ctx context.Context, target string, incremental bool) error {
	pool, err := openDedicated(s.d.DBPath, 1)
	if err != nil {
		return err
	}
	defer pool.Close()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	mode := "NONE"
	if incremental {
		mode = "INCREMENTAL"
	} else {
		// Preserve whatever the source is in rather than forcing it to none:
		// an admin who is already on incremental has not asked to leave it.
		var v int64
		if err := conn.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&v); err == nil &&
			autoVacuumFromPragma(v) == AutoVacuumIncremental {
			mode = "INCREMENTAL"
		}
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA auto_vacuum=`+mode); err != nil {
		return fmt.Errorf("set auto_vacuum=%s: %w", mode, err)
	}
	// Bound, not interpolated: a data directory can contain a quote.
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("vacuum into %s: %w", target, err)
	}
	return nil
}

// trackProgress refreshes the journal with the copy's size while it grows.
// Returns a function that stops it and waits for the goroutine to finish, so
// no update can land after the terminal state has been written.
func (s *Service) trackProgress(st *State, copyPath string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				snapshot := *st
				snapshot.BytesDone = sizeOrZero(copyPath)
				if err := Save(s.d.DBPath, snapshot); err != nil {
					s.d.Logger.Warn("could not record compaction progress", "err", err)
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// openDedicated opens a pool of its own on the database, with no auto_vacuum
// pragma and a long busy timeout.
func openDedicated(path string, maxConns int) (*sql.DB, error) {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "busy_timeout(30000)")
	sdb, err := sql.Open(db.DriverName, "file:"+path+"?"+v.Encode())
	if err != nil {
		return nil, fmt.Errorf("open a dedicated connection to %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(maxConns)
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return sdb, nil
}

// record appends an event to both the in-journal window and the trail on disk.
func (s *Service) record(st *State, lvl Level, msg string) {
	ev := Event{At: time.Now().UTC(), Level: lvl, Phase: st.Phase, Message: msg, RunID: st.RunID}
	st.Events = append(st.Events, ev)
	if err := AppendEvent(s.d.DBPath, ev); err != nil {
		s.d.Logger.Warn("could not append to the maintenance event log", "err", err)
	}
}

func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Only used to tell one run from another in logs and in a stale
		// journal; a clock-derived fallback is fine.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
