package dbmaint

import (
	"context"
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
	sched, err := s.Schedule(ctx)
	if err != nil {
		s.d.Logger.Warn("could not read the maintenance schedule", "err", err)
		return
	}
	if !sched.Enabled {
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
		if _, err := s.Compact(ctx, TargetKeep); err != nil {
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
	s.record(&st, LevelInfo, st.Message)
	if serr := Save(s.d.DBPath, st); serr != nil {
		s.d.Logger.Warn("could not record the scheduled reclaim", "err", serr)
	}
}
