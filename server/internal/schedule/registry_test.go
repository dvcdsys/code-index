package schedule

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixture gives a registry over a real database with a clock the test drives.
func fixture(t *testing.T) (*Registry, *sql.DB, *fakeClock) {
	t.Helper()
	sdb, err := db.Open(filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })

	clock := &fakeClock{now: time.Date(2026, 8, 13, 12, 0, 0, 0, time.Local)}
	r := New(sdb, quiet())
	r.now = clock.Now
	return r, sdb, clock
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// recorder is a handler that remembers when it was called.
type recorder struct {
	mu    sync.Mutex
	calls []time.Time
	err   error
	block chan struct{}
	clock *fakeClock
}

func (rec *recorder) handler(ctx context.Context) error {
	rec.mu.Lock()
	rec.calls = append(rec.calls, rec.clock.Now())
	block := rec.block
	err := rec.err
	rec.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.calls)
}

// waitFor polls until cond holds, so a handler running in its own goroutine can
// be observed without sleeping for a fixed time.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func daily(rec *recorder, catchUp bool) Task {
	return Task{
		Name:           "test.daily",
		Title:          "Daily",
		DefaultCron:    "0 0 * * *",
		DefaultEnabled: true,
		CatchUp:        catchUp,
		Handler:        rec.handler,
	}
}

// The schedule is anchored to the clock, not to when the previous run
// finished. A slow run must not push tomorrow's slot later — that is how a
// schedule drifts a little further every day until "every night" means
// "sometime in the afternoon".
func TestRegistry_DoesNotDriftWhenARunIsSlow(t *testing.T) {
	r, sdb, clock := fixture(t)
	rec := &recorder{clock: clock, block: make(chan struct{})}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	// Arm, then arrive at midnight.
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	midnight := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	clock.set(midnight)
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	waitFor(t, "the handler to start", func() bool { return rec.count() == 1 })

	// The next run is already recorded, before the handler has returned.
	var next string
	if err := sdb.QueryRow(`SELECT next_run_at FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&next); err != nil {
		t.Fatalf("read next_run_at: %v", err)
	}
	got, err := time.Parse(time.RFC3339Nano, next)
	if err != nil {
		t.Fatalf("parse %q: %v", next, err)
	}
	want := midnight.AddDate(0, 0, 1)
	if !got.Local().Equal(want) {
		t.Errorf("next run = %s, want %s — the schedule tracks the clock, not the handler",
			got.Local().Format(time.RFC3339), want.Format(time.RFC3339))
	}
	close(rec.block)
}

// The slot is claimed on disk before the handler is entered. This is the one
// that keeps database compaction from looping: it re-executes the process, and
// a slot still marked due when the new process starts fires it again.
func TestRegistry_ClaimsTheSlotBeforeRunningSoARestartDoesNotRefire(t *testing.T) {
	r, sdb, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	clock.set(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	waitFor(t, "the first run", func() bool { return rec.count() == 1 })

	// A brand-new registry over the same database, as after a restart, at the
	// same instant the compaction would have re-executed at.
	r2 := New(sdb, quiet())
	r2.now = clock.Now
	rec2 := &recorder{clock: clock}
	r2.Register(daily(rec2, false), nil)
	if err := r2.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := rec2.count(); n != 0 {
		t.Errorf("the task fired %d more times after a restart in the same slot; a task that "+
			"restarts the server would do this forever", n)
	}
}

// A slot that passed while the process was down is never silently forgotten:
// either it is caught up, or it is stepped over and the *next* one is armed.
// What must never happen is a task left with no future run at all.
func TestRegistry_AMissedSlotAlwaysLeavesAFutureRun(t *testing.T) {
	for _, catchUp := range []bool{true, false} {
		name := "skips"
		if catchUp {
			name = "catches up"
		}
		t.Run(name, func(t *testing.T) {
			r, sdb, clock := fixture(t)
			rec := &recorder{clock: clock}
			r.Register(daily(rec, catchUp), nil)
			ctx := context.Background()

			if err := r.considerOne(ctx, "test.daily"); err != nil {
				t.Fatalf("arm: %v", err)
			}
			// The machine was asleep through midnight and wakes at 09:00.
			clock.set(time.Date(2026, 8, 14, 9, 0, 0, 0, time.Local))
			if err := r.considerOne(ctx, "test.daily"); err != nil {
				t.Fatalf("wake: %v", err)
			}

			if catchUp {
				waitFor(t, "the missed run to be caught up", func() bool { return rec.count() == 1 })
			} else {
				time.Sleep(50 * time.Millisecond)
				if n := rec.count(); n != 0 {
					t.Errorf("a task that must not catch up ran %d times nine hours late", n)
				}
			}

			var next sql.NullString
			if err := sdb.QueryRow(
				`SELECT next_run_at FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&next); err != nil {
				t.Fatalf("read next_run_at: %v", err)
			}
			if !next.Valid {
				t.Fatal("no future run is scheduled — the task has been lost")
			}
			got, err := time.Parse(time.RFC3339Nano, next.String)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !got.After(clock.Now()) {
				t.Errorf("next run %s is not in the future", got.Local().Format(time.RFC3339))
			}
		})
	}
}

// Even a row with no schedule at all re-arms itself. Nothing about the
// recovery path depends on a previous run having happened.
func TestRegistry_ReArmsFromAnEmptyRow(t *testing.T) {
	r, sdb, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	if _, err := sdb.Exec(`
		INSERT INTO scheduled_tasks (name, cron, enabled, updated_at)
		VALUES ('test.daily', '0 0 * * *', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("consider: %v", err)
	}
	var next sql.NullString
	if err := sdb.QueryRow(
		`SELECT next_run_at FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&next); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !next.Valid {
		t.Fatal("a row with no next_run_at was not re-armed")
	}
	got, _ := time.Parse(time.RFC3339Nano, next.String)
	if want := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local); !got.Local().Equal(want) {
		t.Errorf("next = %s, want %s", got.Local().Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if rec.count() != 0 {
		t.Error("re-arming fired the task")
	}
}

// Changing the expression re-arms rather than firing on the time the old one
// produced.
func TestRegistry_ReArmsWhenTheExpressionChanges(t *testing.T) {
	r, sdb, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	if _, err := r.Save(ctx, "test.daily", strptr("0 3 * * *"), nil, "admin@example.test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	var next string
	if err := sdb.QueryRow(
		`SELECT next_run_at FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&next); err != nil {
		t.Fatalf("read: %v", err)
	}
	got, _ := time.Parse(time.RFC3339Nano, next)
	if want := time.Date(2026, 8, 14, 3, 0, 0, 0, time.Local); !got.Local().Equal(want) {
		t.Errorf("next = %s, want %s", got.Local().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A task slower than its own schedule falls behind rather than running twice
// at once.
func TestRegistry_DoesNotOverlapItself(t *testing.T) {
	r, _, clock := fixture(t)
	rec := &recorder{clock: clock, block: make(chan struct{})}
	r.Register(Task{
		Name: "test.minutely", Title: "Minutely",
		DefaultCron: "* * * * *", DefaultEnabled: true,
		Handler: rec.handler,
	}, nil)
	ctx := context.Background()

	if err := r.considerOne(ctx, "test.minutely"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	clock.set(time.Date(2026, 8, 13, 12, 1, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.minutely"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	waitFor(t, "the first run", func() bool { return rec.count() == 1 })

	for i := 2; i < 6; i++ {
		clock.set(time.Date(2026, 8, 13, 12, i, 0, 0, time.Local))
		if err := r.considerOne(ctx, "test.minutely"); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := rec.count(); n != 1 {
		t.Errorf("ran %d times while the first run was still going, want 1", n)
	}
	close(rec.block)
}

// A handler that panics is an ordinary failure, not the end of the scheduler.
func TestRegistry_SurvivesAPanickingHandler(t *testing.T) {
	r, sdb, clock := fixture(t)
	r.Register(Task{
		Name: "test.boom", Title: "Boom",
		DefaultCron: "0 0 * * *", DefaultEnabled: true,
		Handler: func(context.Context) error { panic("no") },
	}, nil)
	ctx := context.Background()

	if err := r.considerOne(ctx, "test.boom"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	clock.set(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.boom"); err != nil {
		t.Fatalf("fire: %v", err)
	}

	waitFor(t, "the failure to be recorded", func() bool {
		var status sql.NullString
		_ = sdb.QueryRow(`SELECT last_status FROM scheduled_tasks WHERE name = 'test.boom'`).Scan(&status)
		return status.String == "failed"
	})
	var lastErr sql.NullString
	if err := sdb.QueryRow(
		`SELECT last_error FROM scheduled_tasks WHERE name = 'test.boom'`).Scan(&lastErr); err != nil {
		t.Fatalf("read: %v", err)
	}
	if lastErr.String == "" {
		t.Error("a panicking handler recorded no reason")
	}
}

// A disabled task never runs, however overdue it looks.
func TestRegistry_DisabledNeverRuns(t *testing.T) {
	r, _, clock := fixture(t)
	rec := &recorder{clock: clock}
	task := daily(rec, true)
	task.DefaultEnabled = false
	r.Register(task, nil)
	ctx := context.Background()

	clock.set(time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("consider: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if rec.count() != 0 {
		t.Error("a disabled task ran")
	}
}

func TestRegistry_SaveRejectsAnUnusableExpression(t *testing.T) {
	r, _, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)

	if _, err := r.Save(context.Background(), "test.daily", strptr("0 0 30 2 *"), nil, ""); !errors.Is(err, ErrInvalidCron) {
		t.Errorf("saving 30 February = %v, want %v", err, ErrInvalidCron)
	}
	if _, err := r.Save(context.Background(), "nope", strptr("0 0 * * *"), nil, ""); !errors.Is(err, ErrUnknownTask) {
		t.Errorf("saving an unregistered task = %v, want %v", err, ErrUnknownTask)
	}
}

// The environment supplies a default an admin can still override.
func TestRegistry_PrecedenceIsDatabaseThenEnvironmentThenDefault(t *testing.T) {
	r, _, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), strptr("0 4 * * *"))
	ctx := context.Background()

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].Cron != "0 4 * * *" {
		t.Errorf("cron = %q, want the environment's %q", list[0].Cron, "0 4 * * *")
	}
	if list[0].Configured {
		t.Error("an environment default counts as configured by an admin")
	}

	if _, err := r.Save(ctx, "test.daily", strptr("30 5 * * *"), nil, ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	list, err = r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].Cron != "30 5 * * *" {
		t.Errorf("cron = %q, want the saved %q", list[0].Cron, "30 5 * * *")
	}
	if !list[0].Configured {
		t.Error("a saved schedule does not report as configured")
	}
	if len(list[0].NextRuns) != 3 {
		t.Errorf("got %d next runs, want 3 — the dashboard previews the schedule from these", len(list[0].NextRuns))
	}
}

func strptr(s string) *string { return &s }
