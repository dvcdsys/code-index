package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
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

// TestRepos_WebhookModeStored covers the three-state webhook_mode
// introduced for the add-repo UI. The DB column should round-trip the
// chosen mode; the legacy auto_webhook bool is derived (true iff
// mode == "auto") so old API clients keep behaving the same.
func TestRepos_WebhookModeStored(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")

	cases := []struct {
		name         string
		body         map[string]any
		wantMode     string
		wantAutoBool bool
	}{
		{
			name:         "manual explicit",
			body:         map[string]any{"github_url": "https://github.com/a/manual", "branch": "main", "webhook_mode": "manual"},
			wantMode:     "manual",
			wantAutoBool: false,
		},
		{
			name:         "auto explicit",
			body:         map[string]any{"github_url": "https://github.com/a/auto", "branch": "main", "webhook_mode": "auto"},
			wantMode:     "auto",
			wantAutoBool: true,
		},
		{
			name:         "disabled explicit",
			body:         map[string]any{"github_url": "https://github.com/a/disabled", "branch": "main", "webhook_mode": "disabled"},
			wantMode:     "disabled",
			wantAutoBool: false,
		},
		{
			name:         "legacy auto_webhook bool",
			body:         map[string]any{"github_url": "https://github.com/a/legacy", "branch": "main", "auto_webhook": true},
			wantMode:     "auto",
			wantAutoBool: true,
		},
		{
			name:         "default when omitted",
			body:         map[string]any{"github_url": "https://github.com/a/default", "branch": "main"},
			wantMode:     "manual",
			wantAutoBool: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", tc.body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
			}
			var resp struct {
				Repo workspaceRepoPayload `json:"repo"`
			}
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if resp.Repo.WebhookMode != tc.wantMode {
				t.Fatalf("webhook_mode = %q, want %q", resp.Repo.WebhookMode, tc.wantMode)
			}
			if resp.Repo.AutoWebhook != tc.wantAutoBool {
				t.Fatalf("auto_webhook = %v, want %v (for mode=%q)",
					resp.Repo.AutoWebhook, tc.wantAutoBool, tc.wantMode)
			}
		})
	}
}

// TestRepos_WebhookModeRejectsUnknown ensures the DB never receives an
// unknown enum value — the dashboard's three radio buttons are the only
// supported inputs.
func TestRepos_WebhookModeRejectsUnknown(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url":   "https://github.com/a/b",
		"branch":       "main",
		"webhook_mode": "totally-bogus",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on unknown mode, got %d", rr.Code)
	}
}

// TestStandaloneGitRepo_LandsInDefaultWorkspace covers the
// /api/v1/git-repos shortcut used by the dashboard's /projects → Add
// repo button. The endpoint must resolve (or lazily create) the
// default workspace and run the same clone+enqueue logic as the
// per-workspace endpoint. Critical assertions: the new row points at
// the default workspace, a clone_repo job is enqueued, and the
// resulting project_path matches the canonical shape.
func TestStandaloneGitRepo_LandsInDefaultWorkspace(t *testing.T) {
	router, jobsSvc := reposRouter(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/spf13/cobra",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("standalone add: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Repo workspaceRepoPayload `json:"repo"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Repo.WorkspaceID == "" {
		t.Fatalf("workspace_id must be populated, got empty payload: %+v", resp.Repo)
	}
	if resp.Repo.ProjectPath != "github.com/spf13/cobra@main" {
		t.Fatalf("unexpected project_path %q", resp.Repo.ProjectPath)
	}
	if resp.Repo.Status != workspacerepos.StatusPending {
		t.Fatalf("expected status=pending, got %q", resp.Repo.Status)
	}

	// Pull the default workspace through the service-level GetDefault
	// to verify the row actually lives there, not just "some workspace".
	rr2 := doJSON(t, router, http.MethodGet, "/api/v1/workspaces", nil)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list workspaces: %d", rr2.Code)
	}
	var list struct {
		Workspaces []workspacePayload `json:"workspaces"`
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &list)
	var defaultID string
	for _, w := range list.Workspaces {
		if w.IsDefault {
			defaultID = w.ID
		}
	}
	if defaultID == "" {
		t.Fatalf("no default workspace surfaced in list response: %+v", list.Workspaces)
	}
	if resp.Repo.WorkspaceID != defaultID {
		t.Fatalf("standalone repo landed in workspace=%q, want default=%q",
			resp.Repo.WorkspaceID, defaultID)
	}

	jobList, err := jobsSvc.List(context.Background(), jobs.StatusPending, "clone_repo", 10)
	if err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	if len(jobList) != 1 {
		t.Fatalf("expected 1 clone_repo job for the standalone add, got %d", len(jobList))
	}
}

// TestStandaloneGitRepo_DeleteDefaultWorkspaceConflict pins the
// invariant the dashboard relies on: the default workspace cannot be
// removed via the regular delete endpoint, so /projects → Add repo
// always has a home to drop the row into.
func TestStandaloneGitRepo_DeleteDefaultWorkspaceConflict(t *testing.T) {
	router, _ := reposRouter(t)

	// Force the default workspace to exist by hitting /git-repos once.
	if rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	}); rr.Code != http.StatusCreated {
		t.Fatalf("seed: %d (%s)", rr.Code, rr.Body.String())
	}

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces", nil)
	var list struct {
		Workspaces []workspacePayload `json:"workspaces"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	var defaultID string
	for _, w := range list.Workspaces {
		if w.IsDefault {
			defaultID = w.ID
		}
	}
	if defaultID == "" {
		t.Fatalf("no default workspace found")
	}

	rr = doJSON(t, router, http.MethodDelete, "/api/v1/workspaces/"+defaultID, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 on default-workspace delete, got %d (%s)",
			rr.Code, rr.Body.String())
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

// reposRouterWithDB is the same router setup as reposRouter but also
// returns the underlying *sql.DB so link-existing tests can seed
// projects directly. We keep reposRouter signature unchanged so the
// existing call sites in webhooks_test.go and elsewhere stay green.
func reposRouterWithDB(t *testing.T) (http.Handler, *jobs.Service, *sql.DB) {
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
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour})

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
	return router, jobsSvc, d
}

// seedIndexedProject creates an indexed project row with the given
// host_path and returns its path_hash. Used by the link-existing tests
// so they don't need to invoke the real cloner+indexer.
func seedIndexedProject(t *testing.T, db *sql.DB, hostPath string) string {
	t.Helper()
	if _, err := projects.Create(context.Background(), db, projects.CreateRequest{HostPath: hostPath}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE projects SET status = 'indexed', last_indexed_at = ?, updated_at = ? WHERE host_path = ?`,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		hostPath,
	); err != nil {
		t.Fatalf("mark indexed: %v", err)
	}
	return projects.HashPath(hostPath)
}

func TestLinkExistingProject_SkipsCloneJob(t *testing.T) {
	router, jobsSvc, d := reposRouterWithDB(t)
	wsID := createWS(t, router, "platform")
	hash := seedIndexedProject(t, d, "github.com/spf13/cobra@main")

	rr := doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/repos/link",
		map[string]any{"project_hash": hash})
	if rr.Code != http.StatusCreated {
		t.Fatalf("link: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Repo           workspaceRepoPayload `json:"repo"`
		WebhookURL     string               `json:"webhook_url"`
		WebhookSecret  string               `json:"webhook_secret"`
		AutoRegistered bool                 `json:"auto_registered"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Repo.IsLinked {
		t.Fatalf("expected IsLinked=true, got %+v", resp.Repo)
	}
	if resp.Repo.Status != workspacerepos.StatusIndexed {
		t.Fatalf("expected status=indexed, got %q", resp.Repo.Status)
	}
	if resp.Repo.WebhookMode != workspacerepos.WebhookModeDisabled {
		t.Fatalf("expected webhook_mode=disabled, got %q", resp.Repo.WebhookMode)
	}
	if resp.WebhookURL != "" || resp.WebhookSecret != "" {
		t.Fatalf("linked rows must not surface webhook info, got url=%q secret-len=%d",
			resp.WebhookURL, len(resp.WebhookSecret))
	}

	// Critical: no clone_repo job should have been enqueued.
	jobList, err := jobsSvc.List(context.Background(), jobs.StatusPending, "clone_repo", 10)
	if err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	if len(jobList) != 0 {
		t.Fatalf("expected 0 clone_repo jobs, got %d (linked rows must not clone)", len(jobList))
	}
}

func TestLinkExistingProject_409OnDuplicate(t *testing.T) {
	router, _, d := reposRouterWithDB(t)
	wsID := createWS(t, router, "platform")
	hash := seedIndexedProject(t, d, "github.com/foo/bar@main")

	rr := doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/repos/link",
		map[string]any{"project_hash": hash})
	if rr.Code != http.StatusCreated {
		t.Fatalf("first link: %d (%s)", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/repos/link",
		map[string]any{"project_hash": hash})
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate link, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestLinkExistingProject_422IfProjectNotIndexed(t *testing.T) {
	router, _, d := reposRouterWithDB(t)
	wsID := createWS(t, router, "platform")
	// Create the project but leave status=created (the default).
	hostPath := "github.com/foo/bar@main"
	if _, err := projects.Create(context.Background(), d,
		projects.CreateRequest{HostPath: hostPath}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rr := doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/repos/link",
		map[string]any{"project_hash": projects.HashPath(hostPath)})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestLinkExistingProject_404OnUnknownHash(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/repos/link",
		map[string]any{"project_hash": "0000000000000000"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown project_hash, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestListProjectWorkspaces_ReturnsAllMemberships(t *testing.T) {
	router, _, d := reposRouterWithDB(t)
	hash := seedIndexedProject(t, d, "github.com/foo/bar@main")
	wsA := createWS(t, router, "alpha")
	wsB := createWS(t, router, "beta")

	for _, ws := range []string{wsA, wsB} {
		rr := doJSON(t, router, http.MethodPost,
			"/api/v1/workspaces/"+ws+"/repos/link",
			map[string]any{"project_hash": hash})
		if rr.Code != http.StatusCreated {
			t.Fatalf("link to %s: %d (%s)", ws, rr.Code, rr.Body.String())
		}
	}

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/projects/"+hash+"/workspaces", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list workspaces: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Workspaces []struct {
			WorkspaceID   string `json:"workspace_id"`
			WorkspaceName string `json:"workspace_name"`
			RepoID        string `json:"repo_id"`
			IsLinked      bool   `json:"is_linked"`
			Status        string `json:"status"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Workspaces) != 2 {
		t.Fatalf("expected 2 memberships, got %d (%+v)", len(resp.Workspaces), resp.Workspaces)
	}
	for _, m := range resp.Workspaces {
		if !m.IsLinked {
			t.Fatalf("workspace %s membership should be linked", m.WorkspaceName)
		}
		if m.Status != workspacerepos.StatusIndexed {
			t.Fatalf("status should be indexed, got %q", m.Status)
		}
	}
}

func TestListProjectWorkspaces_EmptyWhenUnused(t *testing.T) {
	router, _, d := reposRouterWithDB(t)
	hash := seedIndexedProject(t, d, "github.com/lonely/project@main")

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/projects/"+hash+"/workspaces", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list workspaces: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Workspaces []any `json:"workspaces"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Workspaces) != 0 {
		t.Fatalf("expected empty list, got %d", len(resp.Workspaces))
	}
}

func TestListProjectWorkspaces_404OnUnknownHash(t *testing.T) {
	router, _ := reposRouter(t)
	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/projects/0000000000000000/workspaces", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rr.Code, rr.Body.String())
	}
}
