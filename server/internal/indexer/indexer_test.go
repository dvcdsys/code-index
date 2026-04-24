package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fakeEmbedder returns deterministic unit vectors — enough for vectorstore
// upsert and search to exercise the full path without a llama-server sidecar.
type fakeEmbedder struct {
	dim  int
	busy bool
}

func (f *fakeEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if f.busy {
		return nil, &embeddings.ErrBusy{RetryAfter: 5}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		// Simple hash-like mapping: first byte of each segment seeds.
		for j := 0; j < f.dim && j < len(t); j++ {
			v[j] = float32(t[j]) / 255.0
		}
		out[i] = v
	}
	return out, nil
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedProject(t *testing.T, d *sql.DB, path string) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`,
		path, path, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newStore(t *testing.T) *vectorstore.Store {
	t.Helper()
	tmp := t.TempDir()
	vs, err := vectorstore.Open(filepath.Join(tmp, "chroma"))
	if err != nil {
		t.Fatalf("vectorstore open: %v", err)
	}
	return vs
}

// ---------------------------------------------------------------------------

func TestBeginIndexing_Incremental(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	// Seed a prior hash so stored_hashes is populated.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO file_hashes (project_path, file_path, content_hash, indexed_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj/a.go", "deadbeef", "2026-01-01",
	); err != nil {
		t.Fatal(err)
	}

	runID, hashes, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}
	if runID == "" {
		t.Fatal("run_id empty")
	}
	if got := hashes["/proj/a.go"]; got != "deadbeef" {
		t.Errorf("stored_hashes[/proj/a.go] = %q, want deadbeef", got)
	}

	// Run row must exist.
	var status string
	_ = d.QueryRowContext(ctx,
		`SELECT status FROM index_runs WHERE id = ?`, runID,
	).Scan(&status)
	if status != "running" {
		t.Errorf("run status = %q, want running", status)
	}
}

func TestBeginIndexing_Full_WipesState(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	_, _ = d.ExecContext(ctx,
		`INSERT INTO file_hashes (project_path, file_path, content_hash, indexed_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj/a.go", "deadbeef", "2026-01-01")

	_, hashes, err := svc.BeginIndexing(ctx, "/proj", true)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}
	if len(hashes) != 0 {
		t.Errorf("full=true must return empty hashes, got %v", hashes)
	}

	var cnt int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_hashes WHERE project_path = ?`, "/proj").Scan(&cnt)
	if cnt != 0 {
		t.Errorf("file_hashes must be wiped, got %d rows", cnt)
	}
}

// TestBeginIndexing_ConflictOnConcurrent covers C2: a second /index/begin
// for the same project while the first session is still active must return
// ErrSessionConflict. A different project must be allowed.
func TestBeginIndexing_ConflictOnConcurrent(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/p1")
	seedProject(t, d, "/p2")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	if _, _, err := svc.BeginIndexing(ctx, "/p1", false); err != nil {
		t.Fatalf("first BeginIndexing: %v", err)
	}

	// Second call for the same project must conflict.
	if _, _, err := svc.BeginIndexing(ctx, "/p1", false); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("second BeginIndexing: want ErrSessionConflict, got %v", err)
	}

	// Different project must succeed.
	if _, _, err := svc.BeginIndexing(ctx, "/p2", false); err != nil {
		t.Fatalf("BeginIndexing on different project: %v", err)
	}
}

func TestProcessFiles_HappyPath(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}

	goFile := "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	files := []FilePayload{
		{
			Path:        "/proj/main.go",
			Content:     goFile,
			ContentHash: sha256hex(goFile),
			Language:    "go",
			Size:        len(goFile),
		},
	}

	accepted, chunks, total, err := svc.ProcessFiles(ctx, "/proj", runID, files)
	if err != nil {
		t.Fatalf("ProcessFiles: %v", err)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d, want 1", accepted)
	}
	if chunks == 0 {
		t.Errorf("chunks = 0, want >0")
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}

	// file_hashes updated.
	var hash string
	_ = d.QueryRowContext(ctx,
		`SELECT content_hash FROM file_hashes WHERE project_path = ? AND file_path = ?`,
		"/proj", "/proj/main.go",
	).Scan(&hash)
	if hash != sha256hex(goFile) {
		t.Errorf("content_hash = %q, want %q", hash, sha256hex(goFile))
	}

	// Symbol inserted.
	var symCount int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE project_path = ?`, "/proj").Scan(&symCount)
	if symCount == 0 {
		t.Error("expected at least one symbol (Add function)")
	}

	// Vectorstore count matches chunks.
	if got := vs.Count("/proj"); got != chunks {
		t.Errorf("vs.Count = %d, want %d", got, chunks)
	}
}

func TestProcessFiles_EmbedderBusy(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8, busy: true}, nil)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}

	_, _, _, err = svc.ProcessFiles(ctx, "/proj", runID, []FilePayload{
		{Path: "/proj/a.go", Content: "package x\nfunc F(){}\n", ContentHash: "h", Language: "go"},
	})
	if err == nil {
		t.Fatal("expected busy error, got nil")
	}
	if _, busy := embeddings.IsBusy(err); !busy {
		t.Errorf("error is not ErrBusy: %v", err)
	}
}

func TestProcessFiles_UnknownRunID(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	_, _, _, err := svc.ProcessFiles(context.Background(), "/proj", "no-such-run", nil)
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestFinishIndexing_UpdatesProject(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}

	goFile := "package main\nfunc X() {}\n"
	_, _, _, err = svc.ProcessFiles(ctx, "/proj", runID, []FilePayload{
		{Path: "/proj/a.go", Content: goFile, ContentHash: sha256hex(goFile), Language: "go"},
	})
	if err != nil {
		t.Fatalf("ProcessFiles: %v", err)
	}

	status, filesProcessed, chunks, err := svc.FinishIndexing(ctx, "/proj", runID, nil, 1)
	if err != nil {
		t.Fatalf("FinishIndexing: %v", err)
	}
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if filesProcessed != 1 || chunks == 0 {
		t.Errorf("files=%d chunks=%d", filesProcessed, chunks)
	}

	// Project row reflects completion.
	var projStatus, stats string
	_ = d.QueryRowContext(ctx, `SELECT status, stats FROM projects WHERE host_path = ?`, "/proj").Scan(&projStatus, &stats)
	if projStatus != "indexed" {
		t.Errorf("project.status = %q, want indexed", projStatus)
	}
	if stats == "" {
		t.Error("stats blob empty")
	}

	// Index run marked completed.
	var runStatus string
	_ = d.QueryRowContext(ctx, `SELECT status FROM index_runs WHERE id = ?`, runID).Scan(&runStatus)
	if runStatus != "completed" {
		t.Errorf("run_status = %q, want completed", runStatus)
	}
}

func TestFinishIndexing_DeletesPaths(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate a file.
	f := "package x\nfunc A(){}\n"
	_, _, _, _ = svc.ProcessFiles(ctx, "/proj", runID, []FilePayload{
		{Path: "/proj/gone.go", Content: f, ContentHash: sha256hex(f), Language: "go"},
	})

	// Now report it deleted on finish.
	_, _, _, err = svc.FinishIndexing(ctx, "/proj", runID, []string{"/proj/gone.go"}, 0)
	if err != nil {
		t.Fatalf("FinishIndexing: %v", err)
	}

	var cnt int
	_ = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file_hashes WHERE project_path = ? AND file_path = ?`,
		"/proj", "/proj/gone.go",
	).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("file_hashes should be removed, got %d", cnt)
	}
}

func TestGetProgress_Active(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	if p := svc.GetProgress("/proj"); p != nil {
		t.Errorf("expected nil before begin, got %+v", p)
	}

	runID, _, _ := svc.BeginIndexing(ctx, "/proj", false)

	p := svc.GetProgress("/proj")
	if p == nil {
		t.Fatal("progress is nil after begin")
	}
	if p.RunID != runID {
		t.Errorf("progress.RunID=%q, want %q", p.RunID, runID)
	}
	if p.Status != "indexing" {
		t.Errorf("progress.Status=%q, want indexing", p.Status)
	}
}

func TestProcessFiles_RunIDMismatch(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj-a")
	seedProject(t, d, "/proj-b")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	runID, _, _ := svc.BeginIndexing(ctx, "/proj-a", false)
	_, _, _, err := svc.ProcessFiles(ctx, "/proj-b", runID, nil)
	if !errors.Is(err, ErrProjectMismatch) {
		t.Errorf("err=%v, want ErrProjectMismatch", err)
	}
}
