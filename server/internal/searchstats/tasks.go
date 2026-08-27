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
func (s *Store) Tasks(logger *slog.Logger) []schedule.Task {
	if logger == nil {
		logger = slog.Default()
	}
	return []schedule.Task{
		{
			Name:  TaskPrune,
			Title: "Prune search statistics",
			Description: "Drops search counters older than the retained window. " +
				"Milliseconds; the all-time totals are not touched.",
			// 03:20 rather than on the hour: the reclaim task already owns
			// 03:00, and two SQLite maintenance statements starting together
			// is a needless overlap even across different files.
			DefaultCron:    "20 3 * * *",
			DefaultEnabled: func() bool { return true },
			// Cheap and interruption-free, so a machine asleep at 03:20 runs it
			// on waking rather than never running it at all.
			CatchUp: true,
			Handler: func(ctx context.Context) error {
				removed, err := s.Prune(ctx, time.Now())
				if err != nil {
					return err
				}
				if removed > 0 {
					logger.Info("pruned search statistics",
						"rows", removed,
						"retention_days", int(WindowRetention/(24*time.Hour)))
				}
				return nil
			},
		},
	}
}
