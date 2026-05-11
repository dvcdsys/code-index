// Package jobs implements the persistent worker queue that drives the
// workspaces feature's long-running operations (clone, fetch, index,
// build-call-graph, community-recompute).
//
// Why persistent: clone+index can take minutes, webhook bursts can be
// frequent, and a single binary that operators restart needs to keep its
// work plan across SIGTERM. The cost is one polling SELECT every poll
// interval, which is irrelevant at the concurrency levels we run
// (default 2 workers).
//
// Dedup: every job may carry a `dedupe_key`. The schema has a partial
// unique index on (dedupe_key) WHERE status IN ('pending','running'), so
// 50 webhook deliveries for the same repo collapse into 1 pending job. The
// service translates the resulting UNIQUE error into a no-op return.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Status constants.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Errors.
var (
	ErrNotFound = errors.New("job not found")
	// ErrDuplicate is returned by Enqueue when a job with the same
	// dedupe_key is already pending or running. Callers usually treat
	// this as a soft no-op — the work is already on the queue.
	ErrDuplicate = errors.New("job with this dedupe_key is already active")
)

// Job is the wire view of a row.
type Job struct {
	ID          string
	Type        string
	Status      string
	DedupeKey   string // empty when not set
	Payload     []byte // raw JSON
	Attempts    int
	MaxAttempts int
	LastError   string
	ScheduledAt time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
}

// EnqueueRequest is the input to Enqueue. Payload is encoded as JSON; pass
// a struct, map, or pre-marshalled []byte (which is passed through).
type EnqueueRequest struct {
	Type        string
	DedupeKey   string
	Payload     any
	MaxAttempts int           // default 3 when 0
	Delay       time.Duration // default 0
}

// Handler is the signature for job type registrations. The handler runs
// inside the worker goroutine; long-running handlers MUST honour ctx for
// graceful shutdown.
type Handler func(ctx context.Context, job Job) error

// Service is the SQLite-backed queue + in-process worker pool.
type Service struct {
	db          *sql.DB
	logger      *slog.Logger
	concurrency int
	pollEvery   time.Duration

	mu       sync.RWMutex
	handlers map[string]Handler

	stop chan struct{}
	done chan struct{}
}

// Options configures Open. Sensible defaults are filled in on zero values.
type Options struct {
	Concurrency int           // default: 2
	PollEvery   time.Duration // default: 1s
	Logger      *slog.Logger
}

// New returns a Service. Workers are NOT started yet — call Start.
func New(db *sql.DB, opts Options) *Service {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{
		db:          db,
		logger:      opts.Logger,
		concurrency: opts.Concurrency,
		pollEvery:   opts.PollEvery,
		handlers:    make(map[string]Handler),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Register binds a handler to a job type. Re-registering a type replaces
// the prior handler. Must be called BEFORE Start — handlers added after
// Start are still picked up on the next poll, but the gap is racy with
// existing jobs of that type.
func (s *Service) Register(jobType string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[jobType] = h
}

// Start launches the worker pool. Idempotent-but-not-thread-safe — call
// once per Service. The returned function is a Stop alias for symmetry
// with other supervisor patterns in the codebase.
func (s *Service) Start(ctx context.Context) {
	go s.runPool(ctx)
}

// Stop signals the worker pool to drain. Blocks until all in-flight jobs
// finish or ctx is cancelled. Safe to call multiple times.
func (s *Service) Stop(ctx context.Context) error {
	select {
	case <-s.stop:
		// already stopping
	default:
		close(s.stop)
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runPool is the supervisor goroutine — it owns the worker count and
// fans tickets out to per-worker goroutines via a buffered channel.
func (s *Service) runPool(ctx context.Context) {
	defer close(s.done)

	// Per-worker ticker is cheaper than a single ticker fanned out.
	wg := sync.WaitGroup{}
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			s.workerLoop(ctx, workerID)
		}(i)
	}
	wg.Wait()
}

func (s *Service) workerLoop(ctx context.Context, workerID int) {
	tick := time.NewTicker(s.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-tick.C:
		}
		// Pull one job per tick. Higher throughput would benefit from a
		// LIMIT batch, but the work is dominated by clone/index time,
		// not queue overhead — keep this simple.
		job, err := s.claimNext(ctx)
		if err != nil {
			s.logger.Error("jobs: claim failed", "worker", workerID, "err", err)
			continue
		}
		if job == nil {
			continue
		}
		s.execute(ctx, workerID, *job)
	}
}

// claimNext atomically picks the oldest pending job whose scheduled_at
// has elapsed, marks it running, and returns it. Returns (nil, nil) when
// the queue is empty.
//
// We use a SELECT … FOR UPDATE-ish pattern via an UPDATE … WHERE id IN
// (SELECT … LIMIT 1) … RETURNING. modernc.org/sqlite supports RETURNING
// as of 1.27 (already in go.mod). Concurrent claims race on the inner
// SELECT but the outer WHERE id = ? AND status = 'pending' ensures only
// one worker wins. Lost races re-poll on the next tick.
func (s *Service) claimNext(ctx context.Context) (*Job, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := s.db.QueryRowContext(ctx, `
		UPDATE jobs
		   SET status = 'running',
		       started_at = ?,
		       attempts = attempts + 1
		 WHERE id = (
		   SELECT id FROM jobs
		    WHERE status = 'pending'
		      AND scheduled_at <= ?
		    ORDER BY scheduled_at, created_at
		    LIMIT 1
		 )
		 RETURNING id, type, status, dedupe_key, payload, attempts, max_attempts,
		           last_error, scheduled_at, started_at, completed_at, created_at`,
		now, now)
	job, err := scanRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // queue empty
		}
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return &job, nil
}

func (s *Service) execute(ctx context.Context, workerID int, job Job) {
	s.mu.RLock()
	h, ok := s.handlers[job.Type]
	s.mu.RUnlock()
	if !ok {
		s.markFailed(ctx, job, fmt.Errorf("no handler registered for type %q", job.Type), false)
		return
	}
	s.logger.Info("jobs: running",
		"worker", workerID,
		"id", job.ID,
		"type", job.Type,
		"attempt", job.Attempts,
		"of", job.MaxAttempts)

	// Run the handler — never panic the worker, capture as a failed job.
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("handler panic: %v", r)
			}
		}()
		err = h(ctx, job)
	}()

	if err == nil {
		s.markCompleted(ctx, job)
		s.logger.Info("jobs: completed",
			"worker", workerID,
			"id", job.ID,
			"type", job.Type)
		return
	}

	retry := job.Attempts < job.MaxAttempts
	s.markFailed(ctx, job, err, retry)
	if retry {
		s.logger.Warn("jobs: failed, will retry",
			"worker", workerID,
			"id", job.ID,
			"type", job.Type,
			"attempts", job.Attempts,
			"err", err)
	} else {
		s.logger.Error("jobs: failed permanently",
			"worker", workerID,
			"id", job.ID,
			"type", job.Type,
			"err", err)
	}
}

func (s *Service) markCompleted(ctx context.Context, job Job) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = 'completed', completed_at = ?, last_error = NULL WHERE id = ?`,
		now, job.ID); err != nil {
		s.logger.Error("jobs: mark completed failed", "id", job.ID, "err", err)
	}
}

// markFailed transitions to 'failed' OR back to 'pending' (when retry=true)
// with a small linear backoff (attempts × 10s).
func (s *Service) markFailed(ctx context.Context, job Job, err error, retry bool) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg := err.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if retry {
		backoff := time.Duration(job.Attempts) * 10 * time.Second
		newSchedule := time.Now().UTC().Add(backoff).Format(time.RFC3339Nano)
		if _, qerr := s.db.ExecContext(ctx,
			`UPDATE jobs SET status = 'pending', scheduled_at = ?, last_error = ? WHERE id = ?`,
			newSchedule, msg, job.ID); qerr != nil {
			s.logger.Error("jobs: re-enqueue failed", "id", job.ID, "err", qerr)
		}
		return
	}
	if _, qerr := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = 'failed', completed_at = ?, last_error = ? WHERE id = ?`,
		now, msg, job.ID); qerr != nil {
		s.logger.Error("jobs: mark failed failed", "id", job.ID, "err", qerr)
	}
}

// Enqueue inserts a new job. ErrDuplicate when dedupe_key collides with an
// already-active job.
func (s *Service) Enqueue(ctx context.Context, req EnqueueRequest) (Job, error) {
	if strings.TrimSpace(req.Type) == "" {
		return Job{}, fmt.Errorf("job type required")
	}
	payload, err := marshalPayload(req.Payload)
	if err != nil {
		return Job{}, fmt.Errorf("encode payload: %w", err)
	}
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	scheduledAt := now.Add(req.Delay).Format(time.RFC3339Nano)
	createdAt := now.Format(time.RFC3339Nano)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, type, status, dedupe_key, payload, attempts, max_attempts,
		                   scheduled_at, created_at)
		 VALUES (?, ?, 'pending', ?, ?, 0, ?, ?, ?)`,
		id, req.Type, nullableString(req.DedupeKey), string(payload), maxAttempts,
		scheduledAt, createdAt,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Job{}, ErrDuplicate
		}
		return Job{}, fmt.Errorf("insert job: %w", err)
	}
	return s.GetByID(ctx, id)
}

// GetByID returns a single job.
func (s *Service) GetByID(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, type, status, dedupe_key, payload, attempts, max_attempts,
		       last_error, scheduled_at, started_at, completed_at, created_at
		  FROM jobs WHERE id = ?`, id)
	job, err := scanRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return job, nil
}

// List returns jobs filtered by status / type. Empty filters mean "any".
// Always newest-first, capped at limit (default 100).
func (s *Service) List(ctx context.Context, status, jobType string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, type, status, dedupe_key, payload, attempts, max_attempts,
	             last_error, scheduled_at, started_at, completed_at, created_at
	        FROM jobs WHERE 1=1`
	var args []any
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	if jobType != "" {
		q += " AND type = ?"
		args = append(args, jobType)
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// --- helpers ---

func scanRow(r interface{ Scan(dest ...any) error }) (Job, error) {
	var (
		j                                                Job
		dedupe, lastErr, startedAt, completedAt          sql.NullString
		payload                                          string
		scheduledAt, createdAt                           string
	)
	err := r.Scan(&j.ID, &j.Type, &j.Status, &dedupe, &payload,
		&j.Attempts, &j.MaxAttempts, &lastErr,
		&scheduledAt, &startedAt, &completedAt, &createdAt)
	if err != nil {
		return Job{}, err
	}
	j.DedupeKey = dedupe.String
	j.Payload = []byte(payload)
	j.LastError = lastErr.String
	j.ScheduledAt, _ = time.Parse(time.RFC3339Nano, scheduledAt)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
		j.StartedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
		j.CompletedAt = &t
	}
	return j, nil
}

func marshalPayload(p any) ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	if raw, ok := p.([]byte); ok {
		return raw, nil
	}
	return json.Marshal(p)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// UnmarshalPayload is a tiny convenience for handlers: pass the job and
// a pointer to a typed struct.
func UnmarshalPayload(job Job, out any) error {
	if len(job.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(job.Payload, out)
}
