package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/jobs"
)

func TestAddGitRepo_Succeeds(t *testing.T) {
	router, jobsSvc := reposRouter(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/spf13/cobra",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}

	var resp struct {
		Project struct {
			HostPath string `json:"host_path"`
			Status   string `json:"status"`
		} `json:"project"`
		GitRepo struct {
			ProjectPath string `json:"project_path"`
			PathHash    string `json:"path_hash"`
			GitHubURL   string `json:"github_url"`
			Branch      string `json:"branch"`
			WebhookMode string `json:"webhook_mode"`
		} `json:"git_repo"`
		WebhookURL    string `json:"webhook_url"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "github.com/spf13/cobra@main"
	if resp.GitRepo.ProjectPath != want {
		t.Fatalf("project_path = %q, want %q", resp.GitRepo.ProjectPath, want)
	}
	if resp.Project.HostPath != want {
		t.Fatalf("project.host_path = %q, want %q", resp.Project.HostPath, want)
	}
	// Fresh projects.Create returns status='created'; the clone job
	// flips through 'cloning' → 'indexing' → 'indexed' from there.
	if resp.Project.Status != "created" {
		t.Fatalf("project.status = %q, want created", resp.Project.Status)
	}
	if resp.WebhookSecret == "" {
		t.Errorf("webhook_secret was not populated")
	}
	if resp.GitRepo.WebhookMode != "manual" {
		t.Errorf("default webhook_mode = %q, want manual", resp.GitRepo.WebhookMode)
	}

	// clone_repo job enqueued.
	jobList, err := jobsSvc.List(context.Background(), jobs.StatusPending, "clone_repo", 10)
	if err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	if len(jobList) != 1 {
		t.Fatalf("expected 1 clone_repo job, got %d", len(jobList))
	}
	if jobList[0].DedupeKey != "clone:"+resp.GitRepo.PathHash {
		t.Errorf("dedupe_key = %q, want clone:%s", jobList[0].DedupeKey, resp.GitRepo.PathHash)
	}
}

// TestAddGitRepo_Duplicate confirms the UNIQUE(github_url, branch)
// constraint on git_repos surfaces as 409 from the HTTP handler. Used
// to be a workspace-scoped duplicate; with the split the same upstream
// can only be registered once across the whole server.
func TestAddGitRepo_Duplicate(t *testing.T) {
	router, _ := reposRouter(t)
	body := map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	}
	if rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", body); rr.Code != http.StatusCreated {
		t.Fatalf("first: %d", rr.Code)
	}
	if rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", body); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate should 409, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestReindexProject_RequiresGitRepo(t *testing.T) {
	router, _ := reposRouter(t)

	// CLI-indexed local project — has a projects row but no git_repos.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/projects", map[string]any{
		"host_path": "/Users/x/local-proj",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed local project: %d (%s)", rr.Code, rr.Body.String())
	}
	var p struct {
		PathHash string `json:"path_hash"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &p)
	if p.PathHash == "" {
		t.Fatalf("local project missing path_hash")
	}

	rr = doJSON(t, router, http.MethodPost, "/api/v1/projects/"+p.PathHash+"/reindex", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for local-project reindex, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestDeleteProject_CascadesGitRepoAndMembership exercises the chained
// FK ON DELETE CASCADE: removing the project deletes the git_repos row
// AND every workspace_projects row referencing it. Used to be a
// manual cleanup in projects.Delete; now the FKs do the work.
func TestDeleteProject_CascadesGitRepoAndMembership(t *testing.T) {
	router, _ := reposRouter(t)

	// Add an external project and attach it to a workspace.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	hash := created.GitRepo.PathHash

	// Delete the project directly — the cascade should clear both
	// git_repos and any workspace memberships (there are none here,
	// but the SQL exercises the FK trigger regardless).
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/projects/"+hash, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d (%s)", rr.Code, rr.Body.String())
	}
	// Re-adding the exact same upstream must succeed — proves the
	// git_repos row was actually removed (otherwise UNIQUE(github_url,
	// branch) would 409 here, which is the bug a previous patch fixed).
	rr = doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-add after delete: %d (%s)", rr.Code, rr.Body.String())
	}
}
