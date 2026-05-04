package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// dbOpenMemory + seedless* are tiny shims for tests that need wired
// services against an empty database (no admin seeded).
func dbOpenMemory(t *testing.T) (*sql.DB, error) {
	d, err := apidb.Open(":memory:")
	if err == nil {
		t.Cleanup(func() { _ = d.Close() })
	}
	return d, err
}
func seedlessUsers(d *sql.DB) *users.Service       { return users.New(d) }
func seedlessSessions(d *sql.DB) *sessions.Service { return sessions.New(d) }
func seedlessAPIKeys(d *sql.DB) *apikeys.Service   { return apikeys.New(d) }

// loginRR runs POST /api/v1/auth/login against router and returns the
// response recorder. Centralised because every auth-flow test starts the
// same way.
func loginRR(t *testing.T, router http.Handler, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func sessionCookie(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessions.CookieName {
			return c.Value
		}
	}
	return ""
}

// withCookie adds a session cookie to req for tests that simulate a
// logged-in browser.
func withCookie(req *http.Request, cookieValue string) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessions.CookieName, Value: cookieValue})
	return req
}

func TestLogin_HappyPath(t *testing.T) {
	f := newAuthFixture(t)
	rr := loginRR(t, f.Router, "admin@example.com", "secret-password")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	if sessionCookie(rr) == "" {
		t.Errorf("Set-Cookie missing %s", sessions.CookieName)
	}
	var body struct {
		User struct {
			ID    string
			Email string
		}
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.User.Email != "admin@example.com" {
		t.Errorf("user.email = %q", body.User.Email)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	f := newAuthFixture(t)
	rr := loginRR(t, f.Router, "admin@example.com", "WRONG")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestLogin_RateLimit_BlocksAfterRepeatedFailures fires N+1 wrong-password
// attempts at the live router and expects the (N+1)th to be 429 with a
// Retry-After header. The fixture's loginLimiter starts with the default
// policy (5/15min per email); the test follows that contract.
func TestLogin_RateLimit_BlocksAfterRepeatedFailures(t *testing.T) {
	f := newAuthFixture(t)
	for i := range 5 {
		rr := loginRR(t, f.Router, "admin@example.com", "WRONG")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rr.Code)
		}
	}
	rr := loginRR(t, f.Router, "admin@example.com", "WRONG")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429 (body=%s)", rr.Code, rr.Body.String())
	}
	if ra := rr.Result().Header.Get("Retry-After"); ra == "" {
		t.Errorf("Retry-After header missing on 429")
	}
	// Even a CORRECT password is now blocked — the limiter checks before
	// authenticating, which is what we want for credential-stuffing
	// resistance.
	rr = loginRR(t, f.Router, "admin@example.com", "secret-password")
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("correct password while rate-limited status = %d, want 429", rr.Code)
	}
}

// TestLogin_RateLimit_ResetOnSuccess verifies that a user who fat-fingers
// their password a few times then logs in correctly does not stay locked
// out — the per-(IP, email) counter clears on a successful authentication.
func TestLogin_RateLimit_ResetOnSuccess(t *testing.T) {
	f := newAuthFixture(t)
	for i := range 4 {
		rr := loginRR(t, f.Router, "admin@example.com", "WRONG")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("warmup attempt %d: status = %d", i+1, rr.Code)
		}
	}
	rr := loginRR(t, f.Router, "admin@example.com", "secret-password")
	if rr.Code != http.StatusOK {
		t.Fatalf("correct password status = %d, want 200", rr.Code)
	}
	// Counter is now reset; we should be able to fail again 4 more times
	// before the next 429 (without the reset, we would hit 5 immediately).
	for i := range 4 {
		rr := loginRR(t, f.Router, "admin@example.com", "WRONG")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: status = %d, want 401", i+1, rr.Code)
		}
	}
}

func TestLogin_MissingFields(t *testing.T) {
	f := newAuthFixture(t)
	rr := loginRR(t, f.Router, "", "")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
}

func TestMe_WithSession(t *testing.T) {
	f := newAuthFixture(t)
	login := loginRR(t, f.Router, "admin@example.com", "secret-password")
	cookie := sessionCookie(login)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		User       map[string]any `json:"user"`
		AuthMethod string         `json:"auth_method"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.AuthMethod != "session" {
		t.Errorf("auth_method = %q, want 'session'", body.AuthMethod)
	}
}

func TestMe_WithBearer(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+f.FullKey)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		AuthMethod string `json:"auth_method"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.AuthMethod != "api_key" {
		t.Errorf("auth_method = %q, want 'api_key'", body.AuthMethod)
	}
}

func TestLogout_DropsSession(t *testing.T) {
	f := newAuthFixture(t)
	login := loginRR(t, f.Router, "admin@example.com", "secret-password")
	cookie := sessionCookie(login)

	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rr.Code)
	}
	// Subsequent /me with the same cookie must 401.
	req = withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), cookie)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("/me after logout status = %d, want 401", rr.Code)
	}
}

func TestChangePassword_RotatesOtherSessions(t *testing.T) {
	f := newAuthFixture(t)
	// Two parallel logins (two different "browsers").
	cookieA := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	cookieB := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	body, _ := json.Marshal(map[string]string{
		"current_password": "secret-password",
		"new_password":     "an-even-better-password",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", bytes.NewReader(body)), cookieA)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("change-password status = %d (body=%s)", rr.Code, rr.Body.String())
	}

	// Cookie A still works.
	req = withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), cookieA)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("cookie A status = %d, want 200 (current session preserved)", rr.Code)
	}

	// Cookie B must now 401.
	req = withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), cookieB)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("cookie B status = %d, want 401 (other sessions revoked)", rr.Code)
	}

	// New password authenticates.
	rr = loginRR(t, f.Router, "admin@example.com", "an-even-better-password")
	if rr.Code != http.StatusOK {
		t.Errorf("login with new password status = %d", rr.Code)
	}
}

func TestBootstrapStatus_True(t *testing.T) {
	// Wire services against an empty users table (no Create call) — this
	// is the same shape as a brand-new deployment.
	database, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := NewRouter(Deps{
		DB:            database,
		Users:         seedlessUsers(database),
		Sessions:      seedlessSessions(database),
		APIKeys:       seedlessAPIKeys(database),
		ServerVersion: "0.0.0-test",
		AuthDisabled:  true, // skip the auth gate so we can hit the public endpoint without setup
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/bootstrap-status", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"needs_bootstrap":true`) {
		t.Errorf("body = %s, want needs_bootstrap:true", rr.Body.String())
	}
}

func TestBootstrapStatus_False(t *testing.T) {
	f := newAuthFixture(t) // seeds an admin
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/bootstrap-status", nil)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"needs_bootstrap":false`) {
		t.Errorf("body = %s, want needs_bootstrap:false", rr.Body.String())
	}
}

// --- Admin user CRUD via HTTP ---

func TestCreateUser_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)
	cookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	body, _ := json.Marshal(map[string]string{
		"email": "viewer@example.com", "initial_password": "viewerpass1", "role": "viewer",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(body)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin POST status = %d (body=%s)", rr.Code, rr.Body.String())
	}

	// Now try the same request as the viewer — expect 403.
	viewerCookie := sessionCookie(loginRR(t, f.Router, "viewer@example.com", "viewerpass1"))
	body, _ = json.Marshal(map[string]string{
		"email": "another@example.com", "initial_password": "anotherpass1", "role": "viewer",
	})
	req = withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(body)), viewerCookie)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer POST status = %d, want 403", rr.Code)
	}
}

// --- API key CRUD via HTTP ---

func TestApiKey_CreateListRevokeFlow(t *testing.T) {
	f := newAuthFixture(t)
	cookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	body, _ := json.Marshal(map[string]string{"name": "ci-bot"})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewReader(body)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create key status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		FullKey string `json:"full_key"`
		ApiKey  struct{ ID string } `json:"api_key"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.FullKey == "" {
		t.Fatalf("create key did not return full_key")
	}

	// Use the key — must auth.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+created.FullKey)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Bearer with new key status = %d", rr.Code)
	}

	// Revoke.
	req = withCookie(httptest.NewRequest(http.MethodDelete, "/api/v1/api-keys/"+created.ApiKey.ID, nil), cookie)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", rr.Code)
	}

	// Same key now 401.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+created.FullKey)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("revoked key status = %d, want 401", rr.Code)
	}
}

func TestApiKey_ListForOwnerHidesOthers(t *testing.T) {
	f := newAuthFixture(t)
	// Seed a viewer + their own key directly via the underlying services.
	v, err := f.Deps.Users.Create(context.Background(), "v@b.com", "viewerpass1", users.RoleViewer, false)
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	if _, _, err := f.Deps.APIKeys.Generate(context.Background(), v.ID, "viewer-only-key"); err != nil {
		t.Fatalf("seed viewer key: %v", err)
	}

	// Login as viewer — list must contain only their key, not the
	// admin's seed key.
	cookie := sessionCookie(loginRR(t, f.Router, "v@b.com", "viewerpass1"))
	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/api-keys", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Total != 1 {
		t.Errorf("viewer sees total = %d, want 1 (own key only)", body.Total)
	}
}

// TestProjectMutations_AdminOnly verifies that PATCH and DELETE on a
// project are gated behind the admin role. POST /index/cancel is
// intentionally NOT gated — see comment on IndexCancel for why — and is
// covered separately by TestIndexCancel_AnyAuthenticatedUser.
func TestProjectMutations_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Admin creates a project to act on. CreateProject is intentionally
	// not admin-only — viewers can register their own projects.
	createBody, _ := json.Marshal(map[string]string{"host_path": "/tmp/test-proj"})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(createBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("admin create project status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var created struct{ PathHash string `json:"path_hash"` }
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.PathHash == "" {
		t.Fatalf("created project payload missing path_hash: %s", rr.Body.String())
	}

	// Seed a viewer + log in as them.
	viewerBody, _ := json.Marshal(map[string]string{
		"email": "viewer@example.com", "initial_password": "viewerpass1", "role": "viewer",
	})
	req = withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(viewerBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed viewer status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	viewerCookie := sessionCookie(loginRR(t, f.Router, "viewer@example.com", "viewerpass1"))

	// Each gated endpoint must 403 for the viewer, then succeed for the
	// admin. PATCH first (mutates settings), DELETE last (destructive).
	cases := []struct {
		name        string
		method      string
		path        string
		body        []byte
		adminStatus int
	}{
		{
			name:   "patch settings",
			method: http.MethodPatch,
			path:   "/api/v1/projects/" + created.PathHash,
			body: mustJSON(t, map[string]any{
				"settings": map[string]any{"exclude_patterns": []string{"vendor"}},
			}),
			adminStatus: http.StatusOK,
		},
		{
			name:        "delete project",
			method:      http.MethodDelete,
			path:        "/api/v1/projects/" + created.PathHash,
			adminStatus: http.StatusNoContent,
		},
	}
	for _, c := range cases {
		t.Run(c.name+"/viewer-forbidden", func(t *testing.T) {
			req := withCookie(httptest.NewRequest(c.method, c.path, bytes.NewReader(c.body)), viewerCookie)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			f.Router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("viewer %s %s status = %d, want 403", c.method, c.path, rr.Code)
			}
		})
		t.Run(c.name+"/admin-allowed", func(t *testing.T) {
			req := withCookie(httptest.NewRequest(c.method, c.path, bytes.NewReader(c.body)), adminCookie)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			f.Router.ServeHTTP(rr, req)
			if rr.Code != c.adminStatus {
				t.Errorf("admin %s %s status = %d, want %d (body=%s)", c.method, c.path, rr.Code, c.adminStatus, rr.Body.String())
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestIndexCancel_AnyAuthenticatedUser pins the policy that /index/cancel
// is open to any authenticated user. The CLI calls cancel in defer-cleanup
// on early exit (Ctrl-C, network drop), and gating it behind admin would
// strand viewer-owned run locks until the 1-hour TTL.
func TestIndexCancel_AnyAuthenticatedUser(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Seed a viewer + a project they can cancel against.
	viewerBody, _ := json.Marshal(map[string]string{
		"email": "viewer@example.com", "initial_password": "viewerpass1", "role": "viewer",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(viewerBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed viewer status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	viewerCookie := sessionCookie(loginRR(t, f.Router, "viewer@example.com", "viewerpass1"))

	createBody, _ := json.Marshal(map[string]string{"host_path": "/tmp/cancel-test"})
	req = withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(createBody)), viewerCookie)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("viewer create project status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var created struct{ PathHash string `json:"path_hash"` }
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Viewer cancels — must NOT 403 (idempotent 200 even when no run is active).
	req = withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+created.PathHash+"/index/cancel", nil), viewerCookie)
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("viewer cancel status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestListUsers_IncludesStats — admin-list payload must carry the three
// aggregate columns the dashboard's Users table renders. Round-trip the
// JSON to ensure field names match the OpenAPI contract verbatim.
func TestListUsers_IncludesStats(t *testing.T) {
	f := newAuthFixture(t)
	cookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Seed a viewer + give them an api-key so the row is non-trivial.
	v, err := f.Deps.Users.Create(context.Background(), "v@b.com", "viewerpass1", users.RoleViewer, false)
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	if _, _, err := f.Deps.APIKeys.Generate(context.Background(), v.ID, "k"); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rr.Code, rr.Body.String())
	}

	var body struct {
		Total int `json:"total"`
		Users []struct {
			Email               string  `json:"email"`
			LastLoginAt         *string `json:"last_login_at"`
			ActiveSessionsCount int     `json:"active_sessions_count"`
			ApiKeysCount        int     `json:"api_keys_count"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rr.Body.String())
	}
	if body.Total != 2 {
		t.Fatalf("total = %d, want 2", body.Total)
	}
	by := map[string]int{}
	var viewerRow *struct {
		Email               string  `json:"email"`
		LastLoginAt         *string `json:"last_login_at"`
		ActiveSessionsCount int     `json:"active_sessions_count"`
		ApiKeysCount        int     `json:"api_keys_count"`
	}
	for i := range body.Users {
		by[body.Users[i].Email] = i
		if body.Users[i].Email == "v@b.com" {
			viewerRow = &body.Users[i]
		}
	}
	if viewerRow == nil {
		t.Fatalf("viewer row missing in payload: %s", rr.Body.String())
	}
	if viewerRow.ApiKeysCount != 1 {
		t.Errorf("viewer api_keys_count = %d, want 1", viewerRow.ApiKeysCount)
	}
	if viewerRow.LastLoginAt != nil {
		t.Errorf("viewer last_login_at = %v, want null (never logged in)", *viewerRow.LastLoginAt)
	}
	// Admin: just-logged-in via loginRR → 1 active session.
	admin := body.Users[by["admin@example.com"]]
	if admin.ActiveSessionsCount < 1 {
		t.Errorf("admin active_sessions_count = %d, want >=1", admin.ActiveSessionsCount)
	}
	if admin.LastLoginAt == nil {
		t.Errorf("admin last_login_at should be set after login")
	}
}
