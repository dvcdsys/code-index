package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

func newTestDeps(t *testing.T) Deps {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	// AuthDisabled=true matches the historical behaviour of these tests
	// (no Authorization header sent). Production wiring sets a real key.
	return Deps{DB: d, AuthDisabled: true}
}

func doRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestCreateProject_Success(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"host_path": "/home/user/repo",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. body: %s", w.Code, w.Body.String())
	}

	var resp projectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.HostPath != "/home/user/repo" {
		t.Errorf("host_path = %q", resp.HostPath)
	}
	if resp.Status != "created" {
		t.Errorf("status = %q", resp.Status)
	}
	if len(resp.Languages) == 0 {
		// Languages starts as []; ensure it's an array not null.
		if resp.Languages == nil {
			t.Error("languages must be [] not null")
		}
	}
}

func TestCreateProject_Conflict(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	body := map[string]any{"host_path": "/home/user/repo"}
	doRequest(t, router, http.MethodPost, "/api/v1/projects", body)
	w := doRequest(t, router, http.MethodPost, "/api/v1/projects", body)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

func TestCreateProject_MissingHostPath(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
}

func TestListProjects(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	for _, path := range []string{"/a", "/b"} {
		doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": path})
	}

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp projectListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
}

func TestGetProject_ByHash(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/myproject"})
	hash := projects.HashPath("/myproject")

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects/"+hash, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp projectResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HostPath != "/myproject" {
		t.Errorf("host_path = %q", resp.HostPath)
	}
}

func TestGetProject_NotFound(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects/deadbeef00000000", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPatchProject(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	w := doRequest(t, router, http.MethodPatch, "/api/v1/projects/"+hash, map[string]any{
		"settings": map[string]any{
			"exclude_patterns": []string{"vendor"},
			"max_file_size":    1024,
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp projectResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Settings.ExcludePatterns) != 1 || resp.Settings.ExcludePatterns[0] != "vendor" {
		t.Errorf("settings.exclude_patterns = %v", resp.Settings.ExcludePatterns)
	}
}

func TestDeleteProject(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	w := doRequest(t, router, http.MethodDelete, "/api/v1/projects/"+hash, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	w = doRequest(t, router, http.MethodGet, "/api/v1/projects/"+hash, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w.Code)
	}
}

// DELETE /projects/{path} must not only drop the collection but hand its pages
// back to the filesystem. The reclaim lives in the HTTP wiring
// (projectArtifacts), not inside DeleteCollectionByName — it moved out of the
// store when maintenance.Clean went batched, and this path silently lost it in
// that refactor once already. Behavioural pin: the logical database size must
// shrink and the freelist must end empty, not merely grow.
func TestDeleteProject_ReclaimsVectorStorePages(t *testing.T) {
	d := newTestDeps(t)
	vs, err := vectorstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open vector store: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	d.VectorStore = vs
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/keeper"})
	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/doomed"})

	seed := func(project string, n int) {
		t.Helper()
		chunks := make([]vectorstore.Chunk, n)
		embs := make([][]float32, n)
		for i := range chunks {
			chunks[i] = vectorstore.Chunk{
				Content:  strings.Repeat("x", 512),
				FilePath: fmt.Sprintf("f%d.go", i), StartLine: 1, EndLine: 2,
			}
			embs[i] = []float32{1, 0, 0, 0}
		}
		if err := vs.UpsertChunks(context.Background(), project, chunks, embs); err != nil {
			t.Fatalf("seed %s: %v", project, err)
		}
	}
	seed("/keeper", 50)
	seed("/doomed", 2000) // inserted last, so its pages sit at the file's tail

	stat := func() (pages, free int64) {
		t.Helper()
		raw, err := sql.Open("sqlite", "file:"+vs.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if err := raw.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if err := raw.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
			t.Fatal(err)
		}
		return pages, free
	}
	pagesBefore, _ := stat()

	w := doRequest(t, router, http.MethodDelete, "/api/v1/projects/"+projects.HashPath("/doomed"), nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body: %s", w.Code, w.Body.String())
	}

	pagesAfter, free := stat()
	if free != 0 {
		t.Errorf("freelist_count = %d after a project delete, want 0 — ReclaimFreePages is not wired into the delete path", free)
	}
	if pagesAfter >= pagesBefore {
		t.Errorf("page_count %d -> %d: the deleted project's pages were not returned", pagesBefore, pagesAfter)
	}
	if got := vs.Count("/keeper"); got != 50 {
		t.Errorf("Count(/keeper) = %d after deleting the other project, want 50", got)
	}
}

func TestSymbolSearch(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	// Insert a symbol directly.
	_, _ = d.DB.ExecContext(context.Background(),
		`INSERT INTO symbols (id, project_path, name, kind, file_path, line, end_line, language)
		 VALUES ('id1', '/proj', 'MyFunc', 'function', '/proj/main.go', 5, 10, 'go')`)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search/symbols", map[string]any{
		"query": "MyFunc",
		"limit": 10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp symbolSearchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
	if resp.Results[0].Name != "MyFunc" {
		t.Errorf("name = %q", resp.Results[0].Name)
	}
}

func TestDefinitionSearch(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	_, _ = d.DB.ExecContext(context.Background(),
		`INSERT INTO symbols (id, project_path, name, kind, file_path, line, end_line, language)
		 VALUES ('id1', '/proj', 'Handler', 'function', '/proj/main.go', 1, 5, 'go')`)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search/definitions", map[string]any{
		"symbol": "Handler",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp definitionResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestReferenceSearch(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	_, _ = d.DB.ExecContext(context.Background(),
		`INSERT INTO refs (project_path, name, file_path, line, col, language)
		 VALUES ('/proj', 'MyFunc', '/proj/a.go', 10, 5, 'go')`)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search/references", map[string]any{
		"symbol": "MyFunc",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp referenceResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
	if resp.Results[0].ChunkType != "reference" {
		t.Errorf("chunk_type = %q, want reference", resp.Results[0].ChunkType)
	}
}

func TestFileSearch(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	_, _ = d.DB.ExecContext(context.Background(),
		`INSERT INTO file_hashes (project_path, file_path, content_hash, indexed_at)
		 VALUES ('/proj', '/proj/internal/handler.go', 'abc', '2024-01-01')`)

	w := doRequest(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/search/files", map[string]any{
		"query": "handler",
		"limit": 10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp fileSearchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestProjectSummary(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	doRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]any{"host_path": "/proj"})
	hash := projects.HashPath("/proj")

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects/"+hash+"/summary", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp projectSummaryResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HostPath != "/proj" {
		t.Errorf("host_path = %q", resp.HostPath)
	}
	if resp.TopDirectories == nil {
		t.Error("top_directories must not be null")
	}
	if resp.RecentSymbols == nil {
		t.Error("recent_symbols must not be null")
	}
}

func TestJSONContentType(t *testing.T) {
	d := newTestDeps(t)
	router := NewRouter(d)

	w := doRequest(t, router, http.MethodGet, "/api/v1/projects", nil)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
