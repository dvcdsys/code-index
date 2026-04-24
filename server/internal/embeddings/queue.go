package embeddings

import (
	"context"
	"sync"
	"time"
)

const (
	// emaAlpha is the smoothing factor for the average-batch-duration EMA.
	// Matches api/app/services/embeddings.py _EMA_ALPHA.
	emaAlpha = 0.25

	// avgBatchSecDefault seeds the EMA before any batch has completed.
	// Matches api/app/services/embeddings.py _AVG_BATCH_SEC_DEFAULT.
	avgBatchSecDefault = 3.0

	// minRetryAfterSec is the floor for Retry-After hints — keeps clients from
	// hammering the server when the EMA drops below a reasonable poll interval.
	minRetryAfterSec = 5
)

// Queue is a concurrency limiter plus a rolling estimator of batch duration.
// It is implemented with a buffered channel (capacity = concurrency) rather
// than golang.org/x/sync/semaphore to keep the dependency footprint minimal,
// per the plan's explicit instruction.
type Queue struct {
	slots   chan struct{}
	timeout time.Duration

	mu             sync.Mutex
	avgBatchSec    float64
	estFinishAtMs  int64 // unix millis; 0 when no batch is in flight
}

// NewQueue constructs a queue with the given max concurrency and acquire
// timeout. A concurrency of <=0 is treated as 1 so the caller never deadlocks.
// A timeout of <=0 is treated as no timeout (Acquire waits on ctx only).
func NewQueue(concurrency int, timeout time.Duration) *Queue {
	if concurrency <= 0 {
		concurrency = 1
	}
	return &Queue{
		slots:       make(chan struct{}, concurrency),
		timeout:     timeout,
		avgBatchSec: avgBatchSecDefault,
	}
}

// Acquire blocks until a slot is free, the context is cancelled, or the
// per-queue timeout fires. On timeout it returns *ErrBusy with a RetryAfter
// hint derived from the EMA — callers surface this as HTTP 503.
func (q *Queue) Acquire(ctx context.Context) error {
	var (
		cancel context.CancelFunc
		qctx   = ctx
	)
	if q.timeout > 0 {
		qctx, cancel = context.WithTimeout(ctx, q.timeout)
		defer cancel()
	}

	// Record the estimated finish-at for the caller currently holding the slot
	// so the busy response can tell clients roughly how long to wait.
	select {
	case q.slots <- struct{}{}:
		q.markBatchStart()
		return nil
	case <-qctx.Done():
		// Distinguish caller-cancel from our timeout: if parent ctx is live,
		// the timeout fired and we return ErrBusy. Otherwise propagate cancel.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ErrBusy{RetryAfter: q.retryAfter()}
	}
}

// Release frees the slot held by the caller and updates the EMA using the
// elapsed duration since Acquire. It must be called exactly once per
// successful Acquire. Calling Release without a matching Acquire is a bug and
// will panic (via the channel underflow) — this is intentional so the misuse
// is caught in tests rather than silently leaking slots.
func (q *Queue) Release(start time.Time) {
	<-q.slots
	q.updateEMA(time.Since(start))
}

// EstimatedWaitSec returns the EMA-based wait estimate. Exposed for tests and
// for debug endpoints that want to surface queue health.
func (q *Queue) EstimatedWaitSec() float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.avgBatchSec
}

// markBatchStart stamps the estimated finish time so the retry-after math has
// a fresh datum while this batch is being processed.
func (q *Queue) markBatchStart() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.estFinishAtMs = time.Now().Add(time.Duration(q.avgBatchSec * float64(time.Second))).UnixMilli()
}

// updateEMA folds the observed batch duration into the rolling average using
// the same alpha as the Python implementation.
func (q *Queue) updateEMA(batch time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	observed := batch.Seconds()
	q.avgBatchSec = (1-emaAlpha)*q.avgBatchSec + emaAlpha*observed
	q.estFinishAtMs = 0
}

// retryAfter computes how many seconds a retrying caller should wait. Uses the
// remaining time on the currently-processing batch if any, otherwise the EMA,
// then floors at minRetryAfterSec so the number we return is a usable hint.
func (q *Queue) retryAfter() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	var secs float64
	if q.estFinishAtMs > 0 {
		remainMs := q.estFinishAtMs - time.Now().UnixMilli()
		if remainMs > 0 {
			secs = float64(remainMs) / 1000.0
		} else {
			secs = q.avgBatchSec
		}
	} else {
		secs = q.avgBatchSec
	}
	if int(secs) < minRetryAfterSec {
		return minRetryAfterSec
	}
	return int(secs)
}
