package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// fakeGithubAPIScopes is the comma-separated X-OAuth-Scopes value the
// in-test GitHub stub returns from GET /user. Tests that need to check
// scope-from-header propagation read this constant.
const fakeGithubAPIScopes = "repo, admin:repo_hook"

// fakeGithubAPI returns the base URL of an httptest server that
// answers GET /user with 200 + a stable X-OAuth-Scopes header — the
// minimum the token-creation handler needs to think a PAT is valid.
// Exposed so individual tests can swap in different responses (e.g. a
// 401 to exercise the rejection path) by overriding Deps.GithubAPIBaseURL.
func fakeGithubAPI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("X-OAuth-Scopes", fakeGithubAPIScopes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login": "test-user"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// workspaceRouter spins up a chi router with auth disabled, workspaces
// enabled, and an in-memory backing store. Helpers stay tight; the
// existing dbOpenMemory + seedless* shims live in auth_test.go.
//
// The token-creation handler now calls GET /user to validate the PAT
// and read X-OAuth-Scopes. Tests get a deterministic stub via
// fakeGithubAPI so we don't hit the real api.github.com.
func workspaceRouter(t *testing.T, enabled bool) http.Handler {
	t.Helper()
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	var (
		wsSvc *workspaces.Service
		ghSvc *githubtokens.Service
	)
	if enabled {
		sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
		if err != nil {
			t.Fatalf("open secrets: %v", err)
		}
		wsSvc = workspaces.New(d)
		ghSvc = githubtokens.New(d, sec)
	}
	// When `enabled` is false the services stay nil so the handler
	// helpers return 503 — keeps the disabled-by-default smoke test
	// working without relying on a feature flag.

	return NewRouter(Deps{
		DB:               d,
		ServerVersion:    "test",
		APIVersion:       "v1",
		Backend:          "go",
		Logger:           nil,
		AuthDisabled:     true,
		Users:            seedlessUsers(d),
		Sessions:         seedlessSessions(d),
		APIKeys:          seedlessAPIKeys(d),
		Workspaces:       wsSvc,
		GithubTokens:     ghSvc,
		GithubAPIBaseURL: fakeGithubAPI(t),
	})
}

func doJSON(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// TestWorkspaces_ServicesMissingReturns503 covers the defensive path
// in workspacesUnavailable: when Deps.Workspaces is nil (test passed a
// partial Deps, or boot failed to wire it), the handler returns 503
// rather than nil-panicking.
func TestWorkspaces_ServicesMissingReturns503(t *testing.T) {
	router := workspaceRouter(t, false)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when workspaces service unwired, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestWorkspaces_CRUD(t *testing.T) {
	router := workspaceRouter(t, true)

	// Create
	rr := doJSON(t, router, http.MethodPost, "/api/v1/workspaces", map[string]any{
		"name":        "platform",
		"description": "microservices",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	var created workspacePayload
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID == "" || created.Name != "platform" || created.Description != "microservices" {
		t.Fatalf("unexpected created payload: %+v", created)
	}

	// Duplicate name → 409
	rr = doJSON(t, router, http.MethodPost, "/api/v1/workspaces", map[string]any{"name": "platform"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate: expected 409, got %d", rr.Code)
	}

	// Get
	rr = doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+created.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rr.Code)
	}

	// List
	rr = doJSON(t, router, http.MethodGet, "/api/v1/workspaces", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rr.Code)
	}
	var listResp struct {
		Workspaces []workspacePayload `json:"workspaces"`
		Total      int                `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	if listResp.Total != 1 || len(listResp.Workspaces) != 1 {
		t.Fatalf("list mismatch: %+v", listResp)
	}

	// Patch
	rr = doJSON(t, router, http.MethodPatch, "/api/v1/workspaces/"+created.ID, map[string]any{
		"description": "renamed",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: expected 200, got %d", rr.Code)
	}
	var patched workspacePayload
	_ = json.Unmarshal(rr.Body.Bytes(), &patched)
	if patched.Description != "renamed" || patched.Name != "platform" {
		t.Fatalf("patch did not apply: %+v", patched)
	}

	// Delete
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/workspaces/"+created.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", rr.Code)
	}
	// Second delete → 404
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/workspaces/"+created.ID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("delete-twice: expected 404, got %d", rr.Code)
	}
}

func TestGithubTokens_CRUD_PlaintextNotEchoed(t *testing.T) {
	router := workspaceRouter(t, true)

	const secret = "ghp_super_secret_test_value_donotleak"
	// User-supplied scopes in the body are deliberately wrong here;
	// the server must ignore them and use what the (stubbed) GitHub
	// API advertises via X-OAuth-Scopes.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{
		"name":   "personal",
		"token":  secret,
		"scopes": []string{"deliberately-wrong-scope"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(secret)) {
		t.Fatalf("CRITICAL: plaintext leaked in POST response body: %s", rr.Body.String())
	}
	var created githubTokenPayload
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ID == "" || created.Name != "personal" {
		t.Fatalf("unexpected payload: %+v", created)
	}
	// Scopes must come from the stub's X-OAuth-Scopes header,
	// not from the request body — that's the whole point of the
	// validate-against-GitHub flow.
	if len(created.Scopes) != 2 ||
		created.Scopes[0] != "repo" ||
		created.Scopes[1] != "admin:repo_hook" {
		t.Fatalf("expected scopes from X-OAuth-Scopes header, got %v", created.Scopes)
	}

	// List must not contain plaintext anywhere.
	rr = doJSON(t, router, http.MethodGet, "/api/v1/github-tokens", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rr.Code)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(secret)) {
		t.Fatalf("CRITICAL: plaintext leaked in GET list body: %s", rr.Body.String())
	}

	// Delete.
	rr = doJSON(t, router, http.MethodDelete, "/api/v1/github-tokens/"+created.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", rr.Code)
	}
}

// TestGithubTokens_RejectInvalidToken — when GitHub answers 401 we must
// surface a 422 with a clear message rather than persisting an unusable
// token. Exercised with a one-off stub that always rejects.
func TestGithubTokens_RejectInvalidToken(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	}))
	t.Cleanup(stub.Close)

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
		GithubAPIBaseURL:  stub.URL,
	})

	rr := doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{
		"name":  "personal",
		"token": "ghp_bad",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on invalid token, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("Bad credentials")) {
		t.Fatalf("error body should surface GitHub message, got %s", rr.Body.String())
	}
}

// TestGithubTokens_ListRepos exercises the new add-repo flow's first
// step: the dashboard fetches the repos visible to a stored PAT so it
// can render the repo picker. Validates that:
//   - the PAT is never echoed in the response
//   - the X-OAuth-Scopes-validated token survives long enough for the
//     subsequent /repos call to use it
//   - the optional q= filter is applied server-side
func TestGithubTokens_ListRepos(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}

	// Combined stub serves /user (for token validation) and /user/repos
	// (for the new endpoint). Two repos returned so the q= filter test
	// has something to discriminate.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = w.Write([]byte(`{"login": "alice"}`))
		case "/user/repos":
			_, _ = w.Write([]byte(`[
				{"full_name":"alice/services","default_branch":"main","private":true,"html_url":"https://github.com/alice/services"},
				{"full_name":"alice/docs","default_branch":"main","private":false,"html_url":"https://github.com/alice/docs"}
			]`))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(stub.Close)

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
		GithubAPIBaseURL:  stub.URL,
	})

	// Create the token so we have an id to address.
	const secret = "ghp_secret_value"
	rr := doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{
		"name":  "personal",
		"token": secret,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create token: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	var created githubTokenPayload
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Unfiltered list — two repos.
	rr = doJSON(t, router, http.MethodGet, "/api/v1/github-tokens/"+created.ID+"/repos", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list repos: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(secret)) {
		t.Fatalf("CRITICAL: PAT plaintext leaked in repos list body")
	}
	var allResp struct {
		Repos []struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
		} `json:"repos"`
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &allResp)
	if allResp.Total != 2 {
		t.Fatalf("expected 2 repos, got %d (%s)", allResp.Total, rr.Body.String())
	}

	// Filtered list (q=docs) — server applies the substring filter.
	rr = doJSON(t, router, http.MethodGet, "/api/v1/github-tokens/"+created.ID+"/repos?q=docs", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered: expected 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &allResp)
	if allResp.Total != 1 || allResp.Repos[0].FullName != "alice/docs" {
		t.Fatalf("expected only alice/docs, got %+v", allResp)
	}
}

// TestGithubTokens_ListAccountsAndScopedRepos covers the new
// add-repo flow: the dashboard fetches the accounts visible to a PAT,
// then asks for repos scoped to a specific account. Both paths must
// keep the PAT plaintext server-side and use the right GitHub endpoint.
func TestGithubTokens_ListAccountsAndScopedRepos(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}

	// Records which path GitHub was hit on so the test can assert
	// account-scoped requests reach the right endpoint.
	var hitPath string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = w.Write([]byte(`{"login":"alice"}`))
		case "/orgs/acme/repos":
			_, _ = w.Write([]byte(`[{"full_name":"acme/api","default_branch":"main","private":true,"html_url":"https://github.com/acme/api","owner":{"login":"acme","type":"Organization"}}]`))
		case "/users/alice/repos":
			_, _ = w.Write([]byte(`[{"full_name":"alice/dotfiles","default_branch":"main","private":false,"html_url":"https://github.com/alice/dotfiles","owner":{"login":"alice","type":"User"}}]`))
		case "/user/repos":
			// /user/repos is the new source-of-truth for ListAccounts.
			// owner.type tells the dashboard whether to render this as
			// a user or org account in the dropdown.
			_, _ = w.Write([]byte(`[
				{"full_name":"alice/personal","default_branch":"main","private":false,"html_url":"https://github.com/alice/personal","owner":{"login":"alice","type":"User"}},
				{"full_name":"acme/shared","default_branch":"main","private":true,"html_url":"https://github.com/acme/shared","owner":{"login":"acme","type":"Organization"}}
			]`))
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(stub.Close)

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
		GithubAPIBaseURL:  stub.URL,
	})

	// Create token.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{
		"name": "personal", "token": "ghp_x",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create token: %d (%s)", rr.Code, rr.Body.String())
	}
	var created githubTokenPayload
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// List accounts.
	rr = doJSON(t, router, http.MethodGet, "/api/v1/github-tokens/"+created.ID+"/accounts", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list accounts: %d (%s)", rr.Code, rr.Body.String())
	}
	var accResp struct {
		Accounts []struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"accounts"`
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &accResp)
	if accResp.Total != 2 ||
		accResp.Accounts[0].Login != "alice" || accResp.Accounts[0].Type != "user" ||
		accResp.Accounts[1].Login != "acme" || accResp.Accounts[1].Type != "org" {
		t.Fatalf("unexpected accounts payload: %+v", accResp)
	}

	// Account-scoped repos (org).
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/github-tokens/"+created.ID+"/repos?account=acme&account_type=org", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("scoped org repos: %d", rr.Code)
	}
	if hitPath != "/orgs/acme/repos" {
		t.Fatalf("expected GitHub /orgs/acme/repos hit, got %q", hitPath)
	}

	// Account-scoped repos (user).
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/github-tokens/"+created.ID+"/repos?account=alice&account_type=user", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("scoped user repos: %d", rr.Code)
	}
	if hitPath != "/users/alice/repos" {
		t.Fatalf("expected GitHub /users/alice/repos hit, got %q", hitPath)
	}

	// No account → legacy aggregated /user/repos.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/github-tokens/"+created.ID+"/repos", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("unscoped: %d", rr.Code)
	}
	if hitPath != "/user/repos" {
		t.Fatalf("expected GitHub /user/repos hit, got %q", hitPath)
	}

	// account without account_type → 422.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/github-tokens/"+created.ID+"/repos?account=acme", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing account_type should 422, got %d", rr.Code)
	}
}

func TestGithubTokens_RejectMissingFields(t *testing.T) {
	router := workspaceRouter(t, true)

	// Missing token value.
	rr := doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{"name": "x"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on missing token, got %d", rr.Code)
	}
	// Missing name.
	rr = doJSON(t, router, http.MethodPost, "/api/v1/github-tokens", map[string]any{"token": "y"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on missing name, got %d", rr.Code)
	}
}
