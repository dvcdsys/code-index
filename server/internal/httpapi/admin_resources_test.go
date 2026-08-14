package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/maintenance"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// Every resource endpoint is admin-only. The dashboard hides the section for
// non-admins, but that is decoration — the gate is here.
func TestResourceEndpoints_AdminOnly(t *testing.T) {
	f := newAdminFixture(t)
	viewer := viewerCookie(t, f)

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/admin/resources", nil},
		{http.MethodPost, "/api/v1/admin/resources/analyze", nil},
		{http.MethodPost, "/api/v1/admin/resources/clean", map[string]any{
			"analysis_id": "x", "categories": []string{"orphan_collections"},
		}},
	}
	for _, c := range cases {
		rr, body := doReq(t, f.authTestFixture, viewer, c.method, c.path, c.body)
		if rr.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403 (body: %s)", c.method, c.path, rr.Code, body)
		}
	}
}

func TestResourceEndpoints_RejectAnonymous(t *testing.T) {
	f := newAdminFixture(t)
	// Each route with the method it actually serves. Hitting a POST-only path
	// with GET would return 405 before auth ever runs, which proves nothing
	// about the gate.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/resources"},
		{http.MethodPost, "/api/v1/admin/resources/analyze"},
		{http.MethodPost, "/api/v1/admin/resources/clean"},
	} {
		rr, body := doReq(t, f.authTestFixture, "", c.method, c.path, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401 (body: %s)", c.method, c.path, rr.Code, body)
		}
	}
}

func TestGetResourceUsage_ReportsMemoryAndDisks(t *testing.T) {
	f := newResourceFixture(t)
	rr, body := doReq(t, f.authTestFixture, adminCookie(t, f.adminFixture), http.MethodGet, "/api/v1/admin/resources", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("usage = %d, want 200 (body: %s)", rr.Code, body)
	}
	var got maintenance.Usage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	if got.Memory.HeapAllocBytes <= 0 {
		t.Errorf("heap_alloc_bytes = %d, want > 0", got.Memory.HeapAllocBytes)
	}
	if got.Memory.NumGoroutine <= 0 {
		t.Errorf("num_goroutine = %d, want > 0", got.Memory.NumGoroutine)
	}
	if got.VectorStore.Documents != 2 {
		t.Errorf("vector documents = %d, want 2", got.VectorStore.Documents)
	}
	seen := map[string]bool{}
	for _, d := range got.Disks {
		seen[d.ID] = true
	}
	for _, want := range []string{maintenance.DiskSQLite, maintenance.DiskChroma, maintenance.DiskRepos} {
		if !seen[want] {
			t.Errorf("no %q entry in disks: %+v", want, got.Disks)
		}
	}
}

// The whole analyze → confirm → clean sequence, over HTTP, including the
// re-validation that protects a project which came back in between.
func TestAnalyzeThenClean(t *testing.T) {
	f := newResourceFixture(t)
	cookie := adminCookie(t, f.adminFixture)

	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/analyze", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("analyze = %d, want 200 (body: %s)", rr.Code, body)
	}
	var analysis maintenance.Analysis
	if err := json.Unmarshal(body, &analysis); err != nil {
		t.Fatalf("decode analysis: %v", err)
	}
	if analysis.ID == "" {
		t.Fatal("analysis has no id")
	}
	var orphans maintenance.Category
	for _, c := range analysis.Categories {
		if c.ID == maintenance.CatOrphanCollections {
			orphans = c
		}
	}
	if orphans.ItemCount != 1 {
		t.Fatalf("orphan collections = %d, want 1 (categories: %+v)", orphans.ItemCount, analysis.Categories)
	}

	rr, body = doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/clean", map[string]any{
		"analysis_id": analysis.ID,
		"categories":  []string{string(maintenance.CatOrphanCollections)},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("clean = %d, want 200 (body: %s)", rr.Code, body)
	}
	var res maintenance.CleanResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode clean result: %v", err)
	}
	if res.Categories[0].DeletedCount != 1 {
		t.Errorf("deleted = %d, want 1 (result: %+v)", res.Categories[0].DeletedCount, res.Categories[0])
	}
	if n := f.store.Count("/live/project"); n != 1 {
		t.Errorf("the live project lost documents (%d left, want 1)", n)
	}
}

func TestCleanResources_StaleAnalysisIs409(t *testing.T) {
	f := newResourceFixture(t)
	cookie := adminCookie(t, f.adminFixture)

	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/clean", map[string]any{
		"analysis_id": "never-issued",
		"categories":  []string{string(maintenance.CatOrphanCollections)},
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("clean with unknown analysis = %d, want 409 (body: %s)", rr.Code, body)
	}
}

func TestCleanResources_BadSelectionIs422(t *testing.T) {
	f := newResourceFixture(t)
	cookie := adminCookie(t, f.adminFixture)

	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/analyze", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("analyze = %d (body: %s)", rr.Code, body)
	}
	var analysis maintenance.Analysis
	_ = json.Unmarshal(body, &analysis)

	for _, cats := range [][]string{{}, {"made_up"}} {
		rr, body := doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/clean", map[string]any{
			"analysis_id": analysis.ID,
			"categories":  cats,
		})
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("clean with categories %v = %d, want 422 (body: %s)", cats, rr.Code, body)
		}
	}
}

// A router built without config (the plain admin fixture is one) must say so
// rather than panicking or reporting zeroes as if they were real.
func TestResourceUsage_WithoutConfigIs503(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.Cfg = nil
	f.Router = NewRouter(f.Deps)

	rr, body := doReq(t, f.authTestFixture, adminCookie(t, f), http.MethodGet, "/api/v1/admin/resources", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("usage without config = %d, want 503 (body: %s)", rr.Code, body)
	}
}

// Deleting a project must take its off-database residue with it, or the
// backlog this whole feature exists to clear starts refilling immediately.
func TestDeleteProject_RemovesCollectionAndCheckout(t *testing.T) {
	f := newResourceFixture(t)
	cookie := adminCookie(t, f.adminFixture)

	hash := projects.HashPath("/live/project")
	clone := repocloner.LocalDirFor(f.Deps.DataDir, hash)
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	if _, ok := f.store.CollectionSizeBytes("/live/project"); !ok {
		t.Fatal("precondition: the project has no vector collection")
	}

	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodDelete, "/api/v1/projects/"+hash, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 (body: %s)", rr.Code, body)
	}
	if _, ok := f.store.CollectionSizeBytes("/live/project"); ok {
		t.Error("the vector collection survived the delete")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Errorf("cloned checkout survived the delete (err=%v)", err)
	}
	if n := f.store.Count("/live/project"); n != 0 {
		t.Errorf("%d documents still stored after delete, want 0", n)
	}

	// And the analysis should now find nothing — the delete cleaned up after
	// itself rather than leaving work for the Resources screen.
	rr, body = doReq(t, f.authTestFixture, cookie, http.MethodPost, "/api/v1/admin/resources/analyze", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("analyze = %d (body: %s)", rr.Code, body)
	}
	var analysis maintenance.Analysis
	if err := json.Unmarshal(body, &analysis); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, c := range analysis.Categories {
		if c.ID == maintenance.CatOrphanRepos && c.ItemCount != 0 {
			t.Errorf("delete left %d orphaned checkouts behind", c.ItemCount)
		}
	}
}

// --- fixture ---

type resourceFixture struct {
	*adminFixture
	store *vectorstore.Store
}

// newResourceFixture builds an admin router with a real vector store holding
// one live and one orphaned collection — the exact shape this feature exists
// to find.
func newResourceFixture(t *testing.T) *resourceFixture {
	t.Helper()
	f := newAdminFixture(t)

	dataDir := t.TempDir()
	chroma := filepath.Join(dataDir, "chroma")
	store, err := vectorstore.Open(filepath.Join(chroma, "ollama", "test-model"))
	if err != nil {
		t.Fatalf("open vector store: %v", err)
	}
	ctx := context.Background()
	for _, p := range []string{"/live/project", "/dead/project"} {
		if err := store.UpsertChunks(ctx, p,
			[]vectorstore.Chunk{{Content: "package main", FilePath: "main.go", StartLine: 1, EndLine: 2}},
			[][]float32{{1, 0, 0}},
		); err != nil {
			t.Fatalf("upsert %s: %v", p, err)
		}
	}
	if _, err := projects.Create(ctx, f.Deps.DB, projects.CreateRequest{HostPath: "/live/project"}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	f.Deps.Cfg = &config.Config{
		SQLitePath:        filepath.Join(dataDir, "cix.db"),
		ChromaPersistDir:  chroma,
		WorkspacesDataDir: dataDir,
		GGUFCacheDir:      filepath.Join(dataDir, "models"),
	}
	f.Deps.VectorStore = store
	f.Deps.DataDir = dataDir
	f.Router = NewRouter(f.Deps)

	return &resourceFixture{adminFixture: f, store: store}
}
