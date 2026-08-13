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
		DefaultEnabled: alwaysOn,
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
		DefaultCron: "* * * * *", DefaultEnabled: alwaysOn,
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
		DefaultCron: "0 0 * * *", DefaultEnabled: alwaysOn,
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
	task.DefaultEnabled = func() bool { return false }
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

func alwaysOn() bool { return true }

// Stop must not return while a handler is still running.
//
// This is the contract the database compactor buys its safety with: it calls
// Stop as part of quiescing, then freezes writes and copies the file. A reclaim
// still inside `incremental_vacuum` when Stop returned would go on writing into
// the database being copied, and the HTTP write gate cannot see it — the
// handler holds no request.
func TestRegistry_StopWaitsForHandlersToFinish(t *testing.T) {
	r, _, clock := fixture(t)
	release := make(chan struct{})
	rec := &recorder{clock: clock, block: release}
	r.Register(daily(rec, false), nil)

	ctx, cancel := context.WithCancel(context.Background())
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	clock.set(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	waitFor(t, "the handler to start", func() bool { return rec.count() == 1 })

	// The loop was never started, so close its channel by hand: this test is
	// about the handler half of Stop.
	r.once.Do(func() { close(r.stopped) })

	returned := make(chan error, 1)
	go func() { returned <- r.Stop(context.Background()) }()

	select {
	case err := <-returned:
		t.Fatalf("Stop returned (%v) while a handler was still running — the compactor "+
			"would now freeze and copy the database underneath it", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("Stop = %v, want nil once the handler finished", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after the handler finished")
	}
	cancel()
}

// And it reports rather than hangs when the handler outlives the budget.
func TestRegistry_StopReportsAHandlerThatWillNotFinish(t *testing.T) {
	r, _, clock := fixture(t)
	release := make(chan struct{})
	defer close(release)
	rec := &recorder{clock: clock, block: release}
	r.Register(daily(rec, false), nil)

	ctx := context.Background()
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	clock.set(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local))
	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	waitFor(t, "the handler to start", func() bool { return rec.count() == 1 })
	r.once.Do(func() { close(r.stopped) })

	bounded, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Stop(bounded); err == nil {
		t.Error("Stop reported success with a handler still running")
	}
}

// A run marked in flight by a process that never came back is interrupted, not
// running. Nothing else corrects it, so a host that powers off nightly would
// show a permanent phantom on the dashboard.
func TestRegistry_ClearsARunLeftBehindByADeadProcess(t *testing.T) {
	r, sdb, _ := fixture(t)
	rec := &recorder{clock: &fakeClock{}}
	r.Register(daily(rec, false), nil)

	if _, err := sdb.Exec(`
		INSERT INTO scheduled_tasks (name, cron_used, next_run_at, last_run_at, last_status, updated_at)
		VALUES ('test.daily', '0 0 * * *', '2030-01-01T00:00:00Z', '2026-08-13T00:00:00Z',
		        'running', '2026-08-13T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := r.clearStaleRuns(context.Background()); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var status, lastErr sql.NullString
	if err := sdb.QueryRow(
		`SELECT last_status, last_error FROM scheduled_tasks WHERE name = 'test.daily'`).
		Scan(&status, &lastErr); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status.String != "interrupted" {
		t.Errorf("last_status = %q, want %q", status.String, "interrupted")
	}
	if lastErr.String == "" {
		t.Error("no explanation recorded for the interrupted run")
	}
}

// Flipping the switch must not freeze the expression the environment is
// supplying. Otherwise clearing the variable would stop changing anything, and
// an operator would have no way back short of editing the database.
func TestRegistry_SaveDoesNotPersistAnExpressionItWasNotGiven(t *testing.T) {
	r, sdb, _ := fixture(t)
	rec := &recorder{clock: &fakeClock{}}
	r.Register(daily(rec, false), strptr("0 5 * * *"))
	ctx := context.Background()

	enabled := true
	if _, err := r.Save(ctx, "test.daily", nil, &enabled, ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	var cron sql.NullString
	if err := sdb.QueryRow(`SELECT cron FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&cron); err != nil {
		t.Fatalf("read: %v", err)
	}
	if cron.Valid {
		t.Errorf("cron was persisted as %q by a request that only set the switch", cron.String)
	}

	// An explicit expression is persisted, as it should be.
	if _, err := r.Save(ctx, "test.daily", strptr("0 6 * * *"), nil, ""); err != nil {
		t.Fatalf("save cron: %v", err)
	}
	if err := sdb.QueryRow(`SELECT cron FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&cron); err != nil {
		t.Fatalf("read: %v", err)
	}
	if cron.String != "0 6 * * *" {
		t.Errorf("cron = %q, want the saved expression", cron.String)
	}
}

// The default is asked at resolve time, not captured at registration: whether
// a task makes sense can change under a running server.
func TestRegistry_DefaultEnabledIsAskedEachTime(t *testing.T) {
	r, _, _ := fixture(t)
	rec := &recorder{clock: &fakeClock{}}
	on := false
	task := daily(rec, false)
	task.DefaultEnabled = func() bool { return on }
	r.Register(task, nil)

	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].Enabled {
		t.Fatal("enabled before the condition held")
	}
	on = true
	list, err = r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !list[0].Enabled {
		t.Error("the default was captured at registration; it must be asked each time")
	}
}

// A task switched off between the loop's read and its write must not get one
// more run out of it. Sub-millisecond window, but a run of the compaction task
// is a frozen server and a restart.
func TestRegistry_DoesNotFireATaskDisabledUnderIt(t *testing.T) {
	r, sdb, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	// What Save writes when an admin turns it off.
	if _, err := sdb.Exec(
		`UPDATE scheduled_tasks SET configured = 1, enabled = 0 WHERE name = 'test.daily'`); err != nil {
		t.Fatalf("disable: %v", err)
	}

	claimed, err := r.claim(ctx, "test.daily", "0 0 * * *",
		clock.Now(), clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed {
		t.Error("the slot was claimed for a task that had just been switched off")
	}
	var status sql.NullString
	if err := sdb.QueryRow(
		`SELECT last_status FROM scheduled_tasks WHERE name = 'test.daily'`).Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status.String == "running" {
		t.Error("the row was marked running for a run that must not happen")
	}
}

// The loop sleeps until the earliest armed run rather than polling.
func TestRegistry_SleepsUntilTheNextRunRatherThanPolling(t *testing.T) {
	r, _, clock := fixture(t)
	rec := &recorder{clock: clock}
	r.Register(daily(rec, false), nil)
	ctx := context.Background()

	if err := r.considerOne(ctx, "test.daily"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	// Armed for midnight, twelve hours out: the wait is the cap, not a tick.
	if d := r.untilNext(ctx); d != maxSleep {
		t.Errorf("sleep = %s, want the %s cap", d, maxSleep)
	}

	// Five minutes before the slot, it waits exactly that long.
	clock.set(time.Date(2026, 8, 13, 23, 58, 0, 0, time.Local))
	if d := r.untilNext(ctx); d != 2*time.Minute {
		t.Errorf("sleep = %s, want 2m — the wait tracks the armed time", d)
	}
}

// Saving nudges the loop, so a new expression takes effect now rather than
// after the sleep that was armed for the old one.
func TestRegistry_SaveWakesTheLoop(t *testing.T) {
	r, _, _ := fixture(t)
	rec := &recorder{clock: &fakeClock{}}
	r.Register(daily(rec, false), nil)

	if _, err := r.Save(context.Background(), "test.daily", strptr("0 4 * * *"), nil, ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	select {
	case <-r.wake:
	default:
		t.Error("saving a schedule did not wake the loop; the change waits out the current sleep")
	}
}

// Who changed a schedule is read back, not merely stored.
func TestRegistry_ReportsWhoChangedTheSchedule(t *testing.T) {
	r, _, _ := fixture(t)
	rec := &recorder{clock: &fakeClock{}}
	r.Register(daily(rec, false), nil)

	got, err := r.Save(context.Background(), "test.daily", strptr("0 4 * * *"), nil, "admin@example.test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got.UpdatedBy != "admin@example.test" {
		t.Errorf("updated_by = %q, want the saving admin", got.UpdatedBy)
	}
}
