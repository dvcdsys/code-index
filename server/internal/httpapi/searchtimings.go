package httpapi

import (
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Where a workspace query spent its time.
//
// This exists because the alternative is arithmetic. The dense scan, the BM25
// query and the fan-out's parallel speedup were each measured separately on the
// load-test fixture and multiplied together to guess at a 10.5 s query — a
// budget that happened to close, which is not the same as being right. Two
// optimisations were about to be built on that guess.
//
// The numbers are cheap: a handful of time.Now() calls per project and two
// atomics, against a query that reads gigabytes. So they are always COLLECTED.
// What they are not is always reported:
//
//   - the log line is emitted only for a query slower than slowWorkspaceQuery.
//     A breakdown printed for every query is noise nobody reads, and the
//     server already logs one http_request line per request with the wall
//     time in it, so the routine case is covered. A threshold keeps the
//     property that matters — nobody has to have switched anything on before
//     the slow query happened;
//   - the response object is attached only when the caller asks for it with
//     ?timings=true. In a response it is a debugging aid, not API surface.
//
// ---------------------------------------------------------------------------

// slowWorkspaceQuery is the line above which a query is worth a log entry of
// its own. Workspace search is a fan-out over every project in the workspace
// and is expected to take a while; on the load-test fixture (45 repos, 1.9M
// chunks) the median is ~10 s, and even a small workspace on a warm cache is
// hundreds of milliseconds. Two seconds is therefore not "slow" in the sense
// of "wrong" — it is the point past which the breakdown starts being worth
// storing, and it is low enough that a regression on a small workspace still
// trips it.
//
// A var rather than a const only so the test can exercise both sides of the
// threshold without sleeping for two seconds. Nothing at runtime writes it,
// and the one test that does restores it via t.Cleanup. That is safe only
// because nothing in this package calls t.Parallel(); if you add parallel
// tests here, move the threshold onto Deps first — under -race this global
// becomes a data race whose cause is not obvious from the failure.
var slowWorkspaceQuery = 2 * time.Second

// searchPhases accumulates one workspace query's timings.
//
// The fan-out phases keep a SUM and a MAX, and both are needed: the sum is how
// much work the query did, the max is how long the user waited for the slowest
// project. With perfect parallelism the wall time is the max; with none it is
// the sum. Measured on the fixture, eight concurrent project searches ran 3.4x
// faster than the same eight in sequence — so the truth is between the two
// numbers, and reporting only one of them hides which.
type searchPhases struct {
	embed    time.Duration
	resolve  time.Duration
	staleFTS time.Duration
	fanOut   time.Duration
	fuse     time.Duration

	// bm25 is a single duration, not a sum and a max, because there is a
	// single query: one workspace-wide FTS5 statement partitioned per
	// project. It ran per project once, and the sum/max split existed to
	// separate "work done" from "waited for" across those. With one query
	// they are the same number.
	bm25 time.Duration

	denseSum atomic.Int64 // nanoseconds
	denseMax atomic.Int64
}

// addDense records one project's dense-side latency. Includes the vector
// store's own hydration of the winning rows — the two are not separable from
// out here, and hydration is bounded by the result limit rather than by the
// collection size, so it is not what a scan-side change would move.
//
// A project whose query FAILED is recorded too. The time was spent, and
// leaving it out would put the sums permanently below the wall time they are
// meant to explain. It does mean a slow failure can own the max, which is why
// the fan-out logs its own warning per failed project.
func (p *searchPhases) addDense(d time.Duration) { addSumMax(&p.denseSum, &p.denseMax, d) }

func addSumMax(sum, max *atomic.Int64, d time.Duration) {
	n := d.Nanoseconds()
	sum.Add(n)
	for {
		cur := max.Load()
		if n <= cur || max.CompareAndSwap(cur, n) {
			return
		}
	}
}

// payload renders the timings for the response and for the log line.
//
// scanned vs returned is the ratio that decides whether routing the fan-out is
// worth building: the query does full dense and BM25 work on every project in
// the workspace and then thresholds the answer down. If those two numbers are
// far apart, most of the work was thrown away after it was paid for.
//
// `returned` must therefore be the count that survived the RELEVANCE
// THRESHOLD, not the count the caller was shown. The response panel is capped
// at top_projects (default 10), and feeding that number in here would peg the
// ratio at scanned:10 on any workspace with ten relevant repos or a hundred —
// a measurement of a request parameter rather than of wasted work. The panel
// count is reported separately, because "what did the caller get" is a
// different question from "what did the fan-out pay for".
func (p *searchPhases) payload(wall time.Duration, scanned, returned, panel int) map[string]any {
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	msn := func(n int64) int64 { return time.Duration(n).Milliseconds() }
	return map[string]any{
		"wall_ms":           ms(wall),
		"embed_ms":          ms(p.embed),
		"resolve_ms":        ms(p.resolve),
		"stale_fts_ms":      ms(p.staleFTS),
		"fanout_ms":         ms(p.fanOut),
		"dense_sum_ms":      msn(p.denseSum.Load()),
		"dense_max_ms":      msn(p.denseMax.Load()),
		"bm25_ms":           ms(p.bm25),
		"fuse_ms":           ms(p.fuse),
		"projects_scanned":  scanned,
		"projects_returned": returned,
		"projects_in_panel": panel,
	}
}
