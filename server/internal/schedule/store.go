package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// taskRow is one row of scheduled_tasks, plus whether there was one.
type taskRow struct {
	found bool
	// configured is set only by an admin saving a schedule. A row that exists
	// merely because the scheduler armed the task is not a decision anybody
	// made, and must not override the built-in default.
	configured bool
	cron       string
	enabled    bool

	// cronUsed is the expression next_run_at was derived from. Keeping it lets
	// the loop notice that the expression changed underneath a schedule it had
	// already armed, and re-arm instead of firing on a stale time.
	cronUsed  string
	nextRunAt *time.Time

	lastRunAt  *time.Time
	lastStatus string
	lastError  string
	lastMillis int64
}

func (r *Registry) load(ctx context.Context, name string) (taskRow, error) {
	var (
		row                       taskRow
		configured                int64
		enabled                   sql.NullInt64
		cronUsed, status, errText sql.NullString
		nextRun, lastRun          sql.NullString
		millis                    sql.NullInt64
		cron                      sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT configured, cron, enabled, cron_used, next_run_at, last_run_at,
		       last_status, last_error, last_millis
		  FROM scheduled_tasks WHERE name = ?`, name).
		Scan(&configured, &cron, &enabled, &cronUsed, &nextRun, &lastRun, &status, &errText, &millis)
	if errors.Is(err, sql.ErrNoRows) {
		return taskRow{}, nil
	}
	if err != nil {
		return taskRow{}, fmt.Errorf("select scheduled_tasks %q: %w", name, err)
	}

	row.found = true
	row.configured = configured != 0
	row.cron = cron.String
	row.enabled = enabled.Int64 != 0
	row.cronUsed = cronUsed.String
	row.nextRunAt = parseTime(nextRun)
	row.lastRunAt = parseTime(lastRun)
	row.lastStatus = status.String
	row.lastError = errText.String
	row.lastMillis = millis.Int64
	return row, nil
}

// arm records when a task is next due, without claiming a run.
//
// It writes only derived columns. Touching `enabled` here would be a bug with
// teeth: the first arming of a task would persist whatever the loop happened to
// be holding and, on the next pass, that persisted value would win over the
// default — so a task enabled by default would switch itself off the moment the
// scheduler noticed it.
func (r *Registry) arm(ctx context.Context, name, cron string, next time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (name, cron_used, next_run_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			cron_used   = excluded.cron_used,
			next_run_at = excluded.next_run_at`,
		name, cron, formatTime(next), formatTime(r.now()))
	if err != nil {
		return fmt.Errorf("arm scheduled task %q: %w", name, err)
	}
	return nil
}

// claim marks the slot as taken and moves the schedule on, before the handler
// runs. See the call site for why the ordering is load-bearing.
func (r *Registry) claim(ctx context.Context, name, cron string, at, next time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (name, cron_used, next_run_at, last_run_at, last_status, updated_at)
		VALUES (?, ?, ?, ?, 'running', ?)
		ON CONFLICT(name) DO UPDATE SET
			cron_used   = excluded.cron_used,
			next_run_at = excluded.next_run_at,
			last_run_at = excluded.last_run_at,
			last_status = 'running',
			last_error  = NULL`,
		name, cron, formatTime(next), formatTime(at), formatTime(r.now()))
	if err != nil {
		return fmt.Errorf("claim the slot for %q: %w", name, err)
	}
	return nil
}

// record stores the outcome of a finished run.
func (r *Registry) record(ctx context.Context, name, status, errText string, took time.Duration) error {
	var e any
	if errText != "" {
		e = errText
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_tasks
		   SET last_status = ?, last_error = ?, last_millis = ?, updated_at = ?
		 WHERE name = ?`,
		status, e, took.Milliseconds(), formatTime(r.now()), name)
	if err != nil {
		return fmt.Errorf("record the outcome of %q: %w", name, err)
	}
	return nil
}

// upsert applies an admin's change.
func (r *Registry) upsert(ctx context.Context, name, cron string, enabled bool, next *time.Time, by string) error {
	var byVal any
	if by != "" {
		byVal = by
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (name, configured, cron, enabled, cron_used, next_run_at, updated_at, updated_by)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			configured  = 1,
			cron        = excluded.cron,
			enabled     = excluded.enabled,
			cron_used   = excluded.cron_used,
			next_run_at = excluded.next_run_at,
			updated_at  = excluded.updated_at,
			updated_by  = excluded.updated_by`,
		name, cron, boolToInt(enabled), cron, formatTimePtr(next), formatTime(r.now()), byVal)
	if err != nil {
		return fmt.Errorf("save scheduled task %q: %w", name, err)
	}
	return nil
}

func parseTime(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	// Stored in UTC, compared against a local clock. Local() so a cron
	// expression an admin wrote in their own timezone is honoured in it.
	local := t.Local()
	return &local
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
