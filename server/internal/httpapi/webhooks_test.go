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
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// addGitRepo helper — POSTs /git-repos and returns (path_hash, webhook_secret)
// so individual webhook tests can post against the new URL shape.
func addGitRepo(t *testing.T, router http.Handler, githubURL, branch string) (string, string) {
	t.Helper()
	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url": githubURL,
		"branch":     branch,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add git_repo: %d (%s)", rr.Code, rr.Body.String())
	}
	var got struct {
		GitRepo struct {
			PathHash string `json:"path_hash"`
		} `json:"git_repo"`
		WebhookSecret string `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return got.GitRepo.PathHash, got.WebhookSecret
}

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, router http.Handler, hash string, body []byte, sig, event string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github/"+hash, bytes.NewReader(body))
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
	hash, secret := addGitRepo(t, router, "https://github.com/x/y", "main")
	body := []byte(`{"zen":"Speak like a human."}`)
	rr := postWebhook(t, router, hash, body, signBody(body, secret), "ping")
	if rr.Code != http.StatusOK {
		t.Fatalf("ping: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestWebhook_PushEnqueuesCloneJob(t *testing.T) {
	router, jobsSvc := reposRouter(t)
	hash, secret := addGitRepo(t, router, "https://github.com/x/y", "main")

	ctx := context.Background()
	initial, _ := jobsSvc.List(ctx, jobs.StatusPending, "clone_repo", 10)
	if len(initial) != 1 {
		t.Fatalf("expected 1 initial clone, got %d", len(initial))
	}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123def456"}`)
	rr := postWebhook(t, router, hash, body, signBody(body, secret), "push")
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
	hash, secret := addGitRepo(t, router, "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/develop","after":"abc123"}`)
	rr := postWebhook(t, router, hash, body, signBody(body, secret), "push")
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
	hash, _ := addGitRepo(t, router, "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/main","after":"abc"}`)
	rr := postWebhook(t, router, hash, body, signBody(body, "wrong"), "push")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestWebhook_MissingSignatureRejected(t *testing.T) {
	router, _ := reposRouter(t)
	hash, _ := addGitRepo(t, router, "https://github.com/x/y", "main")
	body := []byte(`{"ref":"refs/heads/main","after":"abc"}`)
	rr := postWebhook(t, router, hash, body, "", "push")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no sig: expected 401, got %d", rr.Code)
	}
}

// TestValidHMAC_RejectsEmptySecret pins the empty-secret guard added in
// Fix #13. HMAC keyed with the empty string returns a fixed, attacker-
// computable digest for any body; if validHMAC accepted that case, a
// caller who forgot to load the secret (or a future bug that passed
// nil) would silently authenticate every delivery. We pre-compute the
// "correct" HMAC with the empty key over a known body and assert the
// function STILL rejects it — the empty-secret short-circuit must fire
// before the constant-time compare, never after.
func TestValidHMAC_RejectsEmptySecret(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	// Compute what the header WOULD be if we naively HMAC'd with "".
	mac := hmac.New(sha256.New, []byte(""))
	mac.Write(body)
	forged := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if validHMAC(body, nil, forged) {
		t.Error("validHMAC accepted a nil-secret HMAC — guard missing")
	}
	if validHMAC(body, []byte(""), forged) {
		t.Error("validHMAC accepted an empty-byte-slice secret HMAC — guard missing")
	}

	// Sanity: with a real secret + matching signature it still works.
	real := []byte("real-secret")
	mac2 := hmac.New(sha256.New, real)
	mac2.Write(body)
	good := "sha256=" + hex.EncodeToString(mac2.Sum(nil))
	if !validHMAC(body, real, good) {
		t.Error("validHMAC rejected a correctly-signed body with a real secret — guard over-broad")
	}
}

func TestWebhook_UnknownHashReturns404(t *testing.T) {
	router, _ := reposRouter(t)
	body := []byte(`{}`)
	rr := postWebhook(t, router, "0000000000000000", body, signBody(body, "anything"), "push")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown hash: expected 404, got %d", rr.Code)
	}
}

func TestWebhook_PathIsPublic(t *testing.T) {
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
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("webhook path leaked into auth-gated set, got 401")
	}
}

func TestAddGitRepo_AutoRegisterFailsCleanlyWithoutPublicURL(t *testing.T) {
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
	grSvc := gitrepos.New(d)
	wpSvc := workspaceprojects.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour})

	router := NewRouter(Deps{
		DB:                d,
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		GitRepos:          grSvc,
		WorkspaceProjects: wpSvc,
		Jobs:              jobsSvc,
		// PublicBaseURL deliberately unset.
	})

	rr := doJSON(t, router, http.MethodPost, "/api/v1/git-repos", map[string]any{
		"github_url":   "https://github.com/x/y",
		"branch":       "main",
		"webhook_mode": "auto",
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

// TestGetProjectWebhookInfo_LocalProject_Returns404 verifies that the
// webhook-info endpoint returns 404 when the project exists but has no
// `git_repos` peer (i.e. it's a local-path project tracked only in the
// `projects` table). Local projects don't go through the clone +
// webhook lifecycle, so there's no URL or secret to surface. The
// operation contract documented in doc/openapi.yaml requires callers
// to disambiguate "project missing" vs "project local" by first
// hitting GET /projects/{hash} — this test pins the local-project arm
// of the 404 response so the contract doesn't drift.
func TestGetProjectWebhookInfo_LocalProject_Returns404(t *testing.T) {
	router, _, d := reposRouterDB(t)

	// Seed a local project — absolute filesystem path, no git_repos peer.
	hostPath := "/Users/x/local-proj"
	hash := projects.HashPath(hostPath)
	if _, err := d.Exec(`
		INSERT INTO projects (host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'indexed', '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z', ?)`,
		hostPath, hostPath, hash,
	); err != nil {
		t.Fatalf("seed local project: %v", err)
	}

	rr := doJSON(t, router, http.MethodGet, "/api/v1/projects/"+hash+"/webhook-info", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for local project webhook-info, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Body should mention "local" or "no git_repos" so an operator
	// reading the API response (or curl output) understands why this
	// project doesn't have webhook coordinates.
	body := rr.Body.String()
	if !strings.Contains(body, "local") && !strings.Contains(body, "git_repos") {
		t.Errorf("404 body should hint at the local-project cause, got: %s", body)
	}
}

func TestWebhookInfo_ReturnsURLAndSecret(t *testing.T) {
	router, _ := reposRouter(t)
	// AddGitRepo includes the secret in the create response.
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
		WebhookSecret string `json:"webhook_secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	hash := projects.HashPath("github.com/a/b@main")
	if hash != created.GitRepo.PathHash {
		t.Fatalf("path_hash mismatch: %q vs %q", hash, created.GitRepo.PathHash)
	}
	rr = doJSON(t, router, http.MethodGet, "/api/v1/projects/"+hash+"/webhook-info", nil)
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
	if info.WebhookURL != "https://cix.example.test/api/v1/webhooks/github/"+hash {
		t.Fatalf("URL wrong: %q", info.WebhookURL)
	}
	if info.AutoRegistered {
		t.Fatalf("AutoRegistered should be false for manual setup")
	}
}
