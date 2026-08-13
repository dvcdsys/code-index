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

// quiesceBudget bounds waiting for background work to drain. Far more
// generous than the 10 s shutdown budget: a large index run has to be allowed
// to finish rather than be killed, and a timeout here is a post-no-return
// failure that costs a restart.
const quiesceBudget = 2 * time.Minute

// Compact reclaims space and leaves the reclaim mode exactly as it found it.
//
// Compaction and the reclaim mode are separate concerns that happen to share
// an implementation: changing the mode of a populated database requires a
// rebuild, and this is the rebuild. That is a reason for SetAutoVacuum to call
// into here, not a reason for "reclaim my space" to carry an opinion about
// settings.
func (s *Service) Compact(ctx context.Context) (State, error) {
	return s.startRebuild(ctx, s.currentMode(ctx))
}

// SetAutoVacuum changes the database's reclaim mode.
//
// Asking for the mode it is already in does nothing and reports changed=false.
// Anything else costs a full rebuild, because that is the only way SQLite can
// change the mode of a populated database — so the caller is agreeing to the
// same interruption Compact describes.
func (s *Service) SetAutoVacuum(ctx context.Context, mode AutoVacuum) (State, bool, error) {
	switch mode {
	case AutoVacuumNone, AutoVacuumIncremental:
	default:
		return State{}, false, fmt.Errorf("%w: unknown reclaim mode %q", ErrInvalidMode, mode)
	}
	if s.currentMode(ctx) == mode {
		return State{
			Phase:   PhaseIdle,
			Message: fmt.Sprintf("the database is already in %s reclaim mode; nothing to do", mode),
		}, false, nil
	}
	st, err := s.startRebuild(ctx, mode)
	return st, err == nil, err
}

// startRebuild is the shared machinery. Everything that can refuse happens
// synchronously, before anything is stopped: a caller that gets an error here
// is on a server that never left normal service. Past that point the operation
// owns the process — it will finish and restart, or fail and restart.
func (s *Service) startRebuild(ctx context.Context, mode AutoVacuum) (State, error) {
	if s.d.RequestRestart == nil || s.d.Quiesce == nil {
		return State{}, errors.New("database compaction is not wired on this server")
	}

	// Claim the run *before* the preflight, not after it.
	//
	// The preflight is slow — pragmas, a job count, an fsynced journal write —
	// and a check that drops the lock before the claim is not a claim at all.
	// Two clicks a second apart both passed it, and the loser's cleanup then
	// deletes the winner's half-written copy and thaws the write gate in the
	// middle of its snapshot, which is the one failure mode this whole feature
	// exists to avoid. Everything that can refuse gives the claim back.
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return State{}, ErrCompactionRunning
	}
	s.running = true
	s.mu.Unlock()

	handedOff := false
	defer func() {
		if !handedOff {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}
	}()

	stats, err := s.Stats(ctx)
	if err != nil {
		return State{}, err
	}
	if err := s.checkPreconditions(ctx, stats); err != nil {
		return State{}, err
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
		RunID:      newRunID(),
		Kind:       KindCompact,
		Phase:      PhasePreparing,
		StartedAt:  &startedAt,
		PID:        os.Getpid(),
		BytesTotal: (stats.PageCount - stats.FreelistPages) * stats.PageSize,
		AutoVacuum: string(mode),
		Message:    "stopping background work before the copy begins",
	}
	s.record(&st, LevelInfo, st.Message)
	if err := Save(s.d.DBPath, st); err != nil {
		return State{}, fmt.Errorf("write the maintenance journal: %w", err)
	}

	handedOff = true
	go s.runCompaction(st)
	return st, nil
}

// checkPreconditions reports what would make a rebuild refuse right now.
//
// Shared with BlockedReason so the dashboard disables the control for exactly
// the conditions the request would reject on. The two disagreeing is worse
// than either being wrong: an admin clicks an enabled button and gets a toast
// explaining it was never going to work.
func (s *Service) checkPreconditions(ctx context.Context, stats Stats) error {
	if s.d.ActiveJobs != nil {
		n, err := s.d.ActiveJobs(ctx)
		if err != nil {
			return fmt.Errorf("check for in-flight jobs: %w", err)
		}
		if n > 0 {
			return ErrJobsInFlight
		}
	}
	if stats.FreeDiskBytes > 0 && stats.FreeDiskBytes < stats.RequiredDiskBytes {
		return fmt.Errorf("%w: %d bytes free, %d needed",
			ErrInsufficientDisk, stats.FreeDiskBytes, stats.RequiredDiskBytes)
	}
	return nil
}

// BlockedReason is why a rebuild cannot start right now, or "" when one can.
//
// Rendered next to the Compact control and used to disable it. It takes the
// stats the caller already read rather than reading them again, so the number
// on screen and the verdict beside it come from one observation.
func (s *Service) BlockedReason(ctx context.Context, stats Stats) string {
	if s.d.RequestRestart == nil || s.d.Quiesce == nil {
		return "this server cannot rebuild its database — compaction is not wired on this build"
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		return "a database rebuild is already running"
	}
	switch err := s.checkPreconditions(ctx, stats); {
	case err == nil:
		return ""
	case errors.Is(err, ErrJobsInFlight):
		return "an indexing or clone job is in flight — a rebuild takes the server read-only and would fail it mid-run"
	case errors.Is(err, ErrInsufficientDisk):
		return fmt.Sprintf("not enough free disk space: the copy needs about %s and %s is free",
			humanBytes(stats.RequiredDiskBytes), humanBytes(stats.FreeDiskBytes))
	default:
		// A counter that cannot answer is not a reason to promise the rebuild
		// will work.
		return "cannot tell whether the server is busy right now: " + err.Error()
	}
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

	// The connection that will write the copy is opened, and its reclaim mode
	// pinned, *before* the write lock is taken.
	//
	// The order is load-bearing. `PRAGMA auto_vacuum=…` opens a write
	// transaction of its own — it has to, it may need page 1 — so running it
	// while the freezer holds BEGIN IMMEDIATE blocks for the full busy timeout
	// and then fails the compaction with SQLITE_BUSY. That is not hypothetical:
	// it is why every rebuild of an already-incremental database stalled for 30
	// seconds and gave up, while a legacy mode-none database was fine (asking
	// for the mode a database is already in is short-circuited before the lock
	// is needed, which is exactly the case the first end-to-end run happened to
	// exercise).
	cp, err := newCopier(ctx, s.d.DBPath, AutoVacuum(st.AutoVacuum))
	if err != nil {
		fail("preparing the copy connection", err)
		return
	}
	defer cp.Close()

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

	started := time.Now()
	err = cp.into(ctx, p.Copy)
	elapsed := time.Since(started)
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

// copier owns the connection that writes the compacted copy, with the reclaim
// mode of the copy already pinned on it.
//
// It is a type rather than one function because the two halves have to happen
// on either side of the write freeze: the mode is set before the lock exists,
// the copy is taken while it is held. Splitting them keeps the connection —
// and therefore the pending mode — identical across both.
type copier struct {
	pool *sql.DB
	conn *sql.Conn
}

// newCopier opens a connection of its own and pins the mode the copy will be
// created in.
//
// The connection matters, and it is guarded twice.
//
// modernc applies DSN pragmas to every connection it opens, and a connection
// carries a pending auto_vacuum change into any VACUUM INTO it runs — even on a
// database where setting it was otherwise a no-op. Measured: a mode-none
// database opened with auto_vacuum(INCREMENTAL) only in its DSN produces a copy
// in incremental mode. Copying through a pool with that pragma anywhere in its
// DSN would therefore convert a legacy database on a run nobody asked it of,
// and make the requested mode decorative.
//
// So this pool omits the pragma *and* the mode is set explicitly below. Either
// alone is sufficient; both are cheap, and TestCompact_CopyKeepsTheSourceMode-
// UnlessAsked fails only when both are removed, which is the honest statement
// of what is protecting what.
func newCopier(ctx context.Context, dbPath string, want AutoVacuum) (*copier, error) {
	pool, err := openDedicated(dbPath, 1)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}

	// Every mode is named explicitly. Folding FULL into the default would
	// silently demote a database that was in full auto-vacuum — a mode this
	// server never sets but a file it opens may well be in — on a run that
	// asked only for its space back.
	var pragma string
	switch want {
	case AutoVacuumIncremental:
		pragma = "INCREMENTAL"
	case AutoVacuumFull:
		pragma = "FULL"
	default:
		pragma = "NONE"
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA auto_vacuum=`+pragma); err != nil {
		conn.Close()
		pool.Close()
		return nil, fmt.Errorf("set auto_vacuum=%s: %w", pragma, err)
	}
	return &copier{pool: pool, conn: conn}, nil
}

// into writes the compacted copy. Safe to call with the write freeze held —
// VACUUM INTO only reads the source.
func (c *copier) into(ctx context.Context, target string) error {
	// Bound, not interpolated: a data directory can contain a quote.
	if _, err := c.conn.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("vacuum into %s: %w", target, err)
	}
	return nil
}

func (c *copier) Close() {
	c.conn.Close()
	c.pool.Close()
}

// copyInto is the whole sequence on one call, for callers with no freeze to
// work around.
func (s *Service) copyInto(ctx context.Context, target string, want AutoVacuum) error {
	c, err := newCopier(ctx, s.d.DBPath, want)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.into(ctx, target)
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
