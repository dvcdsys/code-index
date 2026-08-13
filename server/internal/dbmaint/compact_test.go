package dbmaint

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// Two rebuilds must never overlap, and the check that prevents it has to hold
// across the *whole* preflight rather than just its first instruction.
//
// The preflight reads pragmas, counts jobs and writes an fsynced journal —
// tens of milliseconds, which is an eternity next to a double-clicked button
// or a scheduled run landing on a manual one. A gate released before the claim
// lets both callers through, and the loser then deletes the winner's
// half-written copy and thaws the write gate in the middle of its snapshot,
// which is precisely the data loss the freeze exists to prevent.
func TestStartRebuild_RefusesASecondCallerDuringTheSlowPreflight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 3)

	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	restarted := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32

	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: quietLogger(),
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() { once.Do(func() { close(restarted) }) },
		ActiveJobs: func(context.Context) (int, error) {
			// Hold the first caller inside the preflight. Later callers pass
			// straight through, so anything that reaches here has already got
			// past the gate — which is the failure being tested for.
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return 0, nil
		},
	})

	go func() { _, _ = svc.Compact(context.Background()) }()
	<-entered

	_, err = svc.Compact(context.Background())
	close(release)
	if !errors.Is(err, ErrCompactionRunning) {
		t.Fatalf("a second rebuild started while the first was still in its preflight: err = %v, want %v",
			err, ErrCompactionRunning)
	}

	select {
	case <-restarted:
	case <-time.After(30 * time.Second):
		t.Fatal("the first compaction never finished")
	}
}

// The whole runner, on the database shape every fresh install now has.
//
// This is the case that was broken and that no test caught: setting the
// reclaim mode needs a write transaction, so doing it after the freezer has
// taken BEGIN IMMEDIATE blocks for the full busy timeout and fails. It only
// showed on an *incremental* source — asking a mode-none database for mode
// none is short-circuited before any lock is needed, and a legacy database was
// what the end-to-end run happened to use. Every rebuild of a modern database
// stalled 30 seconds and gave up.
func TestRunCompaction_CompletesOnAnIncrementalDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 4)

	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()
	if got := modeOf(t, path); got != AutoVacuumIncremental {
		t.Fatalf("fixture is in %q mode, want %q", got, AutoVacuumIncremental)
	}

	done := make(chan struct{})
	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: quietLogger(),
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() { close(done) },
		ActiveJobs:     func(context.Context) (int, error) { return 0, nil },
	})

	started := time.Now()
	if _, err := svc.Compact(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the compaction never finished")
	}

	st, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("load journal: %v (found=%v)", err, ok)
	}
	if st.Phase != PhaseReadyToSwap {
		t.Fatalf("phase = %q after %s, want %q (error: %s)",
			st.Phase, time.Since(started).Round(time.Millisecond), PhaseReadyToSwap, st.Error)
	}

	p := PathsFor(path)
	if !exists(p.Copy) {
		t.Fatal("no compacted copy was produced")
	}
	if got := modeOf(t, p.Copy); got != AutoVacuumIncremental {
		t.Errorf("the copy is in %q mode; a compaction must leave the mode as it found it", got)
	}
}

// A refused preflight must give the claim back, or the first failure would
// wedge the feature until a restart.
func TestStartRebuild_ReleasesTheClaimWhenThePreflightRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 1)
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	busy := true
	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: quietLogger(),
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() {},
		ActiveJobs: func(context.Context) (int, error) {
			if busy {
				return 1, nil
			}
			return 0, nil
		},
	})

	if _, err := svc.Compact(context.Background()); !errors.Is(err, ErrJobsInFlight) {
		t.Fatalf("compact with a job in flight = %v, want %v", err, ErrJobsInFlight)
	}
	// The second attempt must fail for the same reason as the first, not
	// because the first left the flag set.
	busy = true
	if _, err := svc.Compact(context.Background()); errors.Is(err, ErrCompactionRunning) {
		t.Fatal("a refused preflight left the service thinking a rebuild was running")
	}
}

// The dashboard disables the Compact control on blocked_reason and shows it as
// the explanation. If it disagreed with the preflight, an admin would either
// click an enabled button and be refused, or be told they cannot do something
// the server would have accepted.
func TestBlockedReason_AgreesWithWhatARebuildWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 1)
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	jobs := 0
	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: quietLogger(),
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() {},
		ActiveJobs:     func(context.Context) (int, error) { return jobs, nil },
	})
	ctx := context.Background()
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if got := svc.BlockedReason(ctx, stats); got != "" {
		t.Errorf("blocked_reason on an idle server = %q, want empty", got)
	}

	jobs = 1
	got := svc.BlockedReason(ctx, stats)
	if got == "" {
		t.Fatal("blocked_reason is empty with a job in flight, but the request would be refused with 409")
	}
	if _, err := svc.Compact(ctx); !errors.Is(err, ErrJobsInFlight) {
		t.Fatalf("compact = %v, want %v — the reason and the refusal disagree", err, ErrJobsInFlight)
	}
}

// A server built without the restart hooks cannot rebuild anything, and must
// say so up front rather than offering a button that 500s.
func TestBlockedReason_SaysSoWhenCompactionIsNotWired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 1)
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	svc := New(Deps{DB: sdb, DBPath: path, Logger: quietLogger()})
	stats, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if svc.BlockedReason(context.Background(), stats) == "" {
		t.Error("a service with no restart hook reports nothing blocking a rebuild it cannot perform")
	}
}

// A CLI push holds no row in the jobs table — the three-phase protocol lives
// in the indexer's session map. A guard that counted only the table would
// freeze writes under a running index and, in full mode, restart the process
// mid-protocol.
func TestActiveWorkCounter_CountsIndexingSessionsWithNoJobRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 1)
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	sessions := 0
	count := ActiveWorkCounter(sdb, func() int { return sessions })

	n, err := count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("idle server reports %d in-flight units of work, want 0", n)
	}

	sessions = 1
	n, err = count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("an in-flight CLI index session counts as %d; the jobs table alone cannot see it", n)
	}
}
