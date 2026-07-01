// Package repolocks provides per-repository read/write locks shared between
// the HTTP file-read handlers (readers) and the clone/reindex worker (the
// single writer that rewrites a worktree via git reset --hard).
//
// The lock guarantees a file read never observes a worktree mid-rewrite:
// readers take the read-lock around stat+read, the clone worker takes the
// write-lock around the worktree mutation. Both run in the same process, so an
// in-process RWMutex is sufficient and correct — no flock / external infra.
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
