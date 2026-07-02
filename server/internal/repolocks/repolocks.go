// Package repolocks provides per-repository read/write locks shared between
// the worktree readers (the HTTP file/tree handlers and the indexer walk) and
// the clone worker (the single writer that rewrites a worktree via
// git reset --hard).
//
// The lock guarantees a reader never observes a worktree mid-rewrite: readers
// take the read-lock around their stat/read/walk, the clone worker takes the
// write-lock around the worktree mutation. Both run in the same process, so an
// in-process RWMutex is sufficient and correct — no flock / external infra.
// Every new consumer that reads a repo's checkout must take the read-lock for
// the same path_hash key, or it can race the writer.
package repolocks

import "sync"

// Locks is a concurrency-safe registry of per-repo RWMutexes keyed by the
// project path_hash. Use New; the zero value's sync.Map is usable but New
// documents intent.
type Locks struct {
	m sync.Map // map[string]*sync.RWMutex
}

// New returns an empty lock registry.
func New() *Locks { return &Locks{} }

// For returns the RWMutex for key, creating it on first use. Safe for
// concurrent callers — the first writer wins the LoadOrStore and every caller
// observes the same mutex for a given key.
func (l *Locks) For(key string) *sync.RWMutex {
	v, _ := l.m.LoadOrStore(key, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// WithWrite runs fn while holding key's write-lock, releasing it even if fn
// panics (the panic propagates after the unlock). Use this around a worktree
// mutation (git reset --hard); a plain Lock/Unlock pair would leak the lock
// forever if the mutating call panics and something up the stack recovers.
// Callers that need a value from fn assign it to a captured variable.
func (l *Locks) WithWrite(key string, fn func() error) error {
	mu := l.For(key)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// WithRead runs fn while holding key's read-lock, releasing it even if fn
// panics. Use this around any read of the repo's worktree (file/tree serving,
// the indexer walk) so it never observes a mid-rewrite state.
func (l *Locks) WithRead(key string, fn func() error) error {
	mu := l.For(key)
	mu.RLock()
	defer mu.RUnlock()
	return fn()
}
