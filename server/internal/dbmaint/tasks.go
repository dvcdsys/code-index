package dbmaint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dvcdsys/code-index/server/internal/schedule"
)

// Task names. Wire values: the API addresses tasks by them and an admin's
// saved schedule is keyed on them, so they outlive any renaming in here.
const (
	TaskReclaim = "db.reclaim"
	TaskCompact = "db.compact"
)

// Thresholds gate whether a scheduled run is worth doing at all.
//
// Two of them rather than one: a percentage alone nags on a small database
// where 40% of 12 MB is not worth the work, and an absolute figure alone nags
// on a large one where 500 MB of slack is ordinary operating headroom.
//
// They are not in the database. What an admin adjusts in the dashboard is
// *when* a task runs; how much waste is worth acting on is a property of the
// deployment, so it is a built-in default with an environment override for the
// unusual installs that need one — and one fewer table, one fewer form.
type Thresholds struct {
	MinFreePercent int
	MinFreeBytes   int64
}

// DefaultThresholds are the built-in figures, shared with the dashboard's
// advice so the verdict on screen and the automation agree.
func DefaultThresholds() Thresholds {
	return Thresholds{MinFreePercent: recommendPercent, MinFreeBytes: recommendBytes}
}

// worthwhile reports whether there is enough waste to bother, and why not.
func worthwhile(stats Stats, th Thresholds) (bool, string) {
	if stats.ReclaimablePercent < float64(th.MinFreePercent) || stats.ReclaimableBytes < th.MinFreeBytes {
		return false, "the database has not wasted enough space to be worth reclaiming"
	}
	return true, ""
}

// Tasks are the recurring jobs this package owns, ready to register.
//
// Two separate tasks rather than one with a mode, because they are different
// operations with different costs and an admin may well want both: nightly
// reclaim, which nobody notices, and a rare compaction, which everybody does.
func (s *Service) Tasks() []schedule.Task {
	return []schedule.Task{
		{
			Name:  TaskReclaim,
			Title: "Reclaim free pages",
			Description: "Returns the database's free pages to the filesystem. " +
				"Milliseconds, no read-only window, no restart.",
			DefaultCron: "0 3 * * *",
			// On by default only where it can actually work. A database
			// carried over from an older install is not in incremental mode
			// and the only thing automation could do for it is the expensive
			// rebuild — so an upgrade starts nothing on its own.
			DefaultEnabled: s.currentMode(context.Background()) == AutoVacuumIncremental,
			// Cheap and interruption-free, so a laptop that was asleep at 03:00
			// runs it on waking rather than never running it at all.
			CatchUp: true,
			Handler: s.reclaimTask,
		},
		{
			Name:  TaskCompact,
			Title: "Compact the database",
			Description: "Rebuilds the database into a fresh file. Takes the server read-only " +
				"for the copy, then restarts it.",
			DefaultCron:    "0 4 * * 0",
			DefaultEnabled: false,
			// Never late. "We noticed at 09:00" would mean a read-only window
			// and a restart in the middle of the working day.
			CatchUp: false,
			Handler: s.compactTask,
		},
	}
}

// reclaimTask is the scheduled bounded reclaim.
func (s *Service) reclaimTask(ctx context.Context) error {
	stats, err := s.Stats(ctx)
	if err != nil {
		return fmt.Errorf("read database statistics: %w", err)
	}
	if stats.AutoVacuum != AutoVacuumIncremental {
		return fmt.Errorf("the database is not in incremental reclaim mode")
	}
	if ok, why := worthwhile(stats, s.thresholds); !ok {
		s.d.Logger.Debug("scheduled reclaim not worth running", "reason", why)
		return nil
	}
	if n, err := s.inFlight(ctx); err != nil {
		return err
	} else if n > 0 {
		return fmt.Errorf("indexing is in flight")
	}

	started := time.Now().UTC()
	res, err := s.Reclaim(ctx, 0)
	if err != nil {
		return err
	}
	s.d.Logger.Info("scheduled database reclaim complete",
		"pages_freed", res.PagesFreed, "bytes_freed", res.BytesFreed)
	s.journalReclaim(started, res.BytesFreed)
	return nil
}

// compactTask is the scheduled full rebuild.
func (s *Service) compactTask(ctx context.Context) error {
	stats, err := s.Stats(ctx)
	if err != nil {
		return fmt.Errorf("read database statistics: %w", err)
	}
	if ok, why := worthwhile(stats, s.thresholds); !ok {
		s.d.Logger.Debug("scheduled compaction not worth running", "reason", why)
		return nil
	}
	// Everything else it could refuse on — a rebuild already running, indexing
	// in flight, not enough disk for the copy — is checked by Compact itself,
	// before anything is stopped. Duplicating it here would be a second copy
	// of the rule to keep in step.
	if _, err := s.Compact(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) inFlight(ctx context.Context) (int, error) {
	if s.d.ActiveJobs == nil {
		return 0, nil
	}
	n, err := s.d.ActiveJobs(ctx)
	if err != nil {
		// Unknown is treated as busy. Guessing "idle" would start work on top
		// of a running index.
		return 0, fmt.Errorf("check for in-flight indexing: %w", err)
	}
	return n, nil
}

// journalReclaim records the run in the maintenance journal.
//
// The scheduler already stores when each task ran and how it went; this is the
// *maintenance* record, which the status endpoint and the banner read, and
// which has to survive the restart a compaction performs. It is skipped when a
// rebuild owns the journal — a reclaim finishing during the quiesce that
// precedes one would otherwise report "reclaim done" for a server that is
// frozen and about to restart.
func (s *Service) journalReclaim(started time.Time, freed int64) {
	finished := time.Now().UTC()
	st := State{
		RunID:      newRunID(),
		Kind:       KindReclaim,
		Phase:      PhaseDone,
		StartedAt:  &started,
		FinishedAt: &finished,
		PID:        os.Getpid(),
		FreedBytes: freed,
		Message:    "scheduled reclaim complete",
	}
	s.record(&st, LevelInfo, st.Message)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.d.Logger.Info("not recording the scheduled reclaim: a database rebuild has taken over",
			"bytes_freed", freed)
		return
	}
	if err := Save(s.d.DBPath, st); err != nil {
		s.d.Logger.Warn("could not record the scheduled reclaim", "err", err)
	}
}

// ErrInvalidSchedule is kept for the handlers that map errors to status codes.
var ErrInvalidSchedule = errors.New("invalid schedule")
