package chunksfts

import (
	"context"
	"database/sql"
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
