package dbmaint

import "sync/atomic"

// Gate is the authoritative write freeze.
//
// There are three layers holding writes off during a compaction, and this is
// the one that carries the load. The others are a stopped background queue and
// a write transaction held by the compactor; the transaction is the guarantee
// of correctness, but on its own it would be a disaster: a blocked write sits
// in SQLite's busy handler for the full busy_timeout — measured at 5.06 s —
// holding a connection out of a pool capped at eight. Eight of those and the
// pool is empty, and because WriteTimeout does not cancel request contexts,
// every subsequent *read* blocks indefinitely too. The feature promises that
// search keeps working, and that promise lives or dies here.
//
// So the gate refuses writes in front of the database, in microseconds, and
// the held transaction only ever catches what the gate missed.
type Gate struct {
	frozen atomic.Bool
}

// Freeze rejects writes. Idempotent.
func (g *Gate) Freeze() { g.frozen.Store(true) }

// Thaw resumes normal service. Idempotent.
func (g *Gate) Thaw() { g.frozen.Store(false) }

// Frozen reports whether writes are currently refused. Safe to call on a nil
// Gate, which reads as "not frozen" — routers wired without maintenance must
// behave exactly as they did before this feature existed.
func (g *Gate) Frozen() bool {
	if g == nil {
		return false
	}
	return g.frozen.Load()
}
