package dbmaint

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
)

// Deps are what the service needs from the rest of the server.
type Deps struct {
	// DB is the live pool. Used only for cheap header reads and for the two
	// bounded operations (checkpoint, incremental reclaim). Compaction never
	// runs through this pool — it opens its own, because it must not spend
	// the shared pool's connections on a long operation.
	DB *sql.DB
	// DBPath is the database file. Everything this package writes — the
	// journal, the event log, the candidate copy — lives beside it.
	DBPath string
	// Env is the CIX_DB_MAINTENANCE_* layer, for deployments configured by a
	// compose file rather than by anyone opening the dashboard.
	Env    ScheduleEnv
	Logger *slog.Logger

	// The hooks below are how compaction reaches the rest of the server
	// without this package importing the job queue, the scheduler or the
	// router. All are optional: a Service wired without them reports and
	// reclaims but refuses to compact.

	// Quiesce stops every background writer and waits for in-flight work to
	// drain. An error means it could not, and the compaction is abandoned.
	Quiesce func(ctx context.Context) error
	// Freeze and Thaw drive the HTTP write gate.
	Freeze func()
	Thaw   func()
	// RequestRestart asks the process to shut down cleanly and re-execute
	// itself, which is how a compacted copy is adopted.
	RequestRestart func()
	// ActiveJobs counts in-flight indexing work. Build it with
	// ActiveWorkCounter — counting the jobs table alone is not enough.
	ActiveJobs func(ctx context.Context) (int, error)
}

// ActiveWorkCounter builds the in-flight-work counter that compaction refuses
// on, from the two places indexing work actually lives.
//
// The jobs table holds server-side clone and index jobs. It does not hold a
// CLI push: the three-phase protocol (begin → files → finish) keeps its state
// in the indexer's session map and creates no row at all. A guard that counted
// only the table would see an idle server in the middle of an index run,
// freeze writes under it, and — in full mode — restart the process mid-protocol
// and leave the project half indexed.
//
// It is a constructor rather than two fields because there were two call sites
// building this by hand and they had already drifted.
func ActiveWorkCounter(sdb *sql.DB, sessions func() int) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		n := 0
		if sessions != nil {
			n = sessions()
		}
		if sdb == nil {
			return n, nil
		}
		var queued int
		err := sdb.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM jobs
			WHERE status IN ('pending','running') AND type IN ('clone_repo','index_repo')`).Scan(&queued)
		if err != nil {
			return 0, err
		}
		return n + queued, nil
	}
}

// Service is the admin-facing surface over database maintenance.
type Service struct {
	d Deps

	mu      sync.Mutex
	running bool

	// schedStopped is closed when RunScheduler returns, so StopScheduler can
	// wait for it rather than merely cancel it.
	schedStopped chan struct{}
	schedDone    sync.Once
	// throughput is the copy rate measured by the last compaction on this
	// machine, used to estimate the next one. Zero until a compaction has run,
	// at which point the estimate stops being a guess.
	throughput int64
}

// New constructs a Service. A nil logger is replaced with the default.
func New(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{d: d, schedStopped: make(chan struct{})}
	// A previous run's measured rate outlives the process that measured it.
	if st, ok, err := Load(d.DBPath); err == nil && ok {
		s.throughput = st.ThroughputBytesPerSec
	}
	// Say so at startup rather than at the first tick an hour later. The
	// scheduler refuses to act on an invalid schedule either way; this is what
	// tells the operator why nothing is happening.
	if err := ValidateEnv(d.Env); err != nil {
		d.Logger.Error("CIX_DB_MAINTENANCE_* does not describe a usable schedule; automatic database maintenance will not run",
			"err", err)
	}
	return s
}

// Stats reports the size, waste and advice for the database file.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	s.mu.Lock()
	rate := s.throughput
	s.mu.Unlock()
	return ReadStats(ctx, s.d.DB, s.d.DBPath, rate)
}

// Checkpoint folds the write-ahead log back into the database file.
func (s *Service) Checkpoint(ctx context.Context) (CheckpointResult, error) {
	return Checkpoint(ctx, s.d.DB, s.d.DBPath)
}

// Reclaim returns free pages to the filesystem, up to maxPages (0 = all).
func (s *Service) Reclaim(ctx context.Context, maxPages int64) (ReclaimResult, error) {
	return Reclaim(ctx, s.d.DB, s.d.DBPath, maxPages)
}

// currentMode reads the database file's own auto-vacuum mode. It is read
// live, never stored, so it is right on any server regardless of how it got
// here — including one upgraded into this feature.
func (s *Service) currentMode(ctx context.Context) AutoVacuum {
	var v int64
	if err := s.d.DB.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&v); err != nil {
		s.d.Logger.Warn("could not read the database auto-vacuum mode", "err", err)
		return AutoVacuumNone
	}
	return autoVacuumFromPragma(v)
}

// Schedule resolves the effective automatic-reclaim schedule.
func (s *Service) Schedule(ctx context.Context) (Schedule, error) {
	return ScheduleFor(ctx, s.d.DB, s.d.Env, s.currentMode(ctx), s.Status())
}

// SaveSchedule applies a patch and returns the schedule as it now resolves.
func (s *Service) SaveSchedule(ctx context.Context, patch SchedulePatch, updatedBy string) (Schedule, error) {
	return SaveSchedule(ctx, s.d.DB, s.d.Env, s.currentMode(ctx), s.Status(), patch, updatedBy)
}

// Status is the public progress report. It reads the journal and the event
// log and touches neither the database nor the session table, because it has
// to keep answering while the server is bringing itself back up on a newly
// compacted file — and because it is the only place the outcome of a
// compaction exists at all.
func (s *Service) Status() State {
	st, ok, err := Load(s.d.DBPath)
	if err != nil {
		s.d.Logger.Warn("could not read the database maintenance journal", "err", err)
	}
	if !ok {
		return State{Phase: PhaseIdle}
	}
	if evs, err := ReadEvents(s.d.DBPath, maxJournalEvents); err == nil && len(evs) > 0 {
		st.Events = evs
	}
	// Progress is measured here rather than written into the journal on a
	// timer. The copy's size on disk *is* the progress, recovery never reads
	// the figure, and an fsynced journal write every couple of seconds is a
	// poor way to spend the read-only window.
	if st.Phase == PhaseCopying {
		if n := sizeOrZero(PathsFor(s.d.DBPath).Copy); n > 0 {
			st.BytesDone = n
		}
	}
	return st
}
