package vectorstore

import (
	"context"
	"errors"
	"sync"
)

// Interface is the vector-store surface consumed by the indexer, the
// search handlers, and repojobs. Both *Store (direct) and *Holder
// (swappable) satisfy it, so a caller can hold either: tests pass a raw
// *Store, while production passes a *Holder so the active store can be
// reopened under a new directory on a provider switch without rewiring
// every holder. The method set mirrors *Store exactly.
type Interface interface {
	UpsertChunks(ctx context.Context, projectPath string, chunks []Chunk, embeddings [][]float32) error
	Search(ctx context.Context, projectPath string, queryEmbedding []float32, limit int, where map[string]string) ([]SearchResult, error)
	DeleteByFile(ctx context.Context, projectPath, filePath string) error
	DeleteCollection(projectPath string) error
	Count(projectPath string) int
}

// Compile-time assertions that both implementations satisfy Interface.
var (
	_ Interface = (*Store)(nil)
	_ Interface = (*Holder)(nil)
)

// Holder is a concurrency-safe, swappable wrapper around a *Store. It
// exists so the active vector store can be reopened under a new on-disk
// directory at runtime — e.g. when an admin switches the embedding
// provider (PUT /admin/embedding-providers/active), the new provider's
// vectors live in a different, dimension-isolated namespace, so the
// Service reopens a *Store at the new path and atomically Swap()s it in.
//
// All read/write proxies take the RLock; Swap takes the Lock. A search
// in flight during a Swap therefore either completes against the old
// store or starts against the new one — never observes a torn pointer.
// Every current holder of a raw *Store (indexer, httpapi Deps, repojobs)
// holds a *Holder instead and calls the identical method set.
//
// Swap does NOT close the store it replaces: the caller owns that decision
// (a *Store now holds an open SQLite pool, so production closes the old one
// — Store.Close waits for in-flight work — while a test that swaps back and
// forth between two stores keeps both alive).
type Holder struct {
	mu    sync.RWMutex
	store *Store
}

// errNotInitialised is returned by write proxies when the Holder has no
// store (only possible before the first Swap / NewHolder(nil)).
var errNotInitialised = errors.New("vectorstore: holder has no active store")

// NewHolder wraps an initial store (which may be nil; callers must Swap a
// real store in before writes succeed).
func NewHolder(s *Store) *Holder { return &Holder{store: s} }

// Swap installs newStore as the active store and returns the previous one
// (nil on first install). The caller may discard the returned store; no
// Close is required (see type doc).
func (h *Holder) Swap(newStore *Store) (old *Store) {
	h.mu.Lock()
	old = h.store
	h.store = newStore
	h.mu.Unlock()
	return old
}

// current returns the active store under the read lock.
func (h *Holder) current() *Store {
	h.mu.RLock()
	s := h.store
	h.mu.RUnlock()
	return s
}

// UpsertChunks proxies to the active store.
func (h *Holder) UpsertChunks(ctx context.Context, projectPath string, chunks []Chunk, embeddings [][]float32) error {
	s := h.current()
	if s == nil {
		return errNotInitialised
	}
	return s.UpsertChunks(ctx, projectPath, chunks, embeddings)
}

// Search proxies to the active store. A nil store yields (nil, nil),
// matching the empty-collection contract so callers degrade to no
// results rather than erroring.
func (h *Holder) Search(ctx context.Context, projectPath string, queryEmbedding []float32, limit int, where map[string]string) ([]SearchResult, error) {
	s := h.current()
	if s == nil {
		return nil, nil
	}
	return s.Search(ctx, projectPath, queryEmbedding, limit, where)
}

// DeleteByFile proxies to the active store.
func (h *Holder) DeleteByFile(ctx context.Context, projectPath, filePath string) error {
	s := h.current()
	if s == nil {
		return errNotInitialised
	}
	return s.DeleteByFile(ctx, projectPath, filePath)
}

// DeleteCollection proxies to the active store.
func (h *Holder) DeleteCollection(projectPath string) error {
	s := h.current()
	if s == nil {
		return errNotInitialised
	}
	return s.DeleteCollection(projectPath)
}

// Count proxies to the active store; a nil store reports 0.
func (h *Holder) Count(projectPath string) int {
	s := h.current()
	if s == nil {
		return 0
	}
	return s.Count(projectPath)
}

// Close closes the active store. A nil store is not an error. Used on
// shutdown; the Holder stays usable (every proxy then reports the store's own
// ErrClosed) rather than being left holding a dangling pointer.
func (h *Holder) Close() error {
	s := h.current()
	if s == nil {
		return nil
	}
	return s.Close()
}
