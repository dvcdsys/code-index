package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// stubEmbedder is the minimum EmbeddingsQuerier surface workspace search
// touches in unit tests — no llama-server, no real model. Returns a
// constant vector so chromem queries don't crash on dim mismatch.
type stubEmbedder struct{}

func (stubEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	// Tiny vector — chromem doesn't care about dim for an empty
	// collection, and stage 1 returns nil result anyway in the
	// "communities_not_built" branch.
	return []float32{0.1, 0.2, 0.3}, nil
}
func (stubEmbedder) Ready(_ context.Context) error { return nil }

// newRouterWithSearch wires the minimum surface needed by workspace
// search: workspaces, workspace_repos, jobs, vectorstore (empty),
// embedder (stub). The vectorstore is opened against an empty
// temp-directory so the centroid collection lookup returns nil
// (the "communities_not_built" branch).
func newRouterWithSearch(t *testing.T, d *sql.DB) http.Handler {
	t.Helper()
	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	vs, err := vectorstore.Open(filepath.Join(t.TempDir(), "chroma"))
	if err != nil {
		t.Fatalf("vectorstore: %v", err)
	}
	return NewRouter(Deps{
		DB:                d,
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		WorkspacesEnabled: true,
		Workspaces:        workspaces.New(d),
		GithubTokens:      githubtokens.New(d, sec),
		WorkspaceRepos:    workspacerepos.New(d),
		Jobs:              jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour}),
		VectorStore:       vs,
		EmbeddingSvc:      stubEmbedder{},
	})
}

func TestWorkspaceSearch_NoCommunitiesBuilt(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := newRouterWithSearch(t, d)
	wsID := createWS(t, router, "platform")

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=login%20flow", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status      string `json:"status"`
		Communities []any  `json:"communities"`
		Chunks      []any  `json:"chunks"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "communities_not_built" {
		t.Fatalf("expected communities_not_built status, got %q", resp.Status)
	}
	if len(resp.Communities) != 0 || len(resp.Chunks) != 0 {
		t.Fatalf("expected empty arrays, got %+v", resp)
	}
}

func TestWorkspaceSearch_MissingQueryReturns422(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := newRouterWithSearch(t, d)
	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on missing q, got %d", rr.Code)
	}
}

func TestWorkspaceSearch_NotFoundWorkspace(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := newRouterWithSearch(t, d)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/no-such-id/search?q=anything", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestWorkspaceSearch_Disabled(t *testing.T) {
	router := workspaceRouter(t, false)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/any/search?q=x", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}
