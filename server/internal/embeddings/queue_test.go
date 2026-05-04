package embeddings

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueueAcquireRelease(t *testing.T) {
	q := NewQueue(1, time.Second)
	start := time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	q.Release(start)

	// Second acquire on the now-empty queue must succeed immediately.
	start = time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	q.Release(start)
}

func TestQueueTimeoutReturnsErrBusy(t *testing.T) {
	q := NewQueue(1, 30*time.Millisecond)

	// Hold the single slot so the second Acquire must wait.
	holdStart := time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("initial Acquire: %v", err)
	}
	defer q.Release(holdStart)

	err := q.Acquire(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var be *ErrBusy
	if !errors.As(err, &be) {
		t.Fatalf("expected *ErrBusy, got %T: %v", err, err)
	}
	if be.RetryAfter < minRetryAfterSec {
		t.Errorf("RetryAfter = %d, want >= %d", be.RetryAfter, minRetryAfterSec)
	}

	// IsBusy helper must also report the same hint.
	if ra, ok := IsBusy(err); !ok || ra != be.RetryAfter {
		t.Errorf("IsBusy(err) = (%d,%v), want (%d,true)", ra, ok, be.RetryAfter)
	}
}

func TestQueueContextCancelPropagated(t *testing.T) {
	// When the parent context is cancelled (not our timeout), the queue must
	// return the context error rather than pretending it was "busy". Handler
	// code distinguishes these two situations (cancel = no response, busy = 503).
	q := NewQueue(1, time.Second)
	holdStart := time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("initial Acquire: %v", err)
	}
	defer q.Release(holdStart)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := q.Acquire(ctx)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestQueueConcurrencyLimit(t *testing.T) {
	const slots = 3
	q := NewQueue(slots, time.Second)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	const workers = 10
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			start := time.Now()
			if err := q.Acquire(context.Background()); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			// Hold briefly to let contention build up.
			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
			q.Release(start)
		}()
	}
	wg.Wait()

	if peak > slots {
		t.Errorf("peak in-flight = %d, exceeds cap %d", peak, slots)
	}
	if peak < 2 {
		t.Errorf("peak in-flight = %d, expected some actual concurrency", peak)
	}
}

func TestQueueEMAConverges(t *testing.T) {
	// Feed three ~50ms batches and check the EMA drifts toward the observed
	// value rather than staying pinned at the 3s seed.
	q := NewQueue(1, time.Second)
	for i := 0; i < 3; i++ {
		start := time.Now()
		if err := q.Acquire(context.Background()); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		q.Release(start)
	}
	got := q.EstimatedWaitSec()
	if got >= avgBatchSecDefault {
		t.Errorf("EMA %.3f did not drift below seed %.1f", got, avgBatchSecDefault)
	}
	if got <= 0 {
		t.Errorf("EMA %.3f should be positive", got)
	}
}

func TestNewQueueClampsConcurrency(t *testing.T) {
	// A non-positive concurrency argument must be clamped to 1 — otherwise
	// the channel would have zero capacity and all Acquires would block.
	q := NewQueue(0, 10*time.Millisecond)
	if cap(q.slots) != 1 {
		t.Errorf("slots cap = %d, want 1", cap(q.slots))
	}
}

// TestQueueBlockNew_RejectsNewAcquires covers the PR-E sidecar restart drain
// path: BlockNew makes Acquire fail fast with ErrBusy so an admin's Save &
// Restart doesn't deadlock against a backlog of waiting search calls.
func TestQueueBlockNew_RejectsNewAcquires(t *testing.T) {
	q := NewQueue(2, time.Second)
	q.BlockNew()
	err := q.Acquire(context.Background())
	var busy *ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("Acquire after BlockNew = %v, want ErrBusy", err)
	}
	q.Resume()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after Resume = %v, want nil", err)
	}
	q.Release(time.Now())
}

// TestQueueWaitDrain_BlocksUntilInFlightZero verifies the drain primitive
// the PR-E Service.Restart relies on: WaitDrain returns once every Acquire
// has been released, regardless of how many slots the queue has.
func TestQueueWaitDrain_BlocksUntilInFlightZero(t *testing.T) {
	q := NewQueue(3, time.Second)
	startA, startB := time.Now(), time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	if got := q.InFlight(); got != 2 {
		t.Fatalf("InFlight = %d, want 2", got)
	}

	// WaitDrain must block while slots are held — release them on a
	// goroutine and ensure WaitDrain returns shortly after.
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- q.WaitDrain(ctx)
	}()

	time.Sleep(80 * time.Millisecond)
	q.Release(startA)
	time.Sleep(40 * time.Millisecond)
	q.Release(startB)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitDrain after both releases: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitDrain did not return after slots fully released")
	}
	if got := q.InFlight(); got != 0 {
		t.Errorf("InFlight after drain = %d, want 0", got)
	}
}

// TestQueueWaitDrain_RespectsContext ensures the drain timeout we use during
// Restart actually fires — without it a stuck embed call could wedge the
// admin's intentional restart forever.
func TestQueueWaitDrain_RespectsContext(t *testing.T) {
	q := NewQueue(1, time.Second)
	hold := time.Now()
	if err := q.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer q.Release(hold)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := q.WaitDrain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitDrain err = %v, want DeadlineExceeded", err)
	}
}

// Avoid unused-var lint while keeping the suppress simple.
var _ = sync.Mutex{}
