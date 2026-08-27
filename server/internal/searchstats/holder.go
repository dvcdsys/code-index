package searchstats

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Holder owns the store and recorder so the feature can be switched on and off
// while the server runs.
//
// The alternative was a boolean checked inside Record, with the database opened
// at boot regardless. That would mean a server with the feature switched off
// still creates and carries a database file, which is a surprising thing to
// find on disk after turning something off — and it would make "off" a
// property of the write path rather than of the feature. Here, off means the
// file is closed and, on a server that never enabled it, never created.
//
// Reads on the search path go through an atomic pointer rather than the mutex.
// Recording is the one thing here that must not queue behind an administrator
// clicking a toggle, and an RWMutex read lock — cheap as it is — is still a
// contended cache line on every search. The mutex serialises only the
// transitions, which are rare and slow.
type Holder struct {
	path   string
	logger *slog.Logger

	// recorder is what the search handlers read, and nil while disabled.
	// Nil-safe on every method, so a call that races a Disable is a no-op
	// rather than a panic.
	recorder atomic.Pointer[Recorder]
	// store is what the dashboard reads. Same nil contract.
	store atomic.Pointer[Store]

	mu sync.Mutex
	// ctx is the background context Enable starts flush loops under. Captured
	// once so a later Enable does not outlive the server's shutdown.
	ctx context.Context
}

// NewHolder returns a Holder for the database at path. Nothing is opened until
// Enable is called.
func NewHolder(ctx context.Context, path string, logger *slog.Logger) *Holder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Holder{path: path, logger: logger, ctx: ctx}
}

// Store returns the open store, or nil when the feature is off. Callers must
// nil-check: that is how the endpoints answer 503.
func (h *Holder) Store() *Store {
	if h == nil {
		return nil
	}
	return h.store.Load()
}

// Recorder returns the running recorder, or nil when the feature is off.
// Recorder's own methods are nil-safe, so the search path can call through
// without a branch of its own.
func (h *Holder) Recorder() *Recorder {
	if h == nil {
		return nil
	}
	return h.recorder.Load()
}

// Enabled reports whether counters are being recorded right now.
func (h *Holder) Enabled() bool { return h.Store() != nil }

// Path is where the database lives, whether or not it is currently open.
func (h *Holder) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

// Enable opens the database and starts the flush loop. Idempotent.
//
// The store is published BEFORE the recorder. For the moment between them the
// dashboard can read an empty table, which is true; the other order would let
// searches record into a recorder whose store the endpoints cannot yet see.
func (h *Holder) Enable() error {
	if h == nil {
		return fmt.Errorf("searchstats: no holder")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store.Load() != nil {
		return nil
	}

	store, err := Open(h.path)
	if err != nil {
		return err
	}
	rec := NewRecorder(store, h.logger)
	rec.Start(h.ctx)

	h.store.Store(store)
	h.recorder.Store(rec)
	h.logger.Info("search statistics enabled", "path", h.path)
	return nil
}

// Disable stops recording and closes the database. Idempotent.
//
// Whatever is buffered is FLUSHED, not discarded: Stop drains before returning.
// Switching the feature off is not a request to lose the last few seconds of
// what it already collected, and the counters that survive are what the
// dashboard will show if it is switched back on.
//
// The recorder is retired before the store, the reverse of Enable, so nothing
// can be recording into a store that is about to close.
func (h *Holder) Disable() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	store := h.store.Load()
	if store == nil {
		return nil
	}

	rec := h.recorder.Swap(nil)
	if rec != nil {
		rec.Stop()
	}
	h.store.Store(nil)

	if err := store.Close(); err != nil {
		return fmt.Errorf("searchstats: close on disable: %w", err)
	}
	h.logger.Info("search statistics disabled")
	return nil
}

// Set applies a desired state, returning whether anything changed.
func (h *Holder) Set(enabled bool) (changed bool, err error) {
	if h == nil {
		return false, fmt.Errorf("searchstats: no holder")
	}
	if enabled == h.Enabled() {
		return false, nil
	}
	if enabled {
		return true, h.Enable()
	}
	return true, h.Disable()
}

// Close shuts the feature down for good, draining anything buffered.
func (h *Holder) Close() error { return h.Disable() }
