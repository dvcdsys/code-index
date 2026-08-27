package searchstats

import (
	"context"
	"log/slog"
	"time"

	"github.com/dvcdsys/code-index/server/internal/schedule"
)

// TaskPrune is the recurring job that drops window rows past the retention
// horizon. A wire value — an admin's saved schedule is keyed on it — so it
// outlives any renaming in here.
const TaskPrune = "searchstats.prune"

// Tasks are the recurring jobs this package owns, ready to register.
//
// Pruning is scheduled rather than done on every flush. The flush runs every
// few seconds and would be issuing a DELETE that matches nothing almost every
// time; once a day at a quiet hour removes exactly the same rows for one
// statement instead of thousands.
//
// It is registered even though nothing catastrophic happens if it never runs —
// the window tables are bounded by ACTIVITY, not by time, so an unpruned
// database grows with traffic rather than with the calendar. That is precisely
// why it has to be a real registered task and not a "safe to run periodically"
// helper nobody calls: sessions.GC in this same codebase carries that comment
// and has no caller anywhere, so expired sessions are never swept. This one is
// wired in main.go at the same place dbmaint's tasks are.
// LiveProjects reports the project paths that still exist, so the prune task
// can sweep counters whose project is gone. Supplied by the caller because this
// package deliberately cannot see the system database.
type LiveProjects func(ctx context.Context) ([]string, error)

// HolderTasks registers the maintenance task against a holder rather than a
// store, so it survives the feature being switched on and off.
//
// The handler resolves the store on every run instead of capturing it. A task
// bound to whatever was open at boot would stop working the moment an admin
// toggled the feature, and re-registering on each toggle would mean an admin's
// saved schedule for it had to be carried across — for a task that is a
// no-op while the feature is off anyway.
func HolderTasks(h *Holder, logger *slog.Logger, live LiveProjects) []schedule.Task {
	if logger == nil {
		logger = slog.Default()
	}
	t := pruneTaskMeta()
	// WithOpenStore rather than Store(): resolving the store and then using it
	// leaves a window for a Disable to close the pool mid-run, which turns a
	// routine prune into `sql: database is closed` and a red row in the
	// scheduled-tasks list. Skipping while off is what the task always meant to
	// do; this just makes the check cover the work as well as the decision.
	t.Handler = func(ctx context.Context) error {
		return h.WithOpenStore(func(store *Store) error {
			return store.maintain(ctx, logger, live)
		})
	}
	// Asked afresh at every resolution rather than captured once, for the same
	// reason dbmaint asks whether its reclaim still makes sense: the answer
	// changes under a running server.
	t.DefaultEnabled = func() bool { return h.Enabled() }
	return []schedule.Task{t}
}

// pruneTaskMeta is the task's description without a handler, so both
// constructors can supply their own.
//
// It exists so neither of them has to build a throwaway Store to harvest the
// metadata. That worked only because nothing outside the handler read the
// receiver — and the day somebody puts the database path into the Description,
// it becomes a nil-pointer panic at boot with nothing in the type system to
// warn them.
func pruneTaskMeta() schedule.Task {
	return schedule.Task{
		Name:  TaskPrune,
		Title: "Prune search statistics",
		Description: "Drops search counters older than the retained window. " +
			"Milliseconds; the all-time totals are not touched.",
		// 03:20 rather than on the hour: the reclaim task already owns 03:00,
		// and two SQLite maintenance statements starting together is a needless
		// overlap even across different files.
		DefaultCron:    "20 3 * * *",
		DefaultEnabled: func() bool { return true },
		// Cheap and interruption-free, so a machine asleep at 03:20 runs it on
		// waking rather than never running it at all.
		CatchUp: true,
	}
}

func (s *Store) Tasks(logger *slog.Logger, live LiveProjects) []schedule.Task {
	if logger == nil {
		logger = slog.Default()
	}
	t := pruneTaskMeta()
	t.Handler = func(ctx context.Context) error {
		return s.maintain(ctx, logger, live)
	}
	return []schedule.Task{t}
}

// maintain is one maintenance run: prune the window tier, then sweep counters
// whose project is gone.
func (s *Store) maintain(ctx context.Context, logger *slog.Logger, live LiveProjects) error {
	removed, err := s.Prune(ctx, time.Now())
	if err != nil {
		return err
	}
	if removed > 0 {
		logger.Info("pruned search statistics",
			"rows", removed,
			"retention_days", int(WindowRetention/(24*time.Hour)))
	}
	return s.sweepOrphans(ctx, logger, live)
}

// sweepOrphans drops counters whose project no longer exists.
//
// Failing to enumerate the live projects is NOT an error the task reports. The
// prune it runs alongside has already succeeded by this point, and turning a
// transient read of somebody else's database into a red scheduled-task row
// would misreport which half failed. A warning is the honest signal: the sweep
// is a safety net, and a net that misses one night still catches the orphan on
// the next.
func (s *Store) sweepOrphans(ctx context.Context, logger *slog.Logger, live LiveProjects) error {
	if live == nil {
		return nil
	}
	paths, err := live(ctx)
	if err != nil {
		logger.Warn("could not list live projects — skipping the search-statistics orphan sweep", "err", err)
		return nil
	}
	dropped, err := s.ForgetAllExcept(ctx, paths)
	if err != nil {
		return err
	}
	if dropped > 0 {
		// Worth a line rather than silence: reaching here means a project
		// delete did not discard its counters at the time, which is a bug
		// somewhere upstream even though this cleaned up after it.
		logger.Info("swept search statistics for projects that no longer exist", "projects", dropped)
	}
	return nil
}
