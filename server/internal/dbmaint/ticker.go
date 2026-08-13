package dbmaint

import (
	"context"
	"errors"
	"os"
	"time"
)

// scheduleTick is how often the conditions are re-checked. Deliberately coarse:
// nothing here is urgent, the check itself is a few header reads, and a
// shorter tick would only make it likelier to catch the server mid-indexing.
const scheduleTick = time.Hour

// RunScheduler drives automatic reclaim until ctx is cancelled.
//
// A tick fires only when every condition holds at once — enabled, interval
// elapsed, both thresholds exceeded, inside the window, no jobs in flight,
// and enough disk for the mode. Anything else and the tick is silent and
// tries again next hour, because an operator who set a schedule did not ask
// to be told hourly that today is not the day.
func (s *Service) RunScheduler(ctx context.Context) {
	defer s.schedDone.Do(func() { close(s.schedStopped) })

	t := time.NewTicker(scheduleTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scheduleTickOnce(ctx)
		}
	}
}

// StopScheduler waits for RunScheduler to exit. Part of the quiesce path: the
// scheduler can start a reclaim, which writes, so a compaction has to know it
// has stopped rather than merely been asked to.
func (s *Service) StopScheduler(ctx context.Context) error {
	select {
	case <-s.schedStopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) scheduleTickOnce(ctx context.Context) {
	// A rebuild owns the process while it runs, including the journal. Ticking
	// on top of one would at best waste work and at worst overwrite the record
	// of an operation that is about to restart the server.
	s.mu.Lock()
	busy := s.running
	s.mu.Unlock()
	if busy {
		return
	}

	sched, err := s.Schedule(ctx)
	if err != nil {
		s.d.Logger.Warn("could not read the maintenance schedule", "err", err)
		return
	}
	if !sched.Enabled {
		return
	}
	// An unusable schedule is refused rather than half-honoured. The env layer
	// is not validated when it is read — it cannot be, config.Load has no
	// database to merge it against — so this is where a compose file that sets
	// a window start and forgets the end gets caught. Honouring it would drop
	// the window entirely and freeze the server in the middle of the day.
	if err := ValidateSchedule(sched); err != nil {
		s.d.Logger.Warn("automatic database maintenance is configured but unusable; skipping",
			"err", err, "fix", "correct the CIX_DB_MAINTENANCE_* variables or the schedule in the dashboard")
		return
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		s.d.Logger.Warn("could not read database statistics for the maintenance schedule", "err", err)
		return
	}
	active := 0
	if s.d.ActiveJobs != nil {
		if n, err := s.d.ActiveJobs(ctx); err == nil {
			active = n
		} else {
			// Unknown is treated as busy. Guessing "idle" here would start a
			// read-only window on top of a running index.
			s.d.Logger.Warn("could not count in-flight jobs; skipping this maintenance tick", "err", err)
			return
		}
	}

	ok, why := DueNow(sched, stats, time.Now(), sched.LastRunAt, active)
	if !ok {
		s.d.Logger.Debug("scheduled database maintenance not due", "reason", why)
		return
	}

	switch sched.Mode {
	case ModeIncremental:
		s.runScheduledReclaim(ctx)
	case ModeFull:
		if _, err := s.Compact(ctx); err != nil {
			s.d.Logger.Warn("scheduled database compaction did not start", "err", err)
		}
	}
}

// runScheduledReclaim performs a bounded reclaim and records it.
//
// It journals the outcome even though reclaim needs no crash recovery: the
// schedule's "last run" has to come from somewhere, and it cannot come from a
// table that a later compaction would replace.
func (s *Service) runScheduledReclaim(ctx context.Context) {
	started := time.Now().UTC()
	res, err := s.Reclaim(ctx, 0)
	st := State{
		RunID:     newRunID(),
		Kind:      KindReclaim,
		StartedAt: &started,
		PID:       os.Getpid(),
	}
	finished := time.Now().UTC()
	st.FinishedAt = &finished
	if err != nil {
		st.Phase = PhaseFailed
		st.Error = err.Error()
		st.Message = "scheduled reclaim failed"
		s.d.Logger.Warn("scheduled database reclaim failed", "err", err)
	} else {
		st.Phase = PhaseDone
		st.FreedBytes = res.BytesFreed
		st.Message = "scheduled reclaim complete"
		s.d.Logger.Info("scheduled database reclaim complete",
			"pages_freed", res.PagesFreed, "bytes_freed", res.BytesFreed)
	}
	if errors.Is(err, context.Canceled) {
		// The reclaim was interrupted by the shutdown or the quiesce that
		// precedes a rebuild. That is not an outcome worth reporting, and
		// writing it would put "reclaim failed" on screen for what was in fact
		// a clean stop.
		s.d.Logger.Info("scheduled database reclaim stopped early", "reason", "shutting down")
		return
	}
	s.record(&st, LevelInfo, st.Message)

	// The journal has one owner at a time. A rebuild journals `preparing`
	// before it quiesces, and this reclaim may well be the work being drained —
	// so a Save here would land on top of it and report "reclaim done" for a
	// server that is frozen and about to restart. The lock is held across the
	// write so the answer cannot go stale between asking and acting.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.d.Logger.Info("not recording the scheduled reclaim: a database rebuild has taken over",
			"bytes_freed", st.FreedBytes)
		return
	}
	if serr := Save(s.d.DBPath, st); serr != nil {
		s.d.Logger.Warn("could not record the scheduled reclaim", "err", serr)
	}
}
