// Package pollscheduler is the single shared watcher that drives git polling
// for every polling-enabled repo. Webhooks require repo-admin rights to
// install; polling is the fallback for read-only remotes.
//
// One goroutine, not one-per-repo: every tick it asks gitrepos for the repos
// whose next_poll_at has elapsed and enqueues a clone_repo job for each into
// the shared internal/jobs queue. That queue is bounded by
// CIX_WORKER_CONCURRENCY, so a fleet of repos coming due at once can never
// spike indexing concurrency — the work simply queues and drains. The
// clone_repo → index_repo pipeline (internal/repojobs) is reused verbatim,
// including PAT auth and incremental tree.Diff reindex.
//
// Cadence is "interval from the end of the last index run": the completion
// handlers in repojobs set next_poll_at = now + interval when a cycle
// finishes. This scheduler additionally writes a provisional floor
// (now + interval) at enqueue time so a single repo can't be re-enqueued every
// tick while its job is in flight, and so a process restart still finds a
// future-dated next_poll_at to re-pick. Job dedupe (clone:<hash>) makes any
// overlap a soft no-op.
package pollscheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/repojobs"
)

// Service is the shared poll watcher. Construct with New, run with Run.
type Service struct {
	gitRepos    *gitrepos.Service
	jobs        *jobs.Service
	tick        time.Duration
	defaultSecs int
	minSecs     int
	logger      *slog.Logger
}

// New builds a Service. tick defaults to 30s, defaultSecs to 300 (5m), and
// minSecs to 60 on non-positive inputs. A nil logger falls back to the
// default.
func New(gr *gitrepos.Service, j *jobs.Service, tick time.Duration, defaultSecs, minSecs int, logger *slog.Logger) *Service {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	if defaultSecs <= 0 {
		defaultSecs = 300
	}
	if minSecs <= 0 {
		minSecs = 60
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		gitRepos:    gr,
		jobs:        j,
		tick:        tick,
		defaultSecs: defaultSecs,
		minSecs:     minSecs,
		logger:      logger,
	}
}

// Run loops on a ticker, draining due polls, until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	s.logger.Info("poll scheduler started", "tick", s.tick, "default_interval_s", s.defaultSecs)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tickOnce(ctx)
		}
	}
}

// tickOnce processes one scan of due repos. Exported behaviour is unit-tested
// directly (no ticker needed). Errors are logged, never fatal — a transient DB
// hiccup just means we retry next tick.
func (s *Service) tickOnce(ctx context.Context) {
	due, err := s.gitRepos.ListDue(ctx, time.Now())
	if err != nil {
		s.logger.Warn("poll scheduler: list due failed", "err", err)
		return
	}
	for _, repo := range due {
		// Provisional floor: advance next_poll_at before enqueueing so the
		// next tick won't re-pick this repo while its job is in flight.
		// The completion handler overwrites this with the authoritative
		// "end of indexing + interval" value.
		secs := gitrepos.EffectivePollInterval(repo, s.defaultSecs, s.minSecs)
		floor := time.Now().UTC().Add(time.Duration(secs) * time.Second)
		if err := s.gitRepos.RescheduleNextPoll(ctx, repo.ProjectPath, floor); err != nil {
			s.logger.Warn("poll scheduler: provisional reschedule failed",
				"project", repo.ProjectPath, "err", err)
			continue
		}
		if err := repojobs.EnqueueClone(ctx, s.jobs, repo.ProjectPath); err != nil {
			s.logger.Warn("poll scheduler: enqueue clone failed",
				"project", repo.ProjectPath, "err", err)
			continue
		}
		s.logger.Info("poll scheduler: enqueued poll", "project", repo.ProjectPath, "interval_s", secs)
	}
}
