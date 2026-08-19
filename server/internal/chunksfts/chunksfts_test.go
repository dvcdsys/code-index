package chunksfts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func upsert(t *testing.T, d *sql.DB, project, file string, chunks []Chunk) {
	t.Helper()
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := UpsertByFileTx(ctx, tx, project, file, chunks); err != nil {
		t.Fatalf("UpsertByFileTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestUpsertAndSearchProject_FindsLiteralToken(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	upsert(t, d, "proj-a", "src/widget_processor.go", []Chunk{
		{Content: "func ProcessWidget(w *Widget) error { ... }", FilePath: "src/widget_processor.go", StartLine: 1, EndLine: 5, SymbolName: "ProcessWidget", Language: "go"},
		{Content: "// WIDGET is the internal product code", FilePath: "src/widget_processor.go", StartLine: 10, EndLine: 10, Language: "go"},
	})
	upsert(t, d, "proj-b", "src/util.go", []Chunk{
		{Content: "func helloWorld() {}", FilePath: "src/util.go", StartLine: 1, EndLine: 3, SymbolName: "helloWorld", Language: "go"},
	})

	hits, err := SearchProject(ctx, d, "proj-a", "WIDGET", 10)
	if err != nil {
		t.Fatalf("SearchProject: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one WIDGET hit in proj-a")
	}
	for _, h := range hits {
		if !strings.Contains(strings.ToLower(h.Content+h.SymbolName), "widget") {
			t.Errorf("hit doesn't actually mention widget: %+v", h)
		}
	}

	hitsB, err := SearchProject(ctx, d, "proj-b", "WIDGET", 10)
	if err != nil {
		t.Fatalf("SearchProject b: %v", err)
	}
	if len(hitsB) != 0 {
		t.Errorf("expected zero WIDGET hits in proj-b, got %d", len(hitsB))
	}
}

func TestSearchProject_RanksMoreMentionsHigher(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Two chunks in the same project; chunk-1 mentions "ping" once,
	// chunk-2 mentions it many times. BM25 should rank chunk-2 higher.
	upsert(t, d, "p", "f.go", []Chunk{
		{Content: "this code does a ping once", FilePath: "f.go", StartLine: 1, EndLine: 1, Language: "go"},
		{Content: "ping ping ping ping ping handle request ping loop", FilePath: "f.go", StartLine: 2, EndLine: 2, Language: "go"},
	})

	hits, err := SearchProject(ctx, d, "p", "ping", 10)
	if err != nil {
		t.Fatalf("SearchProject: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected >=2 hits, got %d", len(hits))
	}
	if hits[0].StartLine != 2 {
		t.Errorf("expected line 2 ranked first; got order %v %v", hits[0].StartLine, hits[1].StartLine)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("expected hits[0].Score > hits[1].Score, got %v vs %v", hits[0].Score, hits[1].Score)
	}
}

func TestSearchProject_OrJoinsTokens(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	upsert(t, d, "p", "f.go", []Chunk{
		{Content: "totally unrelated content", FilePath: "f.go", StartLine: 1, EndLine: 1, Language: "go"},
		{Content: "this mentions ping only", FilePath: "f.go", StartLine: 2, EndLine: 2, Language: "go"},
		{Content: "this mentions WIDGET only", FilePath: "f.go", StartLine: 3, EndLine: 3, Language: "go"},
		{Content: "this mentions both ping and WIDGET", FilePath: "f.go", StartLine: 4, EndLine: 4, Language: "go"},
	})

	hits, err := SearchProject(ctx, d, "p", "ping WIDGET", 10)
	if err != nil {
		t.Fatalf("SearchProject: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits (lines 2,3,4), got %d", len(hits))
	}
	if hits[0].StartLine != 4 {
		t.Errorf("expected the both-tokens chunk (line 4) ranked first, got line %d", hits[0].StartLine)
	}
}

func TestSearchProject_TrigramMatchesInsideCamelCase(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p", "f.go", []Chunk{
		{Content: "func processWidgetItemEvent() {}", FilePath: "f.go", StartLine: 1, EndLine: 1, SymbolName: "processWidgetItemEvent", Language: "go"},
		{Content: "func helloWorld() {}", FilePath: "f.go", StartLine: 2, EndLine: 2, SymbolName: "helloWorld", Language: "go"},
	})
	hits, err := SearchProject(ctx, d, "p", "Widget", 10)
	if err != nil {
		t.Fatalf("SearchProject: %v", err)
	}
	if len(hits) != 1 || hits[0].StartLine != 1 {
		t.Errorf("trigram should match inside CamelCase identifier, got %v", hits)
	}
}

func TestSearchProject_EmptyQueryReturnsNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p", "f.go", []Chunk{{Content: "anything", FilePath: "f.go", StartLine: 1, EndLine: 1}})
	for _, q := range []string{"", "   ", "a", " a b "} {
		hits, err := SearchProject(ctx, d, "p", q, 10)
		if err != nil {
			t.Fatalf("SearchProject %q: %v", q, err)
		}
		if len(hits) != 0 {
			t.Errorf("query %q expected 0 hits, got %d", q, len(hits))
		}
	}
}

func TestUpsertByFile_ReplacesExisting(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p", "f.go", []Chunk{
		{Content: "old WIDGET content", FilePath: "f.go", StartLine: 1, EndLine: 1, Language: "go"},
	})
	upsert(t, d, "p", "f.go", []Chunk{
		{Content: "new replacement content", FilePath: "f.go", StartLine: 1, EndLine: 1, Language: "go"},
	})
	hits, err := SearchProject(ctx, d, "p", "WIDGET", 10)
	if err != nil {
		t.Fatalf("SearchProject: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("old content should be gone after upsert, got %d hits", len(hits))
	}
	hits2, err := SearchProject(ctx, d, "p", "replacement", 10)
	if err != nil {
		t.Fatalf("SearchProject 2: %v", err)
	}
	if len(hits2) != 1 {
		t.Errorf("new content should be searchable, got %d hits", len(hits2))
	}
}

func TestDeleteByFile_RemovesFromFTS(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p", "f.go", []Chunk{{Content: "WIDGET is here", FilePath: "f.go", StartLine: 1, EndLine: 1}})
	upsert(t, d, "p", "g.go", []Chunk{{Content: "also WIDGET here", FilePath: "g.go", StartLine: 1, EndLine: 1}})

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteByFileTx(ctx, tx, "p", "f.go"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	hits, _ := SearchProject(ctx, d, "p", "WIDGET", 10)
	if len(hits) != 1 || hits[0].FilePath != "g.go" {
		t.Errorf("expected only g.go to remain, got %+v", hits)
	}

	// chunks_meta must be drained too — no orphan rows.
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks_meta WHERE project_path = ? AND file_path = ?`,
		"p", "f.go").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 chunks_meta rows for deleted file, got %d", n)
	}
}

func TestDeleteByProject_RemovesEverything(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p1", "a.go", []Chunk{{Content: "WIDGET here", FilePath: "a.go", StartLine: 1, EndLine: 1}})
	upsert(t, d, "p1", "b.go", []Chunk{{Content: "more WIDGET", FilePath: "b.go", StartLine: 1, EndLine: 1}})
	upsert(t, d, "p2", "c.go", []Chunk{{Content: "p2 WIDGET", FilePath: "c.go", StartLine: 1, EndLine: 1}})

	if err := DeleteByProject(ctx, d, "p1"); err != nil {
		t.Fatal(err)
	}

	hits1, _ := SearchProject(ctx, d, "p1", "WIDGET", 10)
	if len(hits1) != 0 {
		t.Errorf("expected p1 wiped, got %d hits", len(hits1))
	}
	hits2, _ := SearchProject(ctx, d, "p2", "WIDGET", 10)
	if len(hits2) != 1 {
		t.Errorf("expected p2 untouched, got %d hits", len(hits2))
	}

	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_meta WHERE project_path = ?`, "p1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 chunks_meta rows for p1, got %d", n)
	}
}

func TestSearchProject_ScopedToProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	upsert(t, d, "p1", "a.go", []Chunk{{Content: "WIDGET order", FilePath: "a.go", StartLine: 1, EndLine: 1}})
	upsert(t, d, "p2", "b.go", []Chunk{{Content: "WIDGET payment", FilePath: "b.go", StartLine: 1, EndLine: 1}})

	hits, err := SearchProject(ctx, d, "p1", "WIDGET", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FilePath != "a.go" {
		t.Errorf("expected only a.go from p1, got %+v", hits)
	}
}

func TestBuildFTS5Query(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"a b", ""},
		{"WIDGET", `"WIDGET"`},
		{"add ping WIDGET", `"add" OR "ping" OR "WIDGET"`},
		{`oh "yes"`, `"oh" OR """yes"""`},
	}
	for _, c := range cases {
		got := buildFTS5Query(c.in)
		if got != c.want {
			t.Errorf("buildFTS5Query(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// hitKey identifies a hit for comparison. Content is included because two
// chunks of the same file can share a line span only if something upstream
// is wrong, and if that ever happens the comparison should notice.
func hitKey(h Hit) string {
	return fmt.Sprintf("%s:%d-%d|%s|%.6f", h.FilePath, h.StartLine, h.EndLine, h.SymbolName, h.Score)
}

func keysOf(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitKey(h))
	}
	return out
}

// seedCorpus fills several projects with overlapping vocabulary, so BM25 has
// something to rank rather than a single obvious winner per project.
func seedCorpus(t *testing.T, d *sql.DB, projects []string) {
	t.Helper()
	bodies := []string{
		"func retryWithBackoff(ctx context.Context) error { return retry(ctx) }",
		"// retry policy: exponential backoff with jitter, capped at one minute",
		"func backoffDuration(attempt int) time.Duration { return base << attempt }",
		"type RetryPolicy struct { MaxAttempts int; Backoff time.Duration }",
		"// no mention of the interesting words at all, just filler content here",
		"func retry(ctx context.Context) error { for { if err := do(); err == nil { return nil } } }",
	}
	for pi, p := range projects {
		for bi, body := range bodies {
			// Vary how many copies each project gets so BM25 scores differ
			// across projects rather than tying everywhere.
			copies := 1 + (pi+bi)%3
			chunks := make([]Chunk, 0, copies)
			for c := 0; c < copies; c++ {
				chunks = append(chunks, Chunk{
					Content:    body,
					FilePath:   fmt.Sprintf("src/f%02d.go", bi),
					StartLine:  1 + c*10,
					EndLine:    5 + c*10,
					SymbolName: fmt.Sprintf("S%02d", bi),
					Language:   "go",
				})
			}
			upsert(t, d, p, fmt.Sprintf("src/f%02d.go", bi), chunks)
		}
		// Deliberate BM25 ties: byte-identical chunks in different files
		// score identically, so any limit below their count forces the
		// engine to pick some of them arbitrarily. Without an explicit
		// tiebreak the per-project query and the partitioned one are free
		// to pick differently, and the equivalence this test asserts would
		// hold only by luck. Ties are not exotic here — a trigram index
		// over real code is full of near-duplicate boilerplate.
		// file_path and symbol_name are indexed columns, so a tie needs all
		// three to match: same file, same symbol, same content. Only the
		// line span differs, and line numbers are not part of the index.
		tied := make([]Chunk, 0, 6)
		for i := 0; i < 6; i++ {
			tied = append(tied, Chunk{
				Content:    "func retryWithBackoff(ctx context.Context) error { return retry(ctx) }",
				FilePath:   "src/tied.go",
				StartLine:  1 + i*10,
				EndLine:    5 + i*10,
				SymbolName: "Tie",
				Language:   "go",
			})
		}
		upsert(t, d, p, "src/tied.go", tied)
	}
}

// TestSearchProjects_MatchesPerProjectQueries is the equivalence test for the
// workspace-wide query: for every project, the partitioned result must be what
// the per-project query returns — same hits, same order, same scores.
//
// This is the whole safety argument for replacing N queries with one. The
// per-project BM25 signal feeds project candidacy in workspace search, so a
// partitioned result that merely contains the right rows in a different order
// would silently re-rank projects.
func TestSearchProjects_MatchesPerProjectQueries(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	projects := []string{"proj-a", "proj-b", "proj-c", "proj-d"}
	seedCorpus(t, d, projects)

	for _, query := range []string{
		"retry backoff",
		"retry",
		"exponential backoff with jitter",
		"nothing matches this string xyzzy",
	} {
		for _, limit := range []int{3, 50} {
			batched, err := SearchProjects(ctx, d, projects, query, limit)
			if err != nil {
				t.Fatalf("SearchProjects(%q, %d): %v", query, limit, err)
			}
			for _, p := range projects {
				want, err := SearchProject(ctx, d, p, query, limit)
				if err != nil {
					t.Fatalf("SearchProject(%q, %q): %v", p, query, err)
				}
				got := batched[p]
				if len(want) == 0 {
					if _, present := batched[p]; present {
						t.Errorf("%q/%q limit=%d: project with no hits is present in the map",
							query, p, limit)
					}
					continue
				}
				gk, wk := keysOf(got), keysOf(want)
				if len(gk) != len(wk) {
					t.Errorf("%q/%q limit=%d: got %d hits, per-project query returns %d",
						query, p, limit, len(gk), len(wk))
					continue
				}
				for i := range wk {
					if gk[i] != wk[i] {
						t.Errorf("%q/%q limit=%d: rank %d differs\n got  %s\n want %s",
							query, p, limit, i, gk[i], wk[i])
						break
					}
				}
			}
		}
	}
}

// TestSearchProjects_DoesNotPrefixMatchProjectPaths guards the IN list. Project
// paths are namespaced strings that routinely share prefixes — "local:host:/x"
// and "local:host:/x/y" are different projects — so an implementation that
// filtered with LIKE, or that built the list by concatenation, would leak one
// project's chunks into another's slice.
func TestSearchProjects_DoesNotPrefixMatchProjectPaths(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	upsert(t, d, "proj", "a.go", []Chunk{
		{Content: "func retryWithBackoff() {}", FilePath: "a.go", StartLine: 1, EndLine: 2, Language: "go"},
	})
	upsert(t, d, "proj-extended", "b.go", []Chunk{
		{Content: "func retryWithBackoff() {}", FilePath: "b.go", StartLine: 1, EndLine: 2, Language: "go"},
	})

	got, err := SearchProjects(ctx, d, []string{"proj"}, "retry", 50)
	if err != nil {
		t.Fatalf("SearchProjects: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("asked about one project, got slices for %d: %v", len(got), got)
	}
	for _, h := range got["proj"] {
		if h.FilePath != "a.go" {
			t.Errorf("hit from another project leaked in: %+v", h)
		}
	}
	if _, present := got["proj-extended"]; present {
		t.Error("a project that was not asked about appears in the result")
	}
}

// TestSearchProjects_SpansTheBatchBoundary checks the IN-list batching. The
// map is filled across several statements, so a batch that overwrote instead
// of appending, or that dropped its last slice, would only show up above the
// batch size.
func TestSearchProjects_SpansTheBatchBoundary(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const n = searchProjectsBatch + 7
	projects := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("p%04d", i)
		projects = append(projects, p)
		upsert(t, d, p, "a.go", []Chunk{
			{Content: "func retryWithBackoff() {}", FilePath: "a.go", StartLine: 1, EndLine: 2, Language: "go"},
		})
	}

	got, err := SearchProjects(ctx, d, projects, "retry", 50)
	if err != nil {
		t.Fatalf("SearchProjects: %v", err)
	}
	if len(got) != n {
		t.Errorf("got hits for %d projects, want %d — a batch was lost or overwritten", len(got), n)
	}
	for _, p := range projects {
		if len(got[p]) != 1 {
			t.Errorf("%s: got %d hits, want 1", p, len(got[p]))
		}
	}
}

// TestSearchProjects_EmptyInputs pins the two no-op paths, both of which must
// avoid touching the DB: nothing to match, and nobody to match it for.
func TestSearchProjects_EmptyInputs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedCorpus(t, d, []string{"proj-a"})

	if got, err := SearchProjects(ctx, d, []string{"proj-a"}, "  x  ", 50); err != nil || got != nil {
		t.Errorf("all-tokens-too-short query: got %v, %v; want nil, nil", got, err)
	}
	if got, err := SearchProjects(ctx, d, nil, "retry", 50); err != nil || got != nil {
		t.Errorf("no projects: got %v, %v; want nil, nil", got, err)
	}
}
