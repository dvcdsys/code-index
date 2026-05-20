package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// markIndexed flips a project's status to 'indexed' so workspaceprojects.Link
// passes its precondition. Production code does this through the indexer's
// finish step (which also stamps last_indexed_at); tests don't run the
// indexer so we mimic both writes here.
//
// The timestamp must be RFC3339Nano because projectToOpenAPI parses
// last_indexed_at with time.RFC3339Nano — SQLite's `datetime('now')`
// emits `2006-01-02 15:04:05` which fails that parse and silently
// becomes NULL on the wire.
func markIndexed(t *testing.T, d *sql.DB, hostPath string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(
		`UPDATE projects SET status = 'indexed', last_indexed_at = ? WHERE host_path = ?`,
		now, hostPath,
	); err != nil {
		t.Fatalf("flip status to indexed for %s: %v", hostPath, err)
	}
}

func TestLinkProjectToWorkspace_RejectsNotIndexed(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/x/y",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add git_repo: %d (%s)", rr.Code, rr.Body.String())
	}
	hash := projects.HashPath("github.com/x/y@main")

	rr = doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/projects",
		map[string]any{"project_hash": hash})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("link before indexed: expected 422, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestLinkProjectToWorkspace_AfterIndexed(t *testing.T) {
	router, _, d := reposRouterDB(t)
	wsID := createWS(t, router, "platform")
	hostPath := "/Users/x/local-proj"
	rr := doJSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"host_path": hostPath,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d (%s)", rr.Code, rr.Body.String())
	}
	markIndexed(t, d, hostPath)
	hash := projects.HashPath(hostPath)

	rr = doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/projects",
		map[string]any{"project_hash": hash})
	if rr.Code != http.StatusCreated {
		t.Fatalf("link: %d (%s)", rr.Code, rr.Body.String())
	}

	rr = doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/projects", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rr.Code, rr.Body.String())
	}
	var list struct {
		Projects []map[string]any `json:"projects"`
		Total    int              `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if list.Total != 1 {
		t.Fatalf("expected 1 project in workspace, got %d", list.Total)
	}

	// Wire-contract check — the CLI client (cli/internal/client/workspace.go)
	// decodes this exact response into Project + WorkspaceProject + Wrapped
	// list. Anyone breaking these field names breaks the CLI silently
	// (which is how Fix #1 happened in the first place). Mirror the
	// CLI's struct shape inline and assert every field the CLI reads.
	type cliWire struct {
		Projects []struct {
			AddedAt time.Time `json:"added_at"`
			Project struct {
				PathHash      string     `json:"path_hash"`
				HostPath      string     `json:"host_path"`
				ContainerPath string     `json:"container_path"`
				Status        string     `json:"status"`
				LastIndexedAt *time.Time `json:"last_indexed_at"`
				Languages     []string   `json:"languages"`
			} `json:"project"`
		} `json:"projects"`
		Total int `json:"total"`
	}
	var wire cliWire
	if err := json.Unmarshal(rr.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode wire shape: %v", err)
	}
	if wire.Total != 1 || len(wire.Projects) != 1 {
		t.Fatalf("expected wire to surface 1 project; got total=%d len=%d", wire.Total, len(wire.Projects))
	}
	got := wire.Projects[0]
	if got.AddedAt.IsZero() {
		t.Errorf("expected added_at to be a non-zero timestamp")
	}
	if got.Project.PathHash != hash {
		t.Errorf("path_hash: want %q, got %q", hash, got.Project.PathHash)
	}
	if got.Project.HostPath != hostPath {
		t.Errorf("host_path: want %q, got %q", hostPath, got.Project.HostPath)
	}
	if got.Project.Status != "indexed" {
		t.Errorf("status: want \"indexed\", got %q", got.Project.Status)
	}
	if got.Project.LastIndexedAt == nil {
		t.Errorf("last_indexed_at should be populated for an indexed project")
	}
	if got.Project.Languages == nil {
		t.Errorf("languages should be [], not null — CLI iterates it without nil-check")
	}
}

// TestListWorkspaceProjects_Empty pins the empty-membership response:
// the handler MUST emit `"projects": []` (not `null`), so the CLI's
// `for _, wp := range resp.Projects` loop is safe. A future refactor
// that drops the `make([]map[string]any, 0, …)` initialisation would
// regress to `null` and this test catches it at server CI.
func TestListWorkspaceProjects_Empty(t *testing.T) {
	router, _, _ := reposRouterDB(t)
	wsID := createWS(t, router, "platform")

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/projects", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// `"projects":[]` (no whitespace; encoding/json produces compact JSON).
	// Negative form `"projects":null` is the regression we're guarding.
	if !strings.Contains(body, `"projects":[]`) {
		t.Errorf("expected `\"projects\":[]` in body, got: %s", body)
	}
	var list struct {
		Projects []map[string]any `json:"projects"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 0 {
		t.Errorf("expected total=0, got %d", list.Total)
	}
	if list.Projects == nil {
		t.Errorf("projects should be empty slice, not nil (CLI iterates it)")
	}
}

// TestListWorkspaceProjects_WorkspaceMissing locks the "unknown id → 404"
// contract for the new endpoint. The CLI surfaces this as a "workspace
// %q not found" error to the user.
func TestListWorkspaceProjects_WorkspaceMissing(t *testing.T) {
	router, _, _ := reposRouterDB(t)
	bogusID := "ws_does_not_exist_01J5"

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+bogusID+"/projects", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "workspace not found") {
		t.Errorf("expected 404 body to mention 'workspace not found', got: %s", rr.Body.String())
	}
}

// TestListWorkspaceProjects_ServiceMissing — defensive: when the
// workspaces services are not wired (e.g. a partial test Deps), the
// handler must short-circuit with 503 *before* touching DB. CLI
// integration relies on this so `cix ws <name> list` against a
// half-wired server prints a useful "not configured" hint, not a
// generic failure.
func TestListWorkspaceProjects_ServiceMissing(t *testing.T) {
	router := workspaceRouter(t, false)
	// Any path id will do — 503 must come before the workspace lookup.
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/ws_any/projects", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with services unwired, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "workspaces service is not configured") {
		t.Errorf("expected 503 body to mention 'workspaces service is not configured', got: %s", rr.Body.String())
	}
}


// TestLink_Duplicate confirms the workspace_projects PRIMARY KEY
// catches a re-link as 409. Same project, same workspace, twice.
func TestLink_Duplicate(t *testing.T) {
	router, _, d := reposRouterDB(t)
	wsID := createWS(t, router, "platform")
	hostPath := "/Users/x/p"
	_ = doJSON(t, router, http.MethodPost, "/api/v1/projects",
		map[string]any{"host_path": hostPath})
	markIndexed(t, d, hostPath)
	hash := projects.HashPath(hostPath)

	body := map[string]any{"project_hash": hash}
	if rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/projects", body); rr.Code != http.StatusCreated {
		t.Fatalf("first link: %d", rr.Code)
	}
	if rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/projects", body); rr.Code != http.StatusConflict {
		t.Fatalf("second link should 409, got %d", rr.Code)
	}
}

// TestUnlinkProject removes a membership without touching the project.
func TestUnlinkProject(t *testing.T) {
	router, _, d := reposRouterDB(t)
	wsID := createWS(t, router, "platform")
	hostPath := "/Users/x/local-proj"
	_ = doJSON(t, router, http.MethodPost, "/api/v1/projects",
		map[string]any{"host_path": hostPath})
	markIndexed(t, d, hostPath)
	hash := projects.HashPath(hostPath)

	if rr := doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/projects",
		map[string]any{"project_hash": hash}); rr.Code != http.StatusCreated {
		t.Fatalf("link: %d (%s)", rr.Code, rr.Body.String())
	}

	rr := doJSON(t, router, http.MethodDelete,
		"/api/v1/workspaces/"+wsID+"/projects/"+hash, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unlink: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, router, http.MethodDelete,
		"/api/v1/workspaces/"+wsID+"/projects/"+hash, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("repeat unlink: expected 404, got %d", rr.Code)
	}
	// Project still exists.
	rr = doJSON(t, router, http.MethodGet, "/api/v1/projects/"+hash, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("project should survive unlink, got %d", rr.Code)
	}
}
