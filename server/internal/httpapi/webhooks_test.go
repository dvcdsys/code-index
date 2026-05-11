package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// addRepo helper — creates a workspace + repo and returns the repo
// payload so tests can lift webhook_secret/id directly.
func addRepo(t *testing.T, router http.Handler, wsName, githubURL, branch string) workspaceRepoPayload {
	t.Helper()
	wsID := createWS(t, router, wsName)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": githubURL,
		"branch":     branch,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var got struct {
		Repo          workspaceRepoPayload `json:"repo"`
		WebhookSecret string               `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	// Stash the secret onto the payload via the URL — tests pluck it
	// from the response body directly when needed; this helper just
	// returns the repo. Test bodies that need the secret call addRepoWithSecret.
	return got.Repo
}

func addRepoWithSecret(t *testing.T, router http.Handler, wsName, githubURL, branch string) (workspaceRepoPayload, string) {
	t.Helper()
	wsID := createWS(t, router, wsName)
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": githubURL,
		"branch":     branch,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var got struct {
		Repo          workspaceRepoPayload `json:"repo"`
		WebhookSecret string               `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return got.Repo, got.WebhookSecret
}

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, router http.Handler, repoID string, body []byte, sig, event string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github/"+repoID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestWebhook_PingReturns200(t *testing.T) {
	router, _ := reposRouter(t)
	repo, secret := addRepoWithSecret(t, router, "platform", "https://github.com/x/y", "main")
	body := []byte(`{"zen":"Speak like a human."}`)
	rr := postWebhook(t, router, repo.ID, body, signBody(body, secret), "ping")
	if rr.Code != http.StatusOK {
		t.Fatalf("ping: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestWebhook_PushEnqueuesCloneJob(t *testing.T) {
	router, jobsSvc := reposRouter(t)
	repo, secret := addRepoWithSecret(t, router, "platform", "https://github.com/x/y", "main")

	// Drain the initial clone job from the add-repo call so we can see the
	// webhook's own dedupe behaviour clearly.
	ctx := context.Background()
	initial, _ := jobsSvc.List(ctx, jobs.StatusPending, "clone_repo", 10)
	if len(initial) != 1 {
		t.Fatalf("expected 1 initial clone, got %d", len(initial))
	}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123def456"}`)
	rr := postWebhook(t, router, repo.ID, body, signBody(body, secret), "push")
	// Dedupe with the in-flight initial clone → 202 already_running.
	if rr.Code != http.StatusAccepted {
		t.Fatalf("push: expected 202, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "already_running" {
		t.Fatalf("expected dedupe → already_running, got %q", resp.Status)
	}
}

func TestWebhook_PushOnDifferentBranchIgnored(t *testing.T) {
	router, _ := reposRouter(t)
	repo, secret := addRepoWithSecret(t, router, "platform", "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/develop","after":"abc123"}`)
	rr := postWebhook(t, router, repo.ID, body, signBody(body, secret), "push")
	if rr.Code != http.StatusOK {
		t.Fatalf("ignored: expected 200, got %d", rr.Code)
	}
	var resp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "ignored" {
		t.Fatalf("expected ignored, got %q", resp.Status)
	}
}

func TestWebhook_BadSignatureRejected(t *testing.T) {
	router, _ := reposRouter(t)
	repo, _ := addRepoWithSecret(t, router, "platform", "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/main","after":"abc"}`)
	// Sign with the wrong secret.
	rr := postWebhook(t, router, repo.ID, body, signBody(body, "wrong"), "push")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestWebhook_MissingSignatureRejected(t *testing.T) {
	router, _ := reposRouter(t)
	repo, _ := addRepoWithSecret(t, router, "platform", "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/main","after":"abc"}`)
	rr := postWebhook(t, router, repo.ID, body, "", "push")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no sig: expected 401, got %d", rr.Code)
	}
}

func TestWebhook_UnknownRepoReturns404(t *testing.T) {
	router, _ := reposRouter(t)
	body := []byte(`{}`)
	// Use the right HMAC math but a bogus repo id — must still 404 (we
	// short-circuit before HMAC since there's no secret to compare against).
	rr := postWebhook(t, router, "no-such-repo", body, signBody(body, "anything"), "push")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown repo: expected 404, got %d", rr.Code)
	}
}

func TestWebhook_PathIsPublic(t *testing.T) {
	// Spin up a router with auth ENABLED (not the test-default) to verify
	// the webhook path is reachable without credentials.
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := NewRouter(Deps{
		DB:           d,
		Users:        seedlessUsers(d),
		Sessions:     seedlessSessions(d),
		APIKeys:      seedlessAPIKeys(d),
		AuthDisabled: false,
		// Workspaces disabled — but auth middleware should still let
		// the request reach our handler before the 503 fires.
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github/anything", bytes.NewReader([]byte(`{}`)))
	router.ServeHTTP(rr, req)
	// We expect either 503 (feature off) or 404, NOT 401 — the public-path
	// gate should let us through the auth middleware.
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("webhook path leaked into auth-gated set, got 401")
	}
}

func TestAddRepo_AutoRegisterFailsCleanlyWithoutPublicURL(t *testing.T) {
	// reposRouter sets PublicBaseURL=https://cix.example.test, but the
	// auto-register flow tries a real github.com call which the test
	// can't allow. So skip when wired with a real URL — this test
	// exercises the empty-URL branch by building a separate router.
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
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		WorkspacesEnabled: true,
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		WorkspaceRepos:    wrSvc,
		Jobs:              jobsSvc,
		// PublicBaseURL deliberately unset.
	})

	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url":   "https://github.com/x/y",
		"branch":       "main",
		"auto_webhook": true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		AutoRegistered bool   `json:"auto_registered"`
		Note           string `json:"auto_register_note"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.AutoRegistered {
		t.Fatalf("AutoRegistered should be false without public URL")
	}
	if resp.Note == "" {
		t.Fatalf("operator-facing note should explain the reason")
	}
}

func TestWebhookInfo_ReturnsURLAndSecret(t *testing.T) {
	router, _ := reposRouter(t)
	wsID := createWS(t, router, "platform")
	// Manual add — we want the wsID + repo for the URL construction.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces/"+wsID+"/repos", map[string]any{
		"github_url": "https://github.com/a/b",
		"branch":     "main",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d", rr.Code)
	}
	var created struct {
		Repo          workspaceRepoPayload `json:"repo"`
		WebhookSecret string               `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/repos/"+created.Repo.ID+"/webhook-info", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("webhook-info: %d (%s)", rr.Code, rr.Body.String())
	}
	var info struct {
		WebhookURL     string `json:"webhook_url"`
		WebhookSecret  string `json:"webhook_secret"`
		AutoRegistered bool   `json:"auto_registered"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.WebhookSecret != created.WebhookSecret {
		t.Fatalf("secret mismatch between create and info")
	}
	if info.WebhookURL != "https://cix.example.test/api/v1/webhooks/github/"+created.Repo.ID {
		t.Fatalf("URL wrong: %q", info.WebhookURL)
	}
	if info.AutoRegistered {
		t.Fatalf("AutoRegistered should be false for manual setup")
	}
}
