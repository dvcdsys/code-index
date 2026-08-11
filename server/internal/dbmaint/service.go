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
	Logger *slog.Logger
}

// Service is the admin-facing surface over database maintenance.
type Service struct {
	d Deps

	mu sync.Mutex
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
	s := &Service{d: d}
	// A previous run's measured rate outlives the process that measured it.
	if st, ok, err := Load(d.DBPath); err == nil && ok {
		s.throughput = st.ThroughputBytesPerSec
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
	return st
}
