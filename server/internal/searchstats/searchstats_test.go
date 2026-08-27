package searchstats

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedNow is an arbitrary instant that does NOT fall on a bucket boundary, so
// every test exercises the flooring rather than accidentally agreeing with it.
var fixedNow = time.Date(2026, 8, 27, 14, 43, 17, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	// A file rather than :memory: — the schema is WITHOUT ROWID with foreign
	// keys, and the pool behaviour differs between the two. Testing the shape
	// production actually runs is worth one temp directory.
	s, err := Open(filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// recorderAt builds a recorder whose clock is fixed, so bucket assignment is
// deterministic instead of depending on when the test ran.
func recorderAt(t *testing.T, s *Store, at time.Time) *Recorder {
	t.Helper()
	r := NewRecorder(s, nil)
	r.now = func() time.Time { return at }
	return r
}

func TestBucketOfFloorsToBucketSeconds(t *testing.T) {
	got := BucketOf(fixedNow)
	if got%BucketSeconds != 0 {
		t.Fatalf("BucketOf(%v) = %d, which is not a multiple of %d", fixedNow, got, BucketSeconds)
	}
	if want := time.Date(2026, 8, 27, 14, 30, 0, 0, time.UTC).Unix(); got != want {
		t.Fatalf("BucketOf = %d, want %d (14:30)", got, want)
	}
}

func TestRecordAndFlushPopulatesBothTiers(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("proj-a", KindSemantic, []string{"a.go", "b.go"})
	r.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Totals tier.
	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, TopFiles: 5, Sort: SortQueries,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(page.Rows))
	}
	row := page.Rows[0]
	if row.Queries != 2 {
		t.Errorf("queries = %d, want 2", row.Queries)
	}
	if row.Results != 3 {
		t.Errorf("results = %d, want 3 (2 files + 1 file)", row.Results)
	}
	if row.FileHits != 3 {
		t.Errorf("file_hits = %d, want 3", row.FileHits)
	}
	if row.DistinctFiles != 2 {
		t.Errorf("distinct_files = %d, want 2", row.DistinctFiles)
	}
	if row.TopFileHits != 2 {
		t.Errorf("top_file_hits = %d, want 2 (a.go seen twice)", row.TopFileHits)
	}
	if len(row.TopFiles) != 2 || row.TopFiles[0].FilePath != "a.go" || row.TopFiles[0].Hits != 2 {
		t.Errorf("top_files = %+v, want a.go first with 2 hits", row.TopFiles)
	}

	// Window tier must agree with the totals while everything is inside it.
	windowed, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, TopFiles: 5, Window: WindowRetention,
	}, fixedNow)
	if err != nil {
		t.Fatalf("windowed ProjectStatsPage: %v", err)
	}
	if len(windowed.Rows) != 1 || windowed.Rows[0].Queries != 2 {
		t.Fatalf("windowed rows = %+v, want one row with 2 queries", windowed.Rows)
	}
	if windowed.Rows[0].TopFileHits != 2 {
		t.Errorf("windowed top_file_hits = %d, want 2", windowed.Rows[0].TopFileHits)
	}
}

// The windowed tier must report the busiest FILE, not the busiest bucket of
// the busiest file. Recording the same file across two buckets is the case
// that tells the two apart.
func TestTopFileHitsSumsAcrossBuckets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	earlier := fixedNow.Add(-2 * time.Hour)
	r1 := recorderAt(t, s, earlier)
	r1.Record("proj-a", KindSemantic, []string{"hot.go"})
	r1.Record("proj-a", KindSemantic, []string{"hot.go"})
	if err := r1.Flush(ctx); err != nil {
		t.Fatalf("flush 1: %v", err)
	}

	r2 := recorderAt(t, s, fixedNow)
	r2.Record("proj-a", KindSemantic, []string{"hot.go"})
	r2.Record("proj-a", KindSemantic, []string{"cold.go"})
	r2.Record("proj-a", KindSemantic, []string{"cold.go"})
	if err := r2.Flush(ctx); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, TopFiles: 5, Window: 24 * time.Hour,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	row := page.Rows[0]
	// hot.go: 3 hits across two buckets. cold.go: 2 hits in one bucket.
	// Reading MAX(hits) straight off the bucket table would say 2.
	if row.TopFileHits != 3 {
		t.Errorf("top_file_hits = %d, want 3 — hot.go's hits must sum across buckets", row.TopFileHits)
	}
	if row.TopFiles[0].FilePath != "hot.go" || row.TopFiles[0].Hits != 3 {
		t.Errorf("top file = %+v, want hot.go with 3", row.TopFiles[0])
	}
}

func TestAccessScopeIsTheProjectList(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("visible", KindSemantic, []string{"a.go"})
	r.Record("hidden", KindSemantic, []string{"secret.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"visible"}, TopFiles: 5,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ProjectPath != "visible" {
		t.Fatalf("rows = %+v, want only the visible project", page.Rows)
	}

	// The empty scope is the safe default, not "everything".
	empty, err := s.ProjectStatsPage(ctx, Query{TopFiles: 5}, fixedNow)
	if err != nil {
		t.Fatalf("empty-scope ProjectStatsPage: %v", err)
	}
	if len(empty.Rows) != 0 || empty.Total != 0 {
		t.Fatalf("empty scope returned %d rows / total %d, want nothing", len(empty.Rows), empty.Total)
	}
}

func TestRangeFiltersAndSort(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	// busy: 5 queries. quiet: 1 query.
	for i := 0; i < 5; i++ {
		r.Record("busy", KindSemantic, []string{"x.go"})
	}
	r.Record("quiet", KindSemantic, []string{"y.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	scope := []string{"busy", "quiet"}

	min := int64(2)
	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, MinQueries: &min, TopFiles: 1,
	}, fixedNow)
	if err != nil {
		t.Fatalf("min filter: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ProjectPath != "busy" {
		t.Fatalf("min_queries=2 gave %+v, want only busy", page.Rows)
	}
	if page.Total != 1 {
		t.Errorf("total = %d, want 1 — the count must share the filters", page.Total)
	}

	max := int64(2)
	page, err = s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, MaxQueries: &max, TopFiles: 1,
	}, fixedNow)
	if err != nil {
		t.Fatalf("max filter: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ProjectPath != "quiet" {
		t.Fatalf("max_queries=2 gave %+v, want only quiet", page.Rows)
	}

	// Sort descending by queries.
	page, err = s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, Sort: SortQueries, Desc: true, TopFiles: 1,
	}, fixedNow)
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0].ProjectPath != "busy" {
		t.Fatalf("sort desc gave %+v, want busy first", page.Rows)
	}

	// And ascending by project name.
	page, err = s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, Sort: SortProject, TopFiles: 1,
	}, fixedNow)
	if err != nil {
		t.Fatalf("sort by project: %v", err)
	}
	if page.Rows[0].ProjectPath != "busy" || page.Rows[1].ProjectPath != "quiet" {
		t.Fatalf("sort by project gave %+v, want alphabetical", page.Rows)
	}
}

func TestPaginationIsStableAcrossPages(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	// Every project has the SAME counter, so only the tie-break keeps the
	// ordering stable — which is the property under test.
	scope := []string{"p1", "p2", "p3", "p4"}
	for _, p := range scope {
		r.Record(p, KindSemantic, []string{"same.go"})
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var seen []string
	for offset := 0; offset < 4; offset += 2 {
		page, err := s.ProjectStatsPage(ctx, Query{
			ProjectPaths: scope, Sort: SortQueries, Desc: true, Limit: 2, Offset: offset,
		}, fixedNow)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if page.Total != 4 {
			t.Errorf("total at offset %d = %d, want 4", offset, page.Total)
		}
		for _, row := range page.Rows {
			seen = append(seen, row.ProjectPath)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("paged through %d rows, want 4: %v", len(seen), seen)
	}
	for i, want := range scope {
		if seen[i] != want {
			t.Fatalf("paged order = %v, want %v — the tie-break is not holding", seen, scope)
		}
	}
}

func TestPruneDropsWindowRowsButKeepsTotals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := recorderAt(t, s, fixedNow.Add(-WindowRetention-time.Hour))
	old.Record("proj-a", KindSemantic, []string{"ancient.go"})
	if err := old.Flush(ctx); err != nil {
		t.Fatalf("flush old: %v", err)
	}

	recent := recorderAt(t, s, fixedNow)
	recent.Record("proj-a", KindSemantic, []string{"fresh.go"})
	if err := recent.Flush(ctx); err != nil {
		t.Fatalf("flush recent: %v", err)
	}

	removed, err := s.Prune(ctx, fixedNow)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed == 0 {
		t.Fatal("Prune removed nothing, want the out-of-window rows gone")
	}

	// Totals keep BOTH queries — that is the whole reason they are a separate
	// tier. If this ever reads 1, the tiers have been collapsed.
	totals, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, TopFiles: 5,
	}, fixedNow)
	if err != nil {
		t.Fatalf("totals after prune: %v", err)
	}
	if totals.Rows[0].Queries != 2 {
		t.Errorf("totals queries after prune = %d, want 2 — pruning must not touch the totals tier",
			totals.Rows[0].Queries)
	}
	if totals.Rows[0].DistinctFiles != 2 {
		t.Errorf("totals distinct_files after prune = %d, want 2", totals.Rows[0].DistinctFiles)
	}

	// The window keeps only the fresh one.
	windowed, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, TopFiles: 5, Window: WindowRetention,
	}, fixedNow)
	if err != nil {
		t.Fatalf("window after prune: %v", err)
	}
	if windowed.Rows[0].Queries != 1 {
		t.Errorf("windowed queries after prune = %d, want 1", windowed.Rows[0].Queries)
	}
	if len(windowed.Rows[0].TopFiles) != 1 || windowed.Rows[0].TopFiles[0].FilePath != "fresh.go" {
		t.Errorf("windowed top files = %+v, want only fresh.go", windowed.Rows[0].TopFiles)
	}
}

// A project whose activity is entirely outside the window must be absent from
// a windowed page, not present with zeroes.
func TestWindowExcludesProjectsWithNoRecentActivity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := recorderAt(t, s, fixedNow.Add(-48*time.Hour))
	old.Record("stale", KindSemantic, []string{"a.go"})
	if err := old.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"stale"}, Window: time.Hour, TopFiles: 5,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("rows = %+v, want none — the project had no activity in the window", page.Rows)
	}
}

func TestKindFilterSeparatesCounters(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("proj-a", KindSemantic, []string{"sem.go"})
	r.Record("proj-a", KindSymbols, []string{"sym.go"})
	r.Record("proj-a", KindSymbols, []string{"sym.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	all, err := s.ProjectStatsPage(ctx, Query{ProjectPaths: []string{"proj-a"}, TopFiles: 5}, fixedNow)
	if err != nil {
		t.Fatalf("all kinds: %v", err)
	}
	if all.Rows[0].Queries != 3 {
		t.Errorf("all-kind queries = %d, want 3", all.Rows[0].Queries)
	}

	sem, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-a"}, Kinds: []string{KindSemantic}, TopFiles: 5,
	}, fixedNow)
	if err != nil {
		t.Fatalf("semantic only: %v", err)
	}
	if sem.Rows[0].Queries != 1 {
		t.Errorf("semantic queries = %d, want 1", sem.Rows[0].Queries)
	}
	if len(sem.Rows[0].TopFiles) != 1 || sem.Rows[0].TopFiles[0].FilePath != "sem.go" {
		t.Errorf("semantic top files = %+v, want only sem.go", sem.Rows[0].TopFiles)
	}
}

func TestForgetRemovesEverythingForOneProject(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("gone", KindSemantic, []string{"a.go"})
	r.Record("stays", KindSemantic, []string{"b.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if err := s.Forget(ctx, "gone"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	// Unknown projects are not an error — a project nobody searched has no row.
	if err := s.Forget(ctx, "never-searched"); err != nil {
		t.Fatalf("Forget on an unknown project: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"gone", "stays"}, TopFiles: 5,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ProjectPath != "stays" {
		t.Fatalf("rows = %+v, want only stays", page.Rows)
	}

	// The cascade has to have reached the child tables, not just the parent.
	for _, table := range []string{
		"search_totals", "search_file_totals", "search_buckets", "search_file_buckets",
	} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s holds %d rows after Forget, want 1 (only stays)", table, n)
		}
	}
}

func TestSeriesReportsPerBucketCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := fixedNow.Add(-time.Hour)
	r1 := recorderAt(t, s, first)
	r1.Record("proj-a", KindSemantic, []string{"a.go"})
	r1.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r1.Flush(ctx); err != nil {
		t.Fatalf("flush 1: %v", err)
	}
	r2 := recorderAt(t, s, fixedNow)
	r2.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r2.Flush(ctx); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	points, err := s.Series(ctx, Query{
		ProjectPaths: []string{"proj-a"}, Window: 24 * time.Hour,
	}, fixedNow)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %+v, want 2 buckets", points)
	}
	if points[0].Bucket != BucketOf(first) || points[0].Queries != 2 {
		t.Errorf("first point = %+v, want bucket %d with 2 queries", points[0], BucketOf(first))
	}
	if points[1].Bucket != BucketOf(fixedNow) || points[1].Queries != 1 {
		t.Errorf("second point = %+v, want bucket %d with 1 query", points[1], BucketOf(fixedNow))
	}
}

// A flush that straddles a bucket boundary must split its counters, not dump
// them all into whichever bucket the flush happened to run in.
func TestRecordsAreAttributedToTheirOwnBucket(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := NewRecorder(s, nil)
	clock := fixedNow
	r.now = func() time.Time { return clock }

	r.Record("proj-a", KindSemantic, []string{"a.go"})
	clock = fixedNow.Add(BucketSeconds * time.Second) // next bucket
	r.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	points, err := s.Series(ctx, Query{
		ProjectPaths: []string{"proj-a"}, Window: 24 * time.Hour,
	}, clock)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %+v, want the two records in two different buckets", points)
	}
}

func TestResetEmptiesEverything(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	page, err := s.ProjectStatsPage(ctx, Query{ProjectPaths: []string{"proj-a"}}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("rows after Reset = %+v, want none", page.Rows)
	}
}

// Record must be safe and free on a nil recorder — that is what lets the
// search handlers call it without guarding a feature that can be off.
func TestNilRecorderIsInert(t *testing.T) {
	var r *Recorder
	r.Record("proj", KindSemantic, []string{"a.go"})
	r.Start(context.Background())
	r.Stop()
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush on nil recorder: %v", err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatalf("stamp future version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a database from a newer build, want a refusal")
	}
}

func TestFlushIsIdempotentWhenNothingPending(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush with nothing pending: %v", err)
	}
}

func TestPathBeside(t *testing.T) {
	if got := PathBeside("/data/sqlite/projects.db"); got != "/data/sqlite/"+DBFileName {
		t.Errorf("PathBeside = %q, want the stats file next to the system database", got)
	}
	// A throwaway system database must not leave a stats file behind.
	if got := PathBeside(":memory:"); got != ":memory:" {
		t.Errorf("PathBeside(:memory:) = %q, want :memory:", got)
	}
	if got := PathBeside(""); got != ":memory:" {
		t.Errorf("PathBeside(\"\") = %q, want :memory:", got)
	}
}

// Record is called from every search handler concurrently while the flush loop
// writes. Under -race this is the test that says the swap-under-lock in Flush
// is actually holding, and the arithmetic at the end says no batch was lost or
// applied twice on the way through.
func TestConcurrentRecordAndFlush(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	const writers, perWriter = 8, 100

	stopFlusher := make(chan struct{})
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		for {
			select {
			case <-stopFlusher:
				return
			default:
			}
			if err := r.Flush(ctx); err != nil {
				t.Errorf("concurrent Flush: %v", err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			project := fmt.Sprintf("proj-%d", w%3)
			for i := 0; i < perWriter; i++ {
				r.Record(project, KindSemantic, []string{"a.go", "b.go"})
			}
		}(w)
	}
	wg.Wait()
	close(stopFlusher)
	<-flusherDone

	// Anything the racing flusher left behind.
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("final Flush: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: []string{"proj-0", "proj-1", "proj-2"},
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	var queries, fileHits int64
	for _, row := range page.Rows {
		queries += row.Queries
		fileHits += row.FileHits
	}
	if want := int64(writers * perWriter); queries != want {
		t.Errorf("recorded %d queries, want %d — a flush lost or duplicated a batch", queries, want)
	}
	if want := int64(writers * perWriter * 2); fileHits != want {
		t.Errorf("recorded %d file hits, want %d", fileHits, want)
	}
}

// Stop must not block on a recorder whose flush loop was never launched.
//
// main.go registers the deferred Stop as soon as the store opens and calls
// Start hundreds of lines later, with several boot checks in between that can
// abort — including the encryption-key mismatch, whose whole job is to fail
// loudly. A Stop that waited on a channel only the loop closes turned every one
// of those into a silent hang.
func TestStopWithoutStartDoesNotBlock(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)

	// Counters buffered before an aborted boot are drained by Stop rather than
	// dropped, which is why this case flushes inline instead of just returning.
	r.Record("proj-a", KindSemantic, []string{"a.go"})

	done := make(chan struct{})
	go func() { r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() on a never-started recorder blocked")
	}

	page, err := s.ProjectStatsPage(context.Background(), Query{
		ProjectPaths: []string{"proj-a"},
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Queries != 1 {
		t.Errorf("rows = %+v, want the buffered counter drained by Stop", page.Rows)
	}
}

// Stop is called from a defer and can also be reached by a second path; and
// Start must not be able to launch two loops, which would close r.stopped twice
// and panic.
func TestStartAndStopAreIdempotent(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)
	r.Start(ctx)

	done := make(chan struct{})
	go func() {
		r.Stop()
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("repeated Start/Stop blocked")
	}
}

// The window tables are delete-heavy, so a database that cannot return pages to
// the filesystem would only ever grow — the same free-list problem this package
// cites as a reason not to live inside projects.db.
func TestFreshDatabaseIsIncrementalAutoVacuum(t *testing.T) {
	s := newTestStore(t)
	var mode int
	if err := s.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatalf("read auto_vacuum: %v", err)
	}
	if mode != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (INCREMENTAL) — it can only be set before the header is written", mode)
	}
}

// Both window tables must be reachable by project, not just by bucket. The
// primary keys lead with `bucket` because that is what pruning needs; every
// read wants the other order, and without an index a per-project query walks the
// whole retained range across every project.
func TestWindowReadsUseAProjectIndex(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()
	r.Record("proj-a", KindSemantic, []string{"a.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	since := BucketOf(fixedNow.Add(-24 * time.Hour))

	for _, c := range []struct{ name, query, wantIndex string }{
		{
			"search_file_buckets by project",
			`SELECT file_path, SUM(hits) FROM search_file_buckets
			  WHERE project_id = 1 AND bucket >= ? GROUP BY file_path`,
			"idx_file_buckets_project",
		},
		{
			"search_buckets by project",
			`SELECT SUM(queries) FROM search_buckets
			  WHERE project_id = 1 AND bucket >= ? GROUP BY project_id`,
			"idx_buckets_project",
		},
	} {
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+c.query, since)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var plan string
		for rows.Next() {
			var a, b, d int
			var detail string
			if err := rows.Scan(&a, &b, &d, &detail); err != nil {
				rows.Close()
				t.Fatalf("scan plan: %v", err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if !strings.Contains(plan, c.wantIndex) {
			t.Errorf("%s did not use %s:\n%s", c.name, c.wantIndex, plan)
		}
	}
}

// Forget is best-effort and runs after a project delete has already committed,
// so a failed call — or a process that dies between the two — strands counters
// that no API read can ever surface, in a tier that is never pruned. The sweep
// is what stops that being permanent.
func TestForgetAllExceptSweepsOrphans(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	r.Record("live-one", KindSemantic, []string{"a.go"})
	r.Record("live-two", KindSemantic, []string{"b.go"})
	r.Record("deleted-long-ago", KindSemantic, []string{"secret.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	dropped, err := s.ForgetAllExcept(ctx, []string{"live-one", "live-two"})
	if err != nil {
		t.Fatalf("ForgetAllExcept: %v", err)
	}
	if dropped != 1 {
		t.Errorf("dropped %d projects, want 1", dropped)
	}

	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM search_file_totals`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("search_file_totals holds %d rows, want 2 — the cascade did not follow the sweep", n)
	}

	// An empty live set means "don't know", not "nothing is live". A caller
	// whose query failed must not be able to wipe every counter.
	dropped, err = s.ForgetAllExcept(ctx, nil)
	if err != nil {
		t.Fatalf("ForgetAllExcept(nil): %v", err)
	}
	if dropped != 0 {
		t.Errorf("an empty live set dropped %d projects, want 0", dropped)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects_seen`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("projects_seen holds %d rows after an empty sweep, want 2", n)
	}
}

// The page query has two shapes: one that aggregates every scoped project's
// files up front (needed when the ORDER or a filter depends on those columns)
// and one that computes them for the page's rows afterwards. They must produce
// identical numbers, or the same table would report different figures depending
// on which column the user happened to click.
func TestFileAggregateShapesAgree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Spread across two buckets and two kinds, so both the per-file regroup and
	// the kind filter are actually exercised rather than degenerate.
	early := recorderAt(t, s, fixedNow.Add(-2*time.Hour))
	early.Record("proj-a", KindSemantic, []string{"hot.go", "warm.go"})
	early.Record("proj-b", KindSemantic, []string{"other.go"})
	if err := early.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	late := recorderAt(t, s, fixedNow)
	for i := 0; i < 3; i++ {
		late.Record("proj-a", KindSemantic, []string{"hot.go"})
	}
	late.Record("proj-a", KindSymbols, []string{"sym.go"})
	late.Record("proj-b", KindSemantic, []string{"other.go", "extra.go"})
	late.Record("proj-c", KindSemantic, nil) // searched, nothing returned
	if err := late.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	scope := []string{"proj-a", "proj-b", "proj-c"}
	// The windows must include one that actually CUTS. With only {0, retention}
	// every seeded row falls inside every window, so the bucket predicate is
	// present but never excludes anything — and deleting it from
	// fillFileAggregates would change no output, leaving it untested. One hour
	// drops the rows seeded two hours back.
	//
	// Limit 2 against 3 matching projects is the other boundary: it makes the
	// page a strict SUBSET of the matched set, which is the only shape where
	// filling the file columns per page can disagree with computing them for
	// everything.
	for _, window := range []time.Duration{0, WindowRetention, time.Hour} {
		for _, kinds := range [][]string{nil, {KindSemantic}} {
			for _, limit := range []int{50, 2} {
				base := Query{
					ProjectPaths: scope, Kinds: kinds, Window: window, TopFiles: 0, Limit: limit,
				}
				// Sorting by queries takes the cheap path; sorting by a file column
				// forces the full aggregate. Same rows either way.
				cheap := base
				cheap.Sort = SortQueries
				full := base
				full.Sort = SortFileHits

				if cheap.needsFileAggregate() {
					t.Fatal("sorting by queries should not need the full file aggregate")
				}
				if !full.needsFileAggregate() {
					t.Fatal("sorting by file_hits must need the full file aggregate")
				}

				cheapPage, err := s.ProjectStatsPage(ctx, cheap, fixedNow)
				if err != nil {
					t.Fatalf("cheap page: %v", err)
				}
				fullPage, err := s.ProjectStatsPage(ctx, full, fixedNow)
				if err != nil {
					t.Fatalf("full page: %v", err)
				}

				label := fmt.Sprintf("window=%v kinds=%v limit=%d", window, kinds, limit)
				if cheapPage.Total != fullPage.Total {
					t.Fatalf("%s: totals differ, %d vs %d", label, cheapPage.Total, fullPage.Total)
				}
				// The two pages are sorted differently on purpose, so compare by
				// project rather than by position. Every project the cheap path
				// returned must carry the same file columns the full aggregate
				// computed for it.
				byPath := map[string]ProjectStats{}
				for _, r := range fullPage.Rows {
					byPath[r.ProjectPath] = r
				}
				// A subset page can legitimately hold projects the other page's
				// different ordering left off, so build the full picture from an
				// unpaginated run when the pages are cut short.
				if limit < 50 {
					wide := full
					wide.Limit = 50
					widePage, werr := s.ProjectStatsPage(ctx, wide, fixedNow)
					if werr != nil {
						t.Fatalf("%s: wide page: %v", label, werr)
					}
					for _, r := range widePage.Rows {
						byPath[r.ProjectPath] = r
					}
				}
				if len(cheapPage.Rows) == 0 {
					t.Fatalf("%s: cheap page returned nothing to compare", label)
				}
				for _, got := range cheapPage.Rows {
					want, ok := byPath[got.ProjectPath]
					if !ok {
						t.Fatalf("%s: %s missing from the full-aggregate page", label, got.ProjectPath)
					}
					if got.FileHits != want.FileHits ||
						got.DistinctFiles != want.DistinctFiles ||
						got.TopFileHits != want.TopFileHits {
						t.Errorf("%s: %s file columns differ — cheap(%d,%d,%d) full(%d,%d,%d)",
							label, got.ProjectPath,
							got.FileHits, got.DistinctFiles, got.TopFileHits,
							want.FileHits, want.DistinctFiles, want.TopFileHits)
					}
				}
			}
		}
	}
}

// The footer sums and the row count come from window functions over the page's
// own result set now, so they must still describe the whole filtered set rather
// than the visible page.
func TestFooterTotalsSpanTheFilteredSetNotThePage(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()

	scope := []string{"p1", "p2", "p3", "p4"}
	for i, p := range scope {
		for q := 0; q <= i; q++ { // 1, 2, 3, 4 queries
			r.Record(p, KindSemantic, []string{"a.go"})
		}
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	page, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, Sort: SortQueries, Desc: true, Limit: 2,
	}, fixedNow)
	if err != nil {
		t.Fatalf("ProjectStatsPage: %v", err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("page holds %d rows, want 2", len(page.Rows))
	}
	if page.Total != 4 {
		t.Errorf("total = %d, want 4", page.Total)
	}
	if page.TotalQueries != 10 {
		t.Errorf("footer queries = %d, want 10 (1+2+3+4), not the page's 7", page.TotalQueries)
	}

	// Paging past the end returns no rows — but must still report how large the
	// set is. `total` is documented as the count BEFORE limit and offset, and
	// the dashboard polls every thirty seconds: a set that shrinks under a
	// reader sitting on the last page would otherwise collapse the pager and
	// render "nothing recorded" over projects that are still there.
	empty, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, Sort: SortQueries, Limit: 2, Offset: 99,
	}, fixedNow)
	if err != nil {
		t.Fatalf("offset past the end: %v", err)
	}
	if len(empty.Rows) != 0 {
		t.Errorf("offset past the end returned %d rows", len(empty.Rows))
	}
	if empty.Total != 4 {
		t.Errorf("total past the end = %d, want 4 — it counts what matched, not what was returned", empty.Total)
	}
	if empty.TotalQueries != 10 {
		t.Errorf("footer queries past the end = %d, want 10", empty.TotalQueries)
	}

	// A filter that genuinely matches nothing is a different answer, and must
	// not be dressed up with the unfiltered totals.
	tooHigh := int64(1000)
	none, err := s.ProjectStatsPage(ctx, Query{
		ProjectPaths: scope, Sort: SortQueries, Limit: 2, Offset: 99, MinQueries: &tooHigh,
	}, fixedNow)
	if err != nil {
		t.Fatalf("no matches: %v", err)
	}
	if none.Total != 0 || none.TotalQueries != 0 {
		t.Errorf("a filter matching nothing reported total=%d queries=%d, want zeroes",
			none.Total, none.TotalQueries)
	}
}

// needsFileAggregate decides whether the expensive per-project file aggregate
// runs. If a sort key that reads a file column is ever added without being
// listed in fileDerivedSorts, the page is built from a stub CTE and the request
// silently returns zeros in the columns it was sorted by — wrong numbers, no
// error. This pins the two lists to each other so that cannot happen quietly.
func TestFileDerivedSortsMatchSortColumns(t *testing.T) {
	fileColumns := map[string]struct{}{
		"file_hits": {}, "distinct_files": {}, "top_file_hits": {},
	}
	for key, expr := range sortColumns {
		_, isFileColumn := fileColumns[expr]
		_, declared := fileDerivedSorts[key]
		if isFileColumn && !declared {
			t.Errorf("sort key %q orders by the file column %q but is missing from fileDerivedSorts — "+
				"requests using it would be served from the stub aggregate", key, expr)
		}
		if declared && !isFileColumn {
			t.Errorf("sort key %q is in fileDerivedSorts but orders by %q, which the per-file "+
				"aggregate does not produce — it would pay for work it does not use", key, expr)
		}
	}
	for key := range fileDerivedSorts {
		if _, ok := sortColumns[key]; !ok {
			t.Errorf("fileDerivedSorts names %q, which is not a valid sort key", key)
		}
	}
}

// Every sort key must resolve against the page query's OUTPUT columns, because
// the ordering is applied both inside the subquery and on the statement wrapping
// it, and only the inner scope can see the source tables.
func TestEverySortKeyRunsAtBothOrderingLevels(t *testing.T) {
	s := newTestStore(t)
	r := recorderAt(t, s, fixedNow)
	ctx := context.Background()
	r.Record("p1", KindSemantic, []string{"a.go"})
	r.Record("p2", KindSemantic, []string{"b.go", "c.go"})
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for key := range sortColumns {
		for _, desc := range []bool{false, true} {
			page, err := s.ProjectStatsPage(ctx, Query{
				ProjectPaths: []string{"p1", "p2"}, Sort: key, Desc: desc, Limit: 1, TopFiles: 2,
			}, fixedNow)
			if err != nil {
				t.Errorf("sort=%s desc=%v: %v", key, desc, err)
				continue
			}
			if len(page.Rows) != 1 || page.Total != 2 {
				t.Errorf("sort=%s desc=%v: %d rows / total %d, want 1 and 2",
					key, desc, len(page.Rows), page.Total)
			}
		}
	}
}
