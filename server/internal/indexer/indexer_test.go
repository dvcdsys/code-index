package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// recordingEmbedder records peak in-flight concurrency and total call count so
// the parallel/batched embed pipeline can be asserted. Implements
// TokenAwareEmbedder so ProcessFiles routes through TokenizeAndEmbed — the
// production (voyage/ollama) path.
type recordingEmbedder struct {
	dim         int
	delay       time.Duration
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	calls       int
}

func (r *recordingEmbedder) embed(_ context.Context, texts []string) ([][]float32, error) {
	r.mu.Lock()
	r.inFlight++
	r.calls++
	if r.inFlight > r.maxInFlight {
		r.maxInFlight = r.inFlight
	}
	r.mu.Unlock()
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, r.dim)
	}
	return out, nil
}

func (r *recordingEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	return r.embed(ctx, texts)
}

func (r *recordingEmbedder) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	return r.embed(ctx, texts)
}

// TestEmbedPrepared_ParallelAndBatched proves Lever 1 (concurrent embed calls
// across files) and Lever 2 (cross-file chunk batching): 12 files × 3 chunks
// with batchChunks=10 → groups of 3 files (9 chunks each), run concurrently
// at concurrency=4.
func TestEmbedPrepared_ParallelAndBatched(t *testing.T) {
	rec := &recordingEmbedder{dim: 8, delay: 25 * time.Millisecond}
	svc := New(nil, nil, rec, nil)
	svc.SetEmbedConcurrency(4)
	svc.SetEmbedBatchChunks(10)

	const nFiles, perFile = 12, 3
	prep := make([]*preparedFile, nFiles)
	for i := range prep {
		texts := make([]string, perFile)
		for j := range texts {
			texts[j] = fmt.Sprintf("file%d-chunk%d", i, j)
		}
		prep[i] = &preparedFile{texts: texts}
	}

	if err := svc.embedPrepared(context.Background(), &session{}, prep); err != nil {
		t.Fatalf("embedPrepared: %v", err)
	}

	for i, p := range prep {
		if p.embedErr != nil {
			t.Fatalf("file %d embedErr: %v", i, p.embedErr)
		}
		if len(p.embs) != perFile {
			t.Fatalf("file %d: got %d embs, want %d", i, len(p.embs), perFile)
		}
		for _, v := range p.embs {
			if len(v) != 8 {
				t.Fatalf("file %d: embedding dim %d, want 8", i, len(v))
			}
		}
	}
	// Lever 2: cross-file batching → fewer embed calls than files.
	if rec.calls >= nFiles {
		t.Errorf("batching: %d embed calls for %d files; expected cross-file grouping to reduce it", rec.calls, nFiles)
	}
	// Lever 1: groups ran concurrently (would peak at 1 if sequential).
	if rec.maxInFlight < 2 {
		t.Errorf("parallelism: peak in-flight embeds was %d; expected >1 at concurrency=4", rec.maxInFlight)
	}
}

// TestEmbedPrepared_Sequential confirms concurrency<=1 + batchChunks=0 keeps
// the legacy behaviour: one embed call per file, never overlapping.
func TestEmbedPrepared_Sequential(t *testing.T) {
	rec := &recordingEmbedder{dim: 8, delay: 10 * time.Millisecond}
	svc := New(nil, nil, rec, nil)
	svc.SetEmbedConcurrency(1)
	svc.SetEmbedBatchChunks(0)

	prep := make([]*preparedFile, 5)
	for i := range prep {
		prep[i] = &preparedFile{texts: []string{fmt.Sprintf("f%d", i)}}
	}
	if err := svc.embedPrepared(context.Background(), &session{}, prep); err != nil {
		t.Fatalf("embedPrepared: %v", err)
	}
	if rec.maxInFlight != 1 {
		t.Errorf("sequential: peak in-flight was %d, want 1", rec.maxInFlight)
	}
	if rec.calls != 5 {
		t.Errorf("no batching: want 5 calls (one per file), got %d", rec.calls)
	}
}

func TestPlanEmbedGroups(t *testing.T) {
	mk := func(counts ...int) []*preparedFile {
		out := make([]*preparedFile, len(counts))
		for i, n := range counts {
			out[i] = &preparedFile{texts: make([]string, n)}
		}
		return out
	}
	// maxChunks=0 → one group per file.
	if g := planEmbedGroups(mk(3, 3, 3), 0); len(g) != 3 {
		t.Errorf("maxChunks=0: want 3 groups, got %d", len(g))
	}
	// maxChunks=10, files 3,3,3,3,3 → [0,1,2]=9 then [3,4]=6 → 2 groups.
	g := planEmbedGroups(mk(3, 3, 3, 3, 3), 10)
	if len(g) != 2 {
		t.Fatalf("want 2 groups, got %d", len(g))
	}
	if len(g[0].fileIdx) != 3 || g[0].nchunks != 9 {
		t.Errorf("group0 = %+v, want 3 files / 9 chunks", g[0])
	}
	// A single oversized file forms its own group.
	g2 := planEmbedGroups(mk(15, 2), 10)
	if len(g2) != 2 || len(g2[0].fileIdx) != 1 {
		t.Errorf("oversized file should be its own group; got %+v", g2)
	}
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

// TestFailIndexing_ReleasesSessionForRetry covers the regression where an
// aborted in-process run (e.g. a transient "embedding queue saturated") left
// its session active until the idle-timeout, so every retry bounced off
// ErrSessionConflict. FailIndexing must release the session immediately so the
// next BeginIndexing for the same project succeeds, and must be idempotent.
func TestFailIndexing_ReleasesSessionForRetry(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}

	// While the session is active a retry must conflict (the bug's symptom).
	if _, _, err := svc.BeginIndexing(ctx, "/proj", false); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("retry before release: want ErrSessionConflict, got %v", err)
	}

	// Releasing the failed run must unblock the retry.
	svc.FailIndexing(ctx, "/proj", runID)
	if _, _, err := svc.BeginIndexing(ctx, "/proj", false); err != nil {
		t.Fatalf("BeginIndexing after FailIndexing: want success, got %v", err)
	}

	// The released run row must be marked failed (not left 'running').
	var status string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM index_runs WHERE id = ?`, runID,
	).Scan(&status); err != nil {
		t.Fatalf("query index_runs: %v", err)
	}
	if status != "failed" {
		t.Fatalf("index_runs status: want %q, got %q", "failed", status)
	}

	// Idempotent: a second release (or one for an unknown run) is a no-op and
	// must not disturb the now-active retry session.
	svc.FailIndexing(ctx, "/proj", runID)
	svc.FailIndexing(ctx, "/proj", "no-such-run")
	if _, _, err := svc.BeginIndexing(ctx, "/proj", false); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("retry session disturbed by idempotent FailIndexing: got %v", err)
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

// capturingEmbedder records every batch passed to EmbedTexts so tests can
// assert what the indexer actually sent to the embedder. Returned vectors
// are zero-valued (still unit length 8) — value content is never checked
// by callers of this helper.
type capturingEmbedder struct {
	dim   int
	calls [][]string
}

func (c *capturingEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	captured := append([]string(nil), texts...)
	c.calls = append(c.calls, captured)
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, c.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

// TestProcessFiles_EmbedTextFormat_LegacyByDefault verifies that without
// SetEmbedIncludePath, ProcessFiles preserves the historical "<chunk_type>:
// <content>" format that Python parity depends on.
func TestProcessFiles_EmbedTextFormat_LegacyByDefault(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	emb := &capturingEmbedder{dim: 8}
	svc := New(d, newStore(t), emb, nil)
	// Default: embedIncludePath is false.

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}
	goFile := "package main\n\nfunc Hello() {}\n"
	files := []FilePayload{{
		Path:        "/proj/cmd/hello/main.go",
		Content:     goFile,
		ContentHash: sha256hex(goFile),
		Language:    "go",
		Size:        len(goFile),
	}}
	if _, _, _, err := svc.ProcessFiles(ctx, "/proj", runID, files); err != nil {
		t.Fatalf("ProcessFiles: %v", err)
	}

	if len(emb.calls) == 0 {
		t.Fatal("embedder was never called")
	}
	first := emb.calls[0]
	if len(first) == 0 {
		t.Fatal("first batch was empty")
	}
	for _, txt := range first {
		// Legacy format must NOT contain a "File:" preamble.
		if containsString(txt, "File: cmd/hello") {
			t.Errorf("legacy format leaked path preamble:\n%s", txt)
		}
	}
}

// TestProcessFiles_EmbedTextFormat_PathPrefixWhenEnabled verifies that with
// SetEmbedIncludePath(true), every chunk text is wrapped with the path-aware
// preamble produced by embeddings.FormatChunkForEmbedding.
func TestProcessFiles_EmbedTextFormat_PathPrefixWhenEnabled(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	emb := &capturingEmbedder{dim: 8}
	svc := New(d, newStore(t), emb, nil)
	svc.SetEmbedIncludePath(true)

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}
	goFile := "package main\n\nfunc Hello() {}\n"
	files := []FilePayload{{
		Path:        "/proj/cmd/hello/main.go",
		Content:     goFile,
		ContentHash: sha256hex(goFile),
		Language:    "go",
		Size:        len(goFile),
	}}
	if _, _, _, err := svc.ProcessFiles(ctx, "/proj", runID, files); err != nil {
		t.Fatalf("ProcessFiles: %v", err)
	}

	if len(emb.calls) == 0 {
		t.Fatal("embedder was never called")
	}
	first := emb.calls[0]
	if len(first) == 0 {
		t.Fatal("first batch was empty")
	}
	hasPathPreamble := false
	for _, txt := range first {
		if containsString(txt, "File: cmd/hello/main.go") &&
			containsString(txt, "Language: go") {
			hasPathPreamble = true
			break
		}
	}
	if !hasPathPreamble {
		t.Errorf("expected at least one chunk text to carry 'File: cmd/hello/main.go' + 'Language: go'; sent texts:\n%v", first)
	}
}

func containsString(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
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

// TestFinishIndexing_FullSyncFlag verifies that a completed FULL run clears
// projects.full_sync_required (the format-staleness flag migration 18 sets),
// while an incremental run leaves it set — the flag is satisfied only by a
// full rebuild, regardless of where the run was triggered.
func TestFinishIndexing_FullSyncFlag(t *testing.T) {
	cases := []struct {
		name     string
		full     bool
		wantFlag int
	}{
		{"full run clears the flag", true, 0},
		{"incremental run keeps the flag", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			seedProject(t, d, "/proj")
			ctx := context.Background()

			// Flag the project as needing a full resync (as migration 18 does).
			if _, err := d.ExecContext(ctx,
				`UPDATE projects SET full_sync_required = 1, full_sync_reason = 'migrated' WHERE host_path = ?`,
				"/proj",
			); err != nil {
				t.Fatalf("set flag: %v", err)
			}

			vs := newStore(t)
			svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)

			runID, _, err := svc.BeginIndexing(ctx, "/proj", tc.full)
			if err != nil {
				t.Fatalf("BeginIndexing: %v", err)
			}
			goFile := "package main\nfunc X() {}\n"
			if _, _, _, err := svc.ProcessFiles(ctx, "/proj", runID, []FilePayload{
				{Path: "/proj/a.go", Content: goFile, ContentHash: sha256hex(goFile), Language: "go"},
			}); err != nil {
				t.Fatalf("ProcessFiles: %v", err)
			}
			if _, _, _, err := svc.FinishIndexing(ctx, "/proj", runID, nil, 1); err != nil {
				t.Fatalf("FinishIndexing: %v", err)
			}

			var flag int
			var reason sql.NullString
			if err := d.QueryRowContext(ctx,
				`SELECT full_sync_required, full_sync_reason FROM projects WHERE host_path = ?`, "/proj",
			).Scan(&flag, &reason); err != nil {
				t.Fatalf("select flag: %v", err)
			}
			if flag != tc.wantFlag {
				t.Errorf("full_sync_required = %d, want %d", flag, tc.wantFlag)
			}
			if tc.full && reason.Valid {
				t.Errorf("full_sync_reason = %q, want NULL after full run", reason.String)
			}
			if !tc.full && !reason.Valid {
				t.Error("full_sync_reason cleared by an incremental run, want it to persist")
			}
		})
	}
}

// TestFinishIndexing_CapturesEmbeddingModel ensures FinishIndexing writes the
// model identifier set via SetEmbeddingModel into projects.indexed_with_model.
// Empty model (default for tests that don't call the setter) keeps the column
// NULL so the dashboard treats those projects as "indexed before drift
// tracking existed" rather than as stale.
func TestFinishIndexing_CapturesEmbeddingModel(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, "/proj")

	ctx := context.Background()
	vs := newStore(t)
	svc := New(d, vs, &fakeEmbedder{dim: 8}, nil)
	svc.SetEmbeddingModel("test/model-v1")

	runID, _, err := svc.BeginIndexing(ctx, "/proj", false)
	if err != nil {
		t.Fatalf("BeginIndexing: %v", err)
	}
	goFile := "package main\nfunc X() {}\n"
	if _, _, _, err := svc.ProcessFiles(ctx, "/proj", runID, []FilePayload{
		{Path: "/proj/a.go", Content: goFile, ContentHash: sha256hex(goFile), Language: "go"},
	}); err != nil {
		t.Fatalf("ProcessFiles: %v", err)
	}
	if _, _, _, err := svc.FinishIndexing(ctx, "/proj", runID, nil, 1); err != nil {
		t.Fatalf("FinishIndexing: %v", err)
	}

	var model sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT indexed_with_model FROM projects WHERE host_path = ?`, "/proj",
	).Scan(&model); err != nil {
		t.Fatalf("select indexed_with_model: %v", err)
	}
	if !model.Valid || model.String != "test/model-v1" {
		t.Errorf("indexed_with_model = %+v, want test/model-v1", model)
	}

	// Round 2: another project, no setter call → column stays NULL.
	seedProject(t, d, "/proj2")
	svc2 := New(d, vs, &fakeEmbedder{dim: 8}, nil) // no SetEmbeddingModel
	runID2, _, err := svc2.BeginIndexing(ctx, "/proj2", false)
	if err != nil {
		t.Fatalf("BeginIndexing /proj2: %v", err)
	}
	if _, _, _, err := svc2.ProcessFiles(ctx, "/proj2", runID2, []FilePayload{
		{Path: "/proj2/b.go", Content: goFile, ContentHash: sha256hex(goFile), Language: "go"},
	}); err != nil {
		t.Fatalf("ProcessFiles /proj2: %v", err)
	}
	if _, _, _, err := svc2.FinishIndexing(ctx, "/proj2", runID2, nil, 1); err != nil {
		t.Fatalf("FinishIndexing /proj2: %v", err)
	}
	var model2 sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT indexed_with_model FROM projects WHERE host_path = ?`, "/proj2",
	).Scan(&model2); err != nil {
		t.Fatalf("select indexed_with_model /proj2: %v", err)
	}
	if model2.Valid {
		t.Errorf("indexed_with_model /proj2 = %q, want NULL", model2.String)
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
