package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// reposRouterWithIndexer is reposRouterDB plus a wired indexer.Service
// (fake embedder + temp vectorstore), returned so a test can drive an
// active index session directly via BeginIndexing — needed to exercise
// the force-stop cancelled=true path.
func reposRouterWithIndexer(t *testing.T) (http.Handler, *sql.DB, *indexer.Service) {
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
	vs, err := vectorstore.Open(filepath.Join(t.TempDir(), "chroma"))
	if err != nil {
		t.Fatalf("vectorstore open: %v", err)
	}
	emb := &fakeEmbedder{dim: 16}
	idx := indexer.New(d, vs, emb, nil)
	router := NewRouter(Deps{
		DB:                d,
		ServerVersion:     "test",
		APIVersion:        "v1",
		Backend:           "go",
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		Workspaces:        workspaces.New(d),
		GithubTokens:      githubtokens.New(d, sec),
		GitRepos:          gitrepos.New(d),
		WorkspaceProjects: workspaceprojects.New(d),
		Jobs:              jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour}),
		Indexer:           idx,
		VectorStore:       vs,
		EmbeddingSvc:      emb,
		PublicBaseURL:     "https://cix.example.test",
	})
	return router, d, idx
}

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

// TestAddGitRepo_PollingRequiresWebhookDisabled: polling can't be enabled
// while webhook_mode is the default ('manual').
func TestAddGitRepo_PollingRequiresWebhookDisabled(t *testing.T) {
	router, _ := reposRouter(t)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":      "https://github.com/a/b",
		"branch":          "main",
		"polling_enabled": true,
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("polling+manual should 422, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestAddGitRepo_PollingEnabled: webhook_mode=disabled + polling → 201 with
// polling fields surfaced and next_poll_at scheduled.
func TestAddGitRepo_PollingEnabled(t *testing.T) {
	router, _ := reposRouter(t)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":            "https://github.com/a/b",
		"branch":                "main",
		"webhook_mode":          "disabled",
		"polling_enabled":       true,
		"poll_interval_seconds": 120,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		GitRepo struct {
			WebhookMode         string  `json:"webhook_mode"`
			PollingEnabled      bool    `json:"polling_enabled"`
			PollIntervalSeconds *int    `json:"poll_interval_seconds"`
			NextPollAt          *string `json:"next_poll_at"`
		} `json:"git_repo"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.GitRepo.PollingEnabled {
		t.Error("polling_enabled = false, want true")
	}
	if resp.GitRepo.PollIntervalSeconds == nil || *resp.GitRepo.PollIntervalSeconds != 120 {
		t.Errorf("poll_interval_seconds = %v, want 120", resp.GitRepo.PollIntervalSeconds)
	}
	if resp.GitRepo.NextPollAt == nil {
		t.Error("next_poll_at is null, want a scheduled time")
	}
}

// TestAddGitRepo_AutoFallbackToPolling: webhook_mode=auto with no token_id
// fails auto-registration, so the server flips the repo to disabled+polling.
func TestAddGitRepo_AutoFallbackToPolling(t *testing.T) {
	router, _ := reposRouter(t)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/a/b",
		"branch":       "main",
		"webhook_mode": "auto",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		AutoRegistered bool `json:"auto_registered"`
		GitRepo        struct {
			WebhookMode    string `json:"webhook_mode"`
			PollingEnabled bool   `json:"polling_enabled"`
		} `json:"git_repo"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AutoRegistered {
		t.Error("auto_registered = true, want false (no token)")
	}
	if resp.GitRepo.WebhookMode != "disabled" {
		t.Errorf("webhook_mode = %q, want disabled (fallback)", resp.GitRepo.WebhookMode)
	}
	if !resp.GitRepo.PollingEnabled {
		t.Error("polling_enabled = false, want true (fallback)")
	}
}

// syncResp is the PATCH /git-repo envelope: the updated git_repo + an
// optional human note (e.g. webhook→polling fallback).
type syncResp struct {
	GitRepo struct {
		WebhookMode    string  `json:"webhook_mode"`
		PollingEnabled bool    `json:"polling_enabled"`
		NextPollAt     *string `json:"next_poll_at"`
	} `json:"git_repo"`
	Note string `json:"note"`
}

// TestUpdateProjectGitRepoSync exercises the project-page sync-method switcher
// across all three methods + the invalid case.
func TestUpdateProjectGitRepoSync(t *testing.T) {
	router, _ := reposRouter(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/a/b",
		"branch":       "main",
		"webhook_mode": "disabled",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	patchPath := "/api/v1/projects/" + created.GitRepo.PathHash + "/git-repo"

	patch := func(body map[string]any) syncResp {
		t.Helper()
		rr := doJSON(t, router, http.MethodPatch, patchPath, body)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %v: %d (%s)", body, rr.Code, rr.Body.String())
		}
		var out syncResp
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}

	// → polling
	got := patch(map[string]any{"sync_method": "polling", "poll_interval_seconds": 90})
	if got.GitRepo.WebhookMode != "disabled" || !got.GitRepo.PollingEnabled || got.GitRepo.NextPollAt == nil {
		t.Fatalf("polling: mode=%q polling=%v next=%v", got.GitRepo.WebhookMode, got.GitRepo.PollingEnabled, got.GitRepo.NextPollAt)
	}

	// → manual
	got = patch(map[string]any{"sync_method": "manual"})
	if got.GitRepo.WebhookMode != "disabled" || got.GitRepo.PollingEnabled || got.GitRepo.NextPollAt != nil {
		t.Fatalf("manual: mode=%q polling=%v next=%v", got.GitRepo.WebhookMode, got.GitRepo.PollingEnabled, got.GitRepo.NextPollAt)
	}

	// → webhook: this repo has no token, so auto-register can't install the
	// hook → the server leaves webhook_mode='manual' (a valid webhook the
	// operator finishes by hand) and does NOT fall back to polling.
	got = patch(map[string]any{"sync_method": "webhook"})
	if got.GitRepo.WebhookMode != "manual" || got.GitRepo.PollingEnabled {
		t.Fatalf("webhook manual fallback: mode=%q polling=%v", got.GitRepo.WebhookMode, got.GitRepo.PollingEnabled)
	}
	if got.Note == "" {
		t.Error("expected a note explaining manual webhook setup")
	}

	// → webhook again: the repo is already a manual webhook (operator-managed,
	// no stored hook id). Re-saving must PRESERVE manual and NOT try to
	// register a duplicate hook — the note flips to the "left as-is" message.
	got = patch(map[string]any{"sync_method": "webhook"})
	if got.GitRepo.WebhookMode != "manual" {
		t.Fatalf("re-save webhook: mode=%q, want manual preserved", got.GitRepo.WebhookMode)
	}
	if !strings.Contains(got.Note, "left as-is") {
		t.Errorf("re-save note = %q, want manual-preserve message", got.Note)
	}

	// invalid method → 422
	rr = doJSON(t, router, http.MethodPatch, patchPath, map[string]any{"sync_method": "bogus"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid sync_method should 422, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestWebhookReceiverIgnoresDisabledRepo: a repo switched to polling/manual
// (webhook_mode='disabled') ignores any lingering webhook delivery so it
// doesn't double-sync with the poll scheduler.
func TestWebhookReceiverIgnoresDisabledRepo(t *testing.T) {
	router, _ := reposRouter(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/a/b",
		"branch":       "main",
		"webhook_mode": "disabled",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		GitRepo struct {
			PathHash      string `json:"path_hash"`
			WebhookSecret string `json:"-"`
		} `json:"git_repo"`
		WebhookSecret string `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	body := []byte(`{"ref":"refs/heads/main","after":"abc123"}`)
	mac := hmac.New(sha256.New, []byte(created.WebhookSecret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github/"+created.GitRepo.PathHash, bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook status = %d (%s)", rec.Code, rec.Body.String())
	}
	var wr struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &wr)
	if wr.Status != "ignored" {
		t.Fatalf("status = %q, want ignored (webhook_mode=disabled)", wr.Status)
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

// TestReindexProject_FlipsStatusToIndexing pins the dashboard-facing
// behaviour: clicking Reindex must leave the project in status='indexing'
// before the response returns, so the post-mutation refetch shows the
// "Indexing in progress" alert without waiting for the worker to pick up
// the clone_repo job. Asserts both the response body and the DB row.
func TestReindexProject_FlipsStatusToIndexing(t *testing.T) {
	router, _, d := reposRouterDB(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed git_repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		Project struct {
			Status string `json:"status"`
		} `json:"project"`
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.Project.Status != "created" {
		t.Fatalf("baseline project.status = %q, want created", created.Project.Status)
	}
	hash := created.GitRepo.PathHash

	rr = doJSON(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/reindex", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("reindex: %d (%s)", rr.Code, rr.Body.String())
	}

	var resp struct {
		Status  string `json:"status"`
		Project struct {
			Status string `json:"status"`
		} `json:"project"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode reindex response: %v", err)
	}
	// Auto-enqueue from AddGitRepo already holds the clone:<hash> dedup key,
	// so the reindex POST sees a duplicate and reports "already_running".
	// Either outcome is acceptable; the status flip is what we're verifying.
	if resp.Status != "enqueued" && resp.Status != "already_running" {
		t.Fatalf("reindex response.status = %q, want enqueued or already_running", resp.Status)
	}
	if resp.Project.Status != "indexing" {
		t.Errorf("response project.status = %q, want indexing", resp.Project.Status)
	}

	var dbStatus string
	if err := d.QueryRow(
		`SELECT status FROM projects WHERE host_path = ?`, "github.com/a/b@main",
	).Scan(&dbStatus); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if dbStatus != "indexing" {
		t.Errorf("db project.status = %q, want indexing", dbStatus)
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

// TestForceStopIndex_ClearsPipelineJobs verifies force-stop deletes the
// pending clone job that AddGitRepo auto-enqueues and reports it in
// jobs_cleared, settling the project on a terminal status. No live session
// exists in this harness (the worker pool isn't running), so cancelled=false.
func TestForceStopIndex_ClearsPipelineJobs(t *testing.T) {
	router, _, d := reposRouterDB(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed git_repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	hash := created.GitRepo.PathHash

	// Sanity: the auto-enqueued clone job is queued.
	var pending int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE dedupe_key = ? AND status IN ('pending','running')`,
		"clone:"+hash,
	).Scan(&pending); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if pending != 1 {
		t.Fatalf("baseline pending clone jobs = %d, want 1", pending)
	}

	rr = doJSON(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/force-stop", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("force-stop: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Cancelled   bool `json:"cancelled"`
		JobsCleared int  `json:"jobs_cleared"`
		Project     struct {
			Status string `json:"status"`
		} `json:"project"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode force-stop response: %v", err)
	}
	if resp.JobsCleared != 1 {
		t.Errorf("jobs_cleared = %d, want 1", resp.JobsCleared)
	}
	if resp.Cancelled {
		t.Errorf("cancelled = true, want false (no live session in this harness)")
	}
	// Never indexed → terminal status is 'created'.
	if resp.Project.Status != "created" {
		t.Errorf("project.status = %q, want created", resp.Project.Status)
	}

	if err := d.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE dedupe_key = ? AND status IN ('pending','running')`,
		"clone:"+hash,
	).Scan(&pending); err != nil {
		t.Fatalf("recount jobs: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending clone jobs after force-stop = %d, want 0", pending)
	}
}

func TestForceStopIndex_RequiresGitRepo(t *testing.T) {
	router, _ := reposRouter(t)

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

	rr = doJSON(t, router, http.MethodPost, "/api/v1/projects/"+p.PathHash+"/force-stop", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for local-project force-stop, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestForceStopIndex_UnknownHashIs404 pins that a hash matching no project
// returns 404 (project not found), distinct from the 422 a real-but-local
// project gets. Guards against the earlier behaviour where both collapsed
// into a misleading "no git_repos row" 422.
func TestForceStopIndex_UnknownHashIs404(t *testing.T) {
	router, _ := reposRouter(t)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/projects/deadbeefdeadbeef/force-stop", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown hash, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestForceStopIndex_CancelledNeverIndexed_StatusCreated covers the
// cancelled=true path with the never-indexed guard: an external project
// with a live index session but no recorded indexed_sha must settle on
// 'created', NOT 'indexed'. (indexer.CancelIndexing flips to 'indexed'
// unconditionally; the handler corrects it.)
func TestForceStopIndex_CancelledNeverIndexed_StatusCreated(t *testing.T) {
	router, d, idx := reposRouterWithIndexer(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed git_repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	hash := created.GitRepo.PathHash
	const projectPath = "github.com/a/b@main"

	// Simulate an in-flight index by opening a live session for the repo.
	if _, _, err := idx.BeginIndexing(context.Background(), projectPath, true); err != nil {
		t.Fatalf("begin indexing: %v", err)
	}

	rr = doJSON(t, router, http.MethodPost, "/api/v1/projects/"+hash+"/force-stop", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("force-stop: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Cancelled bool `json:"cancelled"`
		Project   struct {
			Status string `json:"status"`
		} `json:"project"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode force-stop response: %v", err)
	}
	if !resp.Cancelled {
		t.Errorf("cancelled = false, want true (a live session existed)")
	}
	if resp.Project.Status != "created" {
		t.Errorf("project.status = %q, want created (never indexed)", resp.Project.Status)
	}

	var dbStatus string
	if err := d.QueryRow(
		`SELECT status FROM projects WHERE host_path = ?`, projectPath,
	).Scan(&dbStatus); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if dbStatus != "created" {
		t.Errorf("db project.status = %q, want created", dbStatus)
	}

	// Session must be gone — a fresh begin would otherwise 409.
	if _, _, err := idx.BeginIndexing(context.Background(), projectPath, true); err != nil {
		t.Errorf("re-begin after force-stop failed (session not released?): %v", err)
	}
}

// TestAddGitRepo_FailedGitRepoCreate_RollsBackProject covers Fix #5 of
// the branch review: when gitrepos.Create fails AFTER projects.Create
// succeeded, the handler must compensate-delete the freshly created
// projects row. Without that rollback the operator sees a 'pending'
// orphan in /projects that can't be linked to a workspace (status !=
// 'indexed') and can't be reindexed (no git_repos row).
//
// Force the gitrepos.Create failure via an invalid webhook_mode — the
// service-side validation rejects unknown values, by which point the
// handler has already staged the projects row. (URL + branch are
// validated by the handler up front so they don't trigger the same
// orphan window.)
func TestAddGitRepo_FailedGitRepoCreate_RollsBackProject(t *testing.T) {
	router, _, d := reposRouterDB(t)

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/x/orphan-test",
		"branch":       "main",
		"webhook_mode": "totally-bogus-mode",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid webhook_mode, got %d (%s)", rr.Code, rr.Body.String())
	}

	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE host_path = ?`,
		"github.com/x/orphan-test@main",
	).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 0 {
		t.Errorf("expected projects row to be rolled back after gitrepos.Create failure, got count=%d", n)
	}

	// Sanity: no git_repos row either (Create rejected before INSERT).
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM git_repos WHERE project_path = ?`,
		"github.com/x/orphan-test@main",
	).Scan(&n); err != nil {
		t.Fatalf("count git_repos: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no git_repos row after validation failure, got %d", n)
	}
}

// TestAddGitRepo_FailedGitRepoCreate_PreservesPreExistingProject is the
// negative half of Fix #5: when the project pre-existed (created by a
// different flow earlier), a failing AddGitRepo must NOT delete it.
// projectCreatedHere=false → no compensation, even though gitrepos.Create
// failed. This guards the cascade-delete corruption case where rolling
// back would wipe somebody else's git_repos row via FK CASCADE.
func TestAddGitRepo_FailedGitRepoCreate_PreservesPreExistingProject(t *testing.T) {
	router, _, d := reposRouterDB(t)

	// Pre-seed an indexed project for the same path the request would
	// derive. Simulates: a previous successful flow created this row,
	// and our concurrent request shouldn't blow it away.
	hostPath := "github.com/x/keepme@main"
	if _, err := d.Exec(`
		INSERT INTO projects (host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'indexed', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', ?)`,
		hostPath, hostPath, "0123456789abcdef",
	); err != nil {
		t.Fatalf("seed pre-existing project: %v", err)
	}

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/x/keepme",
		"branch":       "main",
		"webhook_mode": "totally-bogus-mode",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (%s)", rr.Code, rr.Body.String())
	}

	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE host_path = ?`, hostPath,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("pre-existing project must be preserved on gitrepos.Create failure, got count=%d", n)
	}
}

// TestAddGitRepo_ConcurrentDuplicate_NoOrphan covers the Fix #5
// acceptance criterion literally: two concurrent POSTs with the same
// github_url + branch should yield one 201 and one 409, with exactly
// one projects row and one git_repos row at the end. This also covers
// Fix #15 (concurrent race test) from the same review.
//
// The compensating delete guard `if no git_repos row exists` is what
// makes this safe — without it, the loser's rollback could cascade
// away the winner's git_repos row.
func TestAddGitRepo_ConcurrentDuplicate_NoOrphan(t *testing.T) {
	router, _, d := reposRouterDB(t)
	body := map[string]any{
		"github_url": "https://github.com/x/concurrent",
		"branch":     "main",
	}

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", body)
			codes <- rr.Code
		}()
	}
	wg.Wait()
	close(codes)

	got := []int{<-codes, <-codes}
	sort.Ints(got)
	// Exactly one 201 (winner) and one 409 (duplicate).
	if got[0] != http.StatusCreated || got[1] != http.StatusConflict {
		t.Fatalf("expected one 201 + one 409, got %v", got)
	}

	// Single projects row.
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE host_path = ?`,
		"github.com/x/concurrent@main",
	).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 projects row after concurrent race, got %d", n)
	}

	// Single git_repos row.
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM git_repos WHERE project_path = ?`,
		"github.com/x/concurrent@main",
	).Scan(&n); err != nil {
		t.Fatalf("count git_repos: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly 1 git_repos row after concurrent race, got %d", n)
	}
}

// TestDeleteProject_CascadesGitRepoAndMembership exercises the chained
// FK ON DELETE CASCADE: removing the project deletes the git_repos row
// AND every workspace_projects row referencing it. Used to be a
// manual cleanup in projects.Delete; now the FKs do the work.
//
// Fix #16 acceptance: explicit COUNT(*) assertions on both child tables
// after the DELETE so anyone who later strips `ON DELETE CASCADE` from
// either FK gets a clear "git_repos should cascade" / "workspace_projects
// should cascade" failure instead of a downstream behavioural surprise.
func TestDeleteProject_CascadesGitRepoAndMembership(t *testing.T) {
	router, _, d := reposRouterDB(t)

	// Add an external project — kicks off project + git_repos rows.
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
	projPath := "github.com/a/b@main"

	// Force status=indexed so workspaceprojects.Link's precondition passes —
	// the clone+index job chain isn't wired in the test harness so we
	// satisfy the invariant by hand.
	if _, err := d.Exec(`UPDATE projects SET status = 'indexed' WHERE host_path = ?`, projPath); err != nil {
		t.Fatalf("mark indexed: %v", err)
	}

	// Create a workspace and link the project so the workspace_projects
	// cascade has a real row to verify (the prior version of this test
	// asserted nothing on workspace_projects — the FK trigger was untested).
	rr = doJSON(t, router, http.MethodPost, "/api/v1/workspaces", map[string]any{
		"name":        "platform",
		"description": "cascade test",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create workspace: %d (%s)", rr.Code, rr.Body.String())
	}
	var ws struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &ws)
	rr = doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/projects", map[string]any{
		"project_hash": hash,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("link project: %d (%s)", rr.Code, rr.Body.String())
	}

	// Sanity-pin the pre-delete state — without this, a "0 rows after
	// delete" assertion can't distinguish "cascade fired" from "row
	// never existed in the first place".
	assertCount := func(t *testing.T, q string, want int) {
		t.Helper()
		var n int
		if err := d.QueryRow(q, projPath).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		if n != want {
			t.Errorf("count %q: got %d, want %d", q, n, want)
		}
	}
	assertCount(t, `SELECT COUNT(*) FROM git_repos WHERE project_path = ?`, 1)
	assertCount(t, `SELECT COUNT(*) FROM workspace_projects WHERE project_path = ?`, 1)

	// Delete the project — both child rows must cascade.
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/projects/"+hash, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d (%s)", rr.Code, rr.Body.String())
	}
	assertCount(t, `SELECT COUNT(*) FROM git_repos WHERE project_path = ?`, 0)
	assertCount(t, `SELECT COUNT(*) FROM workspace_projects WHERE project_path = ?`, 0)

	// Re-adding the exact same upstream must succeed — end-to-end check
	// that the git_repos row was actually removed (otherwise
	// UNIQUE(github_url, branch) would 409 here, which is the bug a
	// previous patch fixed).
	rr = doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-add after delete: %d (%s)", rr.Code, rr.Body.String())
	}
}
