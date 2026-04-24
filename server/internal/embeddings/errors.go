// Package embeddings implements the in-process embeddings service for cix-server.
// It supervises a sibling llama-server (llama.cpp) process and proxies
// embedding requests over a unix socket. This file defines the typed errors the
// public surface of the package returns so HTTP handlers (Phase 4) can map them
// to proper status codes.
package embeddings

import (
	"errors"
	"fmt"
)

// ErrBusy is returned by EmbedQuery/EmbedTexts when the concurrency queue is
// saturated and the caller's Acquire deadline fires. RetryAfter is the number
// of seconds the caller should wait before retrying — it is computed from the
// EMA of recent batch durations plus a safety floor. HTTP handlers should map
// it to 503 with a Retry-After header.
type ErrBusy struct {
	RetryAfter int
}

func (e *ErrBusy) Error() string {
	return fmt.Sprintf("embedding queue saturated, retry after %ds", e.RetryAfter)
}

// IsBusy reports whether err wraps an *ErrBusy and returns the retry hint.
// Kept as a helper because `errors.As` requires a typed variable at the call
// site; this is the idiomatic shortcut for handler code.
func IsBusy(err error) (int, bool) {
	var be *ErrBusy
	if errors.As(err, &be) {
		return be.RetryAfter, true
	}
	return 0, false
}

// ErrNotReady signals that the llama-server child is not yet accepting
// requests. The supervisor returns this during startup until the /health probe
// succeeds, and also after a crash while restart backoff is pending.
var ErrNotReady = errors.New("embeddings: llama-server not ready")

// ErrSupervisor signals a terminal supervisor failure — the llama-server child
// exited unexpectedly and exceeded the restart budget. Subsequent calls return
// this error until the process is restarted (by operator action).
var ErrSupervisor = errors.New("embeddings: supervisor dead, restart budget exhausted")

// ErrDisabled is returned when the service is constructed with embeddings
// disabled (cfg.EmbeddingsEnabled == false). Useful for tests that exercise
// the HTTP surface without spinning up llama-server.
var ErrDisabled = errors.New("embeddings: service disabled")
