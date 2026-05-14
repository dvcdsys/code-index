package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// markIndexed flips a project's status to 'indexed' so workspaceprojects.Link
// passes its precondition. Production code does this through the indexer's
// finish step; tests don't run the indexer and write the row directly.
func markIndexed(t *testing.T, d *sql.DB, hostPath string) {
	t.Helper()
	if _, err := d.Exec(
		`UPDATE projects SET status = 'indexed' WHERE host_path = ?`, hostPath,
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
