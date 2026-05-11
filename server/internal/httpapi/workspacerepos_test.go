package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// reposRouter spins up a router with the full workspaces+repos surface
// wired against an in-memory DB. Auth is disabled — the focus here is
// the persistence + enqueue paths.
//
// We deliberately do NOT start the jobs worker pool: we only assert the
// job row landed in the right state. End-to-end clone+index runs against
// real git remotes and the embeddings sidecar — out of scope for unit
// tests.
func reposRouter(t *testing.T) (http.Handler, *jobs.Service) {
	t.Helper()
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	wsSvc := workspaces.New(d)
	ghSvc := githubtokens.New(d, sec)
	wrSvc := workspacerepos.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour}) // never poll in tests

	router := NewRouter(Deps{
		DB:                d,
		ServerVersion:     "test",
		APIVersion:        "v1",
		Backend:           "go",
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		WorkspacesEnabled: true,
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		WorkspaceRepos:    wrSvc,
		Jobs:              jobsSvc,
		PublicBaseURL:     "https://cix.example.test",
	})
	return router, jobsSvc
}

func createWS(t *testing.T, router http.Handler, name string) string {
	t.Helper()
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces", map[string]any{
		"name": name,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create workspace: %d (%s)", rr.Code, rr.Body.String())
	}
	var got workspacePayload
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return got.ID
}

func TestRepos_AddEnqueuesCloneJob(t *testing.T) {
	router, jobsSvc := reposRouter(t)
	wsID := createWS(t, router, "platform")

	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": "https://github.com/spf13/cobra",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Repo          workspaceRepoPayload `json:"repo"`
		WebhookURL    string               `json:"webhook_url"`
		WebhookSecret string               `json:"webhook_secret"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Repo.ProjectPath != "github.com/spf13/cobra@main" {
		t.Fatalf("unexpected project_path %q", resp.Repo.ProjectPath)
	}
	if resp.Repo.Status != workspacerepos.StatusPending {
		t.Fatalf("expected status=pending, got %q", resp.Repo.Status)
	}
	if resp.WebhookSecret == "" {
		t.Fatalf("webhook secret should be present in response")
	}
	if resp.WebhookURL != "https://cix.example.test/api/v1/webhooks/github/"+resp.Repo.ID {
		t.Fatalf("webhook URL wrong: %q", resp.WebhookURL)
	}

	// Verify the job landed on the queue.
	jobList, err := jobsSvc.List(context.Background(), jobs.StatusPending, "clone_repo", 10)
	if err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	if len(jobList) != 1 {
		t.Fatalf("expected 1 pending clone_repo job, got %d", len(jobList))
	}
	if jobList[0].DedupeKey != "clone:"+resp.Repo.ID {
		t.Fatalf("unexpected dedupe_key %q", jobList[0].DedupeKey)
	}
}

func TestRepos_DuplicateRejected(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")
	body := map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	}
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first add: %d", rr.Code)
	}
	rr = doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate should 409, got %d", rr.Code)
	}
}

func TestRepos_BadURLRejected(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": "https://gitlab.com/x/y",
		"branch":     "main",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for non-github URL, got %d", rr.Code)
	}
}

func TestRepos_DeleteCrossWorkspaceForbidden(t *testing.T) {
	router, _ := reposRouter(t)
	wsA := createWS(t, router, "alpha")
	wsB := createWS(t, router, "bravo")

	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsA+"/repos", map[string]any{
		"github_url": "https://github.com/x/y",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d", rr.Code)
	}
	var resp struct {
		Repo workspaceRepoPayload `json:"repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	// Try to delete repo from workspace B — must 404 (don't leak existence).
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/workspaces/"+wsB+"/repos/"+resp.Repo.ID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete should 404, got %d", rr.Code)
	}

	// Correct workspace should succeed.
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/workspaces/"+wsA+"/repos/"+resp.Repo.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
}

func TestRepos_ReindexDedupeCollapsesInFlightJob(t *testing.T) {
	router, jobsSvc := reposRouter(t)
	wsID := createWS(t, router, "platform")

	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": "https://github.com/foo/bar",
		"branch":     "main",
	})
	var created struct {
		Repo workspaceRepoPayload `json:"repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Add-time already enqueued a clone_repo job — reindex should be
	// dedup'd and return status="already_running".
	rr = doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos/"+created.Repo.ID+"/reindex", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("reindex: %d (%s)", rr.Code, rr.Body.String())
	}
	var rresp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &rresp)
	if rresp.Status != "already_running" {
		t.Fatalf("expected already_running on dedupe, got %q", rresp.Status)
	}

	// Exactly one job on the queue still.
	all, _ := jobsSvc.List(context.Background(), jobs.StatusPending, "clone_repo", 10)
	if len(all) != 1 {
		t.Fatalf("expected dedupe to collapse into 1 job, got %d", len(all))
	}
}

func TestRepos_DisabledFeatureReturns503(t *testing.T) {
	router := workspaceRouter(t, false)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/any/repos", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestJobs_ListEndpointFiltersByStatus(t *testing.T) {
	router, jobsSvc := reposRouter(t)
	ctx := context.Background()
	if _, err := jobsSvc.Enqueue(ctx, jobs.EnqueueRequest{Type: "test_a"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := jobsSvc.Enqueue(ctx, jobs.EnqueueRequest{Type: "test_b"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	rr := doJSON(t, router, http.MethodGet, "/api/v1/jobs", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("jobs list: %d", rr.Code)
	}
	var lr struct {
		Jobs  []jobPayload `json:"jobs"`
		Total int          `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &lr)
	if lr.Total != 2 {
		t.Fatalf("expected 2 jobs, got %d", lr.Total)
	}
	rr = doJSON(t, router, http.MethodGet, "/api/v1/jobs?type=test_a", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("typed list: %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &lr)
	if lr.Total != 1 {
		t.Fatalf("expected 1 typed job, got %d", lr.Total)
	}
}
