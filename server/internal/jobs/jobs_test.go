package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func openSvc(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(d, Options{Concurrency: 1, PollEvery: 20 * time.Millisecond})
}

func TestEnqueueAndExecute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := openSvc(t)

	var ran atomic.Bool
	var receivedPayload atomic.Value
	svc.Register("test_job", func(_ context.Context, job Job) error {
		var p struct{ Name string }
		_ = UnmarshalPayload(job, &p)
		receivedPayload.Store(p.Name)
		ran.Store(true)
		return nil
	})

	if _, err := svc.Enqueue(ctx, EnqueueRequest{
		Type:    "test_job",
		Payload: map[string]string{"Name": "hello"},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	svc.Start(ctx)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	}()

	waitFor(t, time.Second, ran.Load)
	if got, _ := receivedPayload.Load().(string); got != "hello" {
		t.Fatalf("payload not delivered, got %q", got)
	}
}

func TestRetryOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := openSvc(t)
	// Shorten the linear backoff path by setting max_attempts > 1 but
	// keeping attempts small. The backoff is attempts × 10s; first retry
	// fires at +10s which is too slow for a unit test, so we'll just
	// check the row transitions to status=failed eventually rather than
	// waiting for retry.
	var attempts atomic.Int32
	svc.Register("flaky_job", func(_ context.Context, _ Job) error {
		attempts.Add(1)
		return errors.New("boom")
	})
	j, err := svc.Enqueue(ctx, EnqueueRequest{Type: "flaky_job", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	svc.Start(ctx)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	}()

	waitFor(t, time.Second, func() bool {
		got, err := svc.GetByID(ctx, j.ID)
		if err != nil {
			return false
		}
		return got.Status == StatusFailed
	})
	if a := attempts.Load(); a < 1 {
		t.Fatalf("expected >=1 attempts, got %d", a)
	}
}

func TestDedupeKey(t *testing.T) {
	ctx := context.Background()
	svc := openSvc(t)
	if _, err := svc.Enqueue(ctx, EnqueueRequest{
		Type: "x", DedupeKey: "k1", Payload: nil,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := svc.Enqueue(ctx, EnqueueRequest{
		Type: "x", DedupeKey: "k1", Payload: nil,
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestDedupeKeyAllowsAfterCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := openSvc(t)
	var done sync.WaitGroup
	done.Add(1)
	svc.Register("x", func(_ context.Context, _ Job) error {
		done.Done()
		return nil
	})
	if _, err := svc.Enqueue(ctx, EnqueueRequest{
		Type: "x", DedupeKey: "k", Payload: nil,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	svc.Start(ctx)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	}()
	done.Wait()
	// Tiny pause to allow markCompleted to settle.
	time.Sleep(100 * time.Millisecond)
	if _, err := svc.Enqueue(ctx, EnqueueRequest{
		Type: "x", DedupeKey: "k", Payload: nil,
	}); err != nil {
		t.Fatalf("second enqueue (after completion) should succeed, got %v", err)
	}
}

func TestUnregisteredTypeFailsLoudly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := openSvc(t)
	// Force max_attempts=1 so the job goes straight to failed without retry.
	j, err := svc.Enqueue(ctx, EnqueueRequest{Type: "missing", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	svc.Start(ctx)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	}()
	waitFor(t, time.Second, func() bool {
		got, err := svc.GetByID(ctx, j.ID)
		return err == nil && got.Status == StatusFailed
	})
}

func TestPanicRecovered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := openSvc(t)
	svc.Register("crash", func(_ context.Context, _ Job) error {
		panic("oops")
	})
	j, err := svc.Enqueue(ctx, EnqueueRequest{Type: "crash", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	svc.Start(ctx)
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	}()
	waitFor(t, time.Second, func() bool {
		got, err := svc.GetByID(ctx, j.ID)
		return err == nil && got.Status == StatusFailed
	})
}

func TestList(t *testing.T) {
	ctx := context.Background()
	svc := openSvc(t)
	for i := 0; i < 3; i++ {
		if _, err := svc.Enqueue(ctx, EnqueueRequest{Type: "x"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	all, err := svc.List(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(all))
	}
}

func waitFor(t *testing.T, max time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", max)
}
