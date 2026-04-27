package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fakeEmbedder duplicated locally so tests stay inside httpapi_test.
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim && j < len(t); j++ {
			v[j] = float32(t[j]) / 255.0
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	v := make([]float32, f.dim)
	for j := 0; j < f.dim && j < len(q); j++ {
		v[j] = float32(q[j]) / 255.0
	}
	return v, nil
}

// Ready satisfies the EmbeddingsQuerier interface so a fake embedder can
// stand in for *embeddings.Service in router tests. Always healthy.
func (f *fakeEmbedder) Ready(_ context.Context) error { return nil }

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newIndexerTestDeps wires a full Phase 5 Deps (DB + vectorstore + indexer +
// fake embedder), with a pre-created project. Returns deps and the project hash.
func newIndexerTestDeps(t *testing.T, projectPath string) (Deps, string) {
	t.Helper()
	d := newTestDeps(t)

	// Create the project via the existing package API so row layout matches.
	_, err := projects.Create(context.Background(), d.DB, projects.CreateRequest{HostPath: projectPath})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	vs, err := vectorstore.Open(filepath.Join(t.TempDir(), "chroma"))
	if err != nil {
		t.Fatalf("vectorstore open: %v", err)
	}
	emb := &fakeEmbedder{dim: 16}
	d.VectorStore = vs
	d.EmbeddingSvc = emb
	d.Indexer = indexer.New(d.DB, vs, emb, nil)

	return d, projects.HashPath(projectPath)
}

// ---------------------------------------------------------------------------

func TestIndexBegin_HTTP(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{"full": false})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp indexBeginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.RunID == "" {
		t.Error("run_id empty")
	}
	if resp.StoredHashes == nil {
		t.Error("stored_hashes must be {} not null")
	}
}

func TestIndexFiles_HTTP_Success(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	// begin
	beginW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	var begin indexBeginResponse
	_ = json.Unmarshal(beginW.Body.Bytes(), &begin)

	// files
	content := "package main\nfunc F() int { return 1 }\n"
	filesBody := map[string]any{
		"run_id": begin.RunID,
		"files": []map[string]any{
			{
				"path":         "/proj/main.go",
				"content":      content,
				"content_hash": shaHex(content),
				"language":     "go",
				"size":         len(content),
			},
		},
	}
	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/files", filesBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp indexFilesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.FilesAccepted != 1 {
		t.Errorf("files_accepted=%d", resp.FilesAccepted)
	}
	if resp.ChunksCreated == 0 {
		t.Errorf("chunks_created=0")
	}
}

func TestIndexFiles_HTTP_InvalidRunID(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/files", map[string]any{
		"run_id": "bogus",
		"files":  []any{},
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestIndexFinish_HTTP(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	beginW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	var begin indexBeginResponse
	_ = json.Unmarshal(beginW.Body.Bytes(), &begin)

	// finish with no files processed
	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/finish", map[string]any{
		"run_id":                 begin.RunID,
		"deleted_paths":          []string{},
		"total_files_discovered": 0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp indexFinishResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "completed" {
		t.Errorf("status=%q", resp.Status)
	}
}

func TestIndexStatus_HTTP_Idle(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects/"+hash+"/index/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp indexProgressResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "idle" {
		t.Errorf("status=%q, want idle", resp.Status)
	}
}

// TestIndexCancel_HTTP_NoSession verifies the endpoint is idempotent: the
// stale-session guard in the CLI calls /cancel unconditionally at startup,
// even when nothing is active. Must return 200 + cancelled:false, not 404.
func TestIndexCancel_HTTP_NoSession(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/cancel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp indexCancelResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Cancelled {
		t.Errorf("cancelled=true with no active session")
	}
}

// TestIndexCancel_HTTP_ActiveSession exercises the main path: an active
// session gets torn down, and a subsequent /begin succeeds (where without
// cancel it would return 409 Conflict).
func TestIndexCancel_HTTP_ActiveSession(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	// Start a session.
	beginW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	if beginW.Code != http.StatusOK {
		t.Fatalf("begin: status=%d body=%s", beginW.Code, beginW.Body.String())
	}

	// Cancel it.
	cancelW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/cancel", nil)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel: status=%d body=%s", cancelW.Code, cancelW.Body.String())
	}
	var resp indexCancelResponse
	_ = json.Unmarshal(cancelW.Body.Bytes(), &resp)
	if !resp.Cancelled {
		t.Errorf("cancelled=false, want true")
	}

	// A fresh begin must now succeed (would be 409 without cancel).
	begin2W := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	if begin2W.Code != http.StatusOK {
		t.Fatalf("second begin after cancel: status=%d body=%s", begin2W.Code, begin2W.Body.String())
	}
}

func TestSemanticSearch_HTTP(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	// Index a file first.
	beginW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	var begin indexBeginResponse
	_ = json.Unmarshal(beginW.Body.Bytes(), &begin)

	content := "package main\nfunc HandleRequest() {}\n"
	doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/files", map[string]any{
		"run_id": begin.RunID,
		"files": []map[string]any{
			{"path": "/proj/main.go", "content": content, "content_hash": shaHex(content), "language": "go"},
		},
	})
	doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/finish", map[string]any{
		"run_id": begin.RunID,
	})

	// Now search.
	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search", map[string]any{
		"query":     "HandleRequest",
		"limit":     10,
		"min_score": 0.0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp searchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total == 0 {
		t.Error("expected at least one result")
	}
}

// TestSemanticSearch_NestedMarkdownMerge indexes a markdown file with H1
// containing two H2 sections, all containing a unique token. The chunker
// emits 3 overlapping `section` chunks (1 outer + 2 inner). After
// mergeOverlappingHits the outer section absorbs both inner ones —
// observable as ONE result with NestedHits populated, instead of three
// near-duplicates fighting for the user's --limit budget.
func TestSemanticSearch_NestedMarkdownMerge(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj-md")
	router := NewRouter(d)

	beginW := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/begin", map[string]any{})
	var begin indexBeginResponse
	_ = json.Unmarshal(beginW.Body.Bytes(), &begin)

	content := "# Setup zlork\n\nIntro about zlork.\n\n## Local zlork dev\n\n" +
		"Steps for zlork.\n\n## Remote zlork\n\nMore zlork.\n"
	doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/files", map[string]any{
		"run_id": begin.RunID,
		"files": []map[string]any{
			{"path": "/proj-md/README.md", "content": content, "content_hash": shaHex(content), "language": "markdown"},
		},
	})
	doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/index/finish", map[string]any{
		"run_id": begin.RunID,
	})

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search", map[string]any{
		"query":     "zlork",
		"limit":     10,
		"min_score": 0.0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp searchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Find the README.md file group and verify the merge happened.
	var group *fileGroupResult
	for i := range resp.Results {
		if resp.Results[i].FilePath == "/proj-md/README.md" {
			group = &resp.Results[i]
			break
		}
	}
	if group == nil {
		t.Fatalf("expected a file group for README.md, got results: %+v", resp.Results)
	}
	// After merge, only ONE match should remain in this file (the outer
	// H1 section absorbing the two H2s as nested hits).
	if len(group.Matches) != 1 {
		t.Fatalf("expected 1 match in README.md after merge, got %d: %+v", len(group.Matches), group.Matches)
	}
	outer := group.Matches[0]
	if outer.StartLine != 1 {
		t.Errorf("outer match should start at line 1, got %d", outer.StartLine)
	}
	if len(outer.NestedHits) == 0 {
		t.Errorf("outer match should record absorbed nested hits, got NestedHits=%v", outer.NestedHits)
	}
}

func TestSemanticSearch_HTTP_MissingQuery(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search", map[string]any{})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status=%d", w.Code)
	}
}

func TestSemanticSearch_HTTP_NoEmbeddings(t *testing.T) {
	d, hash := newIndexerTestDeps(t, "/proj")
	d.EmbeddingSvc = nil
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search", map[string]any{"query": "x"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d", w.Code)
	}
}
