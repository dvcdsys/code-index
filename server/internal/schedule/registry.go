package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// tick is how often the clock is consulted. Cron resolves to the minute, so
// this only has to be fast enough that a run lands inside its own minute.
const tick = 30 * time.Second

// Handler is the work a task does. The context is cancelled on shutdown.
type Handler func(ctx context.Context) error

// Task is a recurring task as its owner declares it. Everything here is code,
// not configuration: what an admin can change lives in the database.
type Task struct {
	// Name is the stable identifier and the primary key of the row. It is a
	// wire value — the API addresses tasks by it — so it outlives renames of
	// whatever implements it.
	Name string
	// Title is what the dashboard shows.
	Title string
	// Description is one sentence on what running it does, including the cost.
	Description string

	// DefaultCron applies until an admin saves something.
	DefaultCron string
	// DefaultEnabled is asked every time the schedule is resolved, not once at
	// registration. Whether a task makes sense can change under a running
	// server — the database's reclaim is only meaningful in incremental mode,
	// and that mode is a switch on the same page — so a value captured at boot
	// would be stale until the next restart, in both directions.
	DefaultEnabled func() bool

	// CatchUp says what to do about a slot that passed while the process was
	// not running.
	//
	// Crontab itself never catches up — a machine asleep at 00:00 simply did
	// not run that day. That is the right default for anything expensive, and
	// the wrong one for a laptop that is asleep every night: a nightly reclaim
	// would then never run at all. So each task chooses, and the choice follows
	// the cost. Cheap and interruption-free: catch up whenever we notice.
	// Freezes the server: never, because "we noticed at 09:00" would mean a
	// read-only window in the middle of the working day.
	CatchUp bool

	Handler Handler
}

// State is a task and its schedule as it currently resolves, for the API.
type State struct {
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Cron        string      `json:"cron"`
	Enabled     bool        `json:"enabled"`
	Configured  bool        `json:"configured"`
	CatchUp     bool        `json:"catch_up"`
	NextRuns    []time.Time `json:"next_runs"`
	LastRunAt   *time.Time  `json:"last_run_at"`
	LastStatus  string      `json:"last_status,omitempty"`
	LastError   string      `json:"last_error,omitempty"`
	LastMillis  int64       `json:"last_millis,omitempty"`
	Running     bool        `json:"running"`
}

// Registry owns the clock for every recurring task in the server.
type Registry struct {
	db     *sql.DB
	logger *slog.Logger
	// now is injectable so the loop can be driven through a year in a test
	// without waiting for one.
	now func() time.Time

	mu      sync.Mutex
	tasks   map[string]*entry
	order   []string
	running map[string]bool

	// handlers counts task goroutines in flight. Stop joins them: a caller
	// that asks background writers to stop is entitled to assume they have,
	// and the compactor's quiesce is exactly that caller.
	handlers sync.WaitGroup

	stopped chan struct{}
	once    sync.Once
}

type entry struct {
	task    Task
	envCron *string
}

// New builds a registry over sdb. A nil logger becomes the default.
func New(sdb *sql.DB, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		db:      sdb,
		logger:  logger,
		now:     time.Now,
		tasks:   map[string]*entry{},
		running: map[string]bool{},
		stopped: make(chan struct{}),
	}
}

// Register adds a task. envCron, when non-nil, is an operator-supplied default
// from the environment — it beats the built-in default and loses to whatever an
// admin saves in the dashboard.
//
// Registering the same name twice is a programming error and panics: two owners
// for one schedule is not a state worth resolving at runtime.
func (r *Registry) Register(t Task, envCron *string) {
	if t.Name == "" || t.Handler == nil {
		panic("schedule: a task needs a name and a handler")
	}
	if err := Validate(t.DefaultCron); err != nil {
		panic(fmt.Sprintf("schedule: task %q has an unusable default cron: %v", t.Name, err))
	}
	// Validated once, here, rather than on every resolve: parsing plus the
	// probe loop is not free, and an operator's typo should be reported at
	// startup rather than silently dropping them onto the built-in default
	// every time the dashboard asks.
	if envCron != nil {
		if err := Validate(*envCron); err != nil {
			r.logger.Error("the environment supplies an unusable schedule for this task; falling back to its default",
				"task", t.Name, "cron", *envCron, "err", err)
			envCron = nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tasks[t.Name]; dup {
		panic("schedule: duplicate task " + t.Name)
	}
	r.tasks[t.Name] = &entry{task: t, envCron: envCron}
	r.order = append(r.order, t.Name)
}

// Run drives the registry until ctx is cancelled.
func (r *Registry) Run(ctx context.Context) {
	defer r.once.Do(func() { close(r.stopped) })

	// A run marked in flight by a process that is no longer here did not
	// finish, it was interrupted. Nothing else will ever correct it — the
	// status is written before the handler and cleared after — so a host that
	// powers off nightly would show a permanent phantom "running" on the
	// dashboard until the next slot came round.
	if err := r.clearStaleRuns(ctx); err != nil {
		r.logger.Warn("could not clear interrupted scheduled runs", "err", err)
	}

	// A pass on start, so a task whose slot passed while the process was down
	// is dealt with — caught up or stepped over — rather than waiting out a
	// whole tick first.
	r.runDue(ctx)

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.runDue(ctx)
		}
	}
}

// Stop waits for the loop to exit *and* for every handler it started to
// return. Callers that need to know background writers have actually stopped —
// the database compactor does — use this rather than merely cancelling.
//
// Joining the handlers is the whole point. Waiting only for the loop would
// have honoured the letter of the contract and none of its meaning: a reclaim
// in the middle of `incremental_vacuum` would sail straight through the
// compaction's quiesce and go on writing into the database being copied. The
// handlers hold no HTTP connection, so the route gate cannot see them either.
func (r *Registry) Stop(ctx context.Context) error {
	select {
	case <-r.stopped:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Safe to Wait now: handlers are only started from the loop, and the loop
	// has returned.
	drained := make(chan struct{})
	go func() {
		r.handlers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("a scheduled task was still running: %w", ctx.Err())
	}
}

func (r *Registry) runDue(ctx context.Context) {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	r.mu.Unlock()

	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		if err := r.considerOne(ctx, name); err != nil {
			r.logger.Warn("scheduled task could not be evaluated", "task", name, "err", err)
		}
	}
}

// considerOne decides whether a single task is due and, if so, runs it.
func (r *Registry) considerOne(ctx context.Context, name string) error {
	r.mu.Lock()
	e := r.tasks[name]
	busy := r.running[name]
	r.mu.Unlock()
	if e == nil {
		return nil
	}
	if busy {
		// Still inside its previous run. Skipping is the whole overlap policy:
		// a task slower than its own schedule falls behind rather than piling
		// up copies of itself.
		return nil
	}

	row, err := r.load(ctx, name)
	if err != nil {
		return err
	}
	cron, enabled, _ := r.resolve(e, row)
	if !enabled {
		return nil
	}

	now := r.now()
	next := row.nextRunAt
	if next == nil || row.cronUsed != cron {
		// No schedule yet, or the expression changed under it. Recompute from
		// the clock — never from when anything last finished.
		n, err := NextAfter(cron, now)
		if err != nil {
			return err
		}
		return r.arm(ctx, name, cron, n)
	}
	if now.Before(*next) {
		return nil
	}

	// The slot has arrived, or passed while nobody was watching.
	late := now.Sub(*next)
	if !e.task.CatchUp && late > tick*2 {
		// Crontab semantics: a slot missed is a slot lost. Step over it rather
		// than firing hours later, which for anything that freezes the server
		// would be a read-only window nobody asked for.
		n, err := NextAfter(cron, now)
		if err != nil {
			return err
		}
		r.logger.Info("scheduled task skipped a slot it was not running for",
			"task", name, "missed", next.Format(time.RFC3339), "next", n.Format(time.RFC3339))
		return r.arm(ctx, name, cron, n)
	}

	// Claim the slot durably BEFORE running.
	//
	// Not bookkeeping — correctness. One of these tasks re-executes the whole
	// process as its final step, so a slot still marked due when the new
	// process starts would fire it again, and again. The write has to be on
	// disk before the handler is entered.
	upcoming, err := NextAfter(cron, now)
	if err != nil {
		return err
	}
	if err := r.claim(ctx, name, cron, now, upcoming); err != nil {
		return err
	}

	r.mu.Lock()
	r.running[name] = true
	r.mu.Unlock()
	r.handlers.Add(1)
	go func() {
		defer r.handlers.Done()
		r.invoke(ctx, name, e.task)
	}()
	return nil
}

// invoke runs the handler and records what happened.
func (r *Registry) invoke(ctx context.Context, name string, t Task) {
	started := r.now()
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		err = t.Handler(ctx)
	}()
	elapsed := r.now().Sub(started)

	r.mu.Lock()
	delete(r.running, name)
	r.mu.Unlock()

	status, msg := "ok", ""
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		// Interrupted by shutdown. Not a failure — it says nothing about the
		// task — but it must not be left as "running" either, or the dashboard
		// reports a run that nothing will ever finish.
		r.logger.Info("scheduled task stopped early", "task", name, "reason", "shutting down")
		status, msg = "interrupted", "stopped while the server was shutting down"
	default:
		status, msg = "failed", err.Error()
		r.logger.Warn("scheduled task failed", "task", name, "err", err)
	}
	if rerr := r.record(context.WithoutCancel(ctx), name, status, msg, elapsed); rerr != nil {
		r.logger.Warn("could not record a scheduled task outcome", "task", name, "err", rerr)
	}
}

// List reports every registered task as it currently resolves.
func (r *Registry) List(ctx context.Context) ([]State, error) {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	entries := make(map[string]*entry, len(r.tasks))
	busy := make(map[string]bool, len(r.running))
	maps.Copy(entries, r.tasks)
	maps.Copy(busy, r.running)
	r.mu.Unlock()

	out := make([]State, 0, len(names))
	for _, name := range names {
		e := entries[name]
		row, err := r.load(ctx, name)
		if err != nil {
			return nil, err
		}
		cron, enabled, configured := r.resolve(e, row)
		st := State{
			Name:        e.task.Name,
			Title:       e.task.Title,
			Description: e.task.Description,
			Cron:        cron,
			Enabled:     enabled,
			Configured:  configured,
			CatchUp:     e.task.CatchUp,
			LastRunAt:   row.lastRunAt,
			LastStatus:  row.lastStatus,
			LastError:   row.lastError,
			LastMillis:  row.lastMillis,
			Running:     busy[name],
		}
		if enabled {
			if runs, err := NextRuns(cron, r.now(), 3); err == nil {
				st.NextRuns = runs
			}
		}
		if st.NextRuns == nil {
			st.NextRuns = []time.Time{}
		}
		out = append(out, st)
	}
	return out, nil
}

// Save applies an admin's change to one task.
func (r *Registry) Save(ctx context.Context, name string, cron *string, enabled *bool, by string) (State, error) {
	r.mu.Lock()
	e := r.tasks[name]
	r.mu.Unlock()
	if e == nil {
		return State{}, fmt.Errorf("%w: %q", ErrUnknownTask, name)
	}

	row, err := r.load(ctx, name)
	if err != nil {
		return State{}, err
	}
	nextCron, nextEnabled, _ := r.resolve(e, row)
	if cron != nil {
		nextCron = *cron
	}
	if enabled != nil {
		nextEnabled = *enabled
	}
	if err := Validate(nextCron); err != nil {
		return State{}, fmt.Errorf("%w: %s", ErrInvalidCron, err)
	}
	// Persist the expression only when this request supplied one. Otherwise a
	// PATCH that merely flips the switch would freeze whatever the expression
	// currently resolves to — including one that came from the environment —
	// into the row, and clearing the variable would no longer change anything.
	var storeCron *string
	if cron != nil {
		storeCron = &nextCron
	}

	// Re-arm from the new expression immediately, so the preview the admin is
	// looking at and the time the server will actually fire are the same thing.
	upcoming, err := NextAfter(nextCron, r.now())
	if err != nil {
		return State{}, fmt.Errorf("%w: %s", ErrInvalidCron, err)
	}
	if err := r.upsert(ctx, name, storeCron, nextCron, nextEnabled, &upcoming, by); err != nil {
		return State{}, err
	}

	all, err := r.List(ctx)
	if err != nil {
		return State{}, err
	}
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
	}
	return State{}, fmt.Errorf("%w: %q", ErrUnknownTask, name)
}

// resolve applies the precedence: a saved row beats the environment, which
// beats what the task was registered with.
func (r *Registry) resolve(e *entry, row taskRow) (cron string, enabled, configured bool) {
	cron = e.task.DefaultCron
	if e.task.DefaultEnabled != nil {
		enabled = e.task.DefaultEnabled()
	}
	if e.envCron != nil {
		cron = *e.envCron
	}
	// A row exists as soon as the scheduler arms the task, which is not a
	// decision anybody made. Only a row an admin actually saved overrides.
	if row.found && row.configured {
		configured = true
		if row.cron != "" {
			cron = row.cron
		}
		enabled = row.enabled
	}
	return cron, enabled, configured
}

var (
	// ErrUnknownTask means no task is registered under that name.
	ErrUnknownTask = errors.New("unknown scheduled task")
	// ErrInvalidCron means the expression cannot be honoured as written.
	ErrInvalidCron = errors.New("invalid schedule")
)
