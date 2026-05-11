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

	return NewRouter(Deps{
		DB:                d,
		ServerVersion:     "test",
		APIVersion:        "v1",
		Backend:           "go",
		Logger:            nil,
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		WorkspacesEnabled: enabled,
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		GithubAPIBaseURL:  fakeGithubAPI(t),
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

func TestWorkspaces_DisabledByDefault(t *testing.T) {
	router := workspaceRouter(t, false)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when feature disabled, got %d (body: %s)", rr.Code, rr.Body.String())
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
		WorkspacesEnabled: true,
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
