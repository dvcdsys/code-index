package searchstats

import (
	"context"
	"fmt"
	"path/filepath"
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
