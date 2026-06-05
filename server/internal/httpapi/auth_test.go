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
		"email": "viewer@example.com", "initial_password": "viewerpass1", "role": "user",
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
		"email": "another@example.com", "initial_password": "anotherpass1", "role": "user",
	})
	req = withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(body)), viewerCookie)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer POST status = %d, want 403", rr.Code)
	}
}

// resetPasswordRR POSTs to the admin reset-password endpoint as the caller
// holding `cookie` and returns the recorder.
func resetPasswordRR(t *testing.T, router http.Handler, cookie, userID, newPassword string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"new_password": newPassword})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+userID+"/reset-password", bytes.NewReader(body)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestResetUserPassword_AdminFlow(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Seed a target user who is NOT flagged to change their password (so we
	// can prove the reset sets the flag).
	target, err := f.Deps.Users.Create(context.Background(), "target@example.com", "oldpassword1", users.RoleUser, false)
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	// The target logs in once — gives us a live session to prove revocation.
	targetCookie := sessionCookie(loginRR(t, f.Router, "target@example.com", "oldpassword1"))
	if targetCookie == "" {
		t.Fatalf("target login did not set a cookie")
	}

	// Admin resets the target's password.
	rr := resetPasswordRR(t, f.Router, adminCookie, target.ID, "freshpass123")
	if rr.Code != http.StatusOK {
		t.Fatalf("admin reset status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var payload struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &payload)
	if !payload.MustChangePassword {
		t.Errorf("response must_change_password = false, want true")
	}

	// The old password no longer works; the new temp password does.
	if rr := loginRR(t, f.Router, "target@example.com", "oldpassword1"); rr.Code == http.StatusOK {
		t.Errorf("login with OLD password succeeded, want failure")
	}
	relogin := loginRR(t, f.Router, "target@example.com", "freshpass123")
	if relogin.Code != http.StatusOK {
		t.Fatalf("login with NEW password status = %d (body=%s)", relogin.Code, relogin.Body.String())
	}
	var loginBody struct {
		User struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	_ = json.Unmarshal(relogin.Body.Bytes(), &loginBody)
	if !loginBody.User.MustChangePassword {
		t.Errorf("login payload must_change_password = false, want true")
	}

	// The target's PRE-EXISTING session was revoked.
	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), targetCookie)
	meRR := httptest.NewRecorder()
	f.Router.ServeHTTP(meRR, req)
	if meRR.Code != http.StatusUnauthorized {
		t.Errorf("pre-reset session /auth/me status = %d, want 401", meRR.Code)
	}
}

func TestResetUserPassword_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)

	// Seed a non-admin who will both be the target and attempt the reset.
	viewer, err := f.Deps.Users.Create(context.Background(), "viewer@example.com", "viewerpass1", users.RoleUser, false)
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	viewerCookie := sessionCookie(loginRR(t, f.Router, "viewer@example.com", "viewerpass1"))

	rr := resetPasswordRR(t, f.Router, viewerCookie, viewer.ID, "newpass12345")
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer reset status = %d, want 403", rr.Code)
	}
}

func TestResetUserPassword_CanResetAnotherAdmin(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	other, err := f.Deps.Users.Create(context.Background(), "other-admin@example.com", "otheradmin1", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed other admin: %v", err)
	}
	rr := resetPasswordRR(t, f.Router, adminCookie, other.ID, "resetadmin123")
	if rr.Code != http.StatusOK {
		t.Errorf("reset another admin status = %d (body=%s), want 200", rr.Code, rr.Body.String())
	}
}

func TestResetUserPassword_Validation(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Unknown user id → 404.
	if rr := resetPasswordRR(t, f.Router, adminCookie, "does-not-exist", "validpass123"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
	}

	// Seed a real target, then send a too-short password → 422.
	target, err := f.Deps.Users.Create(context.Background(), "shortpw@example.com", "oldpassword1", users.RoleUser, false)
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if rr := resetPasswordRR(t, f.Router, adminCookie, target.ID, "short"); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("short password status = %d, want 422 (body=%s)", rr.Code, rr.Body.String())
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
		FullKey string              `json:"full_key"`
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
	v, err := f.Deps.Users.Create(context.Background(), "v@b.com", "viewerpass1", users.RoleUser, false)
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
// TestProjectMutations_OwnerOrAdmin pins the auth model for PATCH/DELETE on a
// project: the owner or an admin may mutate it; a different regular user cannot
// even see it (404, hiding existence). CreateProject sets owner = caller.
func TestProjectMutations_OwnerOrAdmin(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Seed a regular user (alice) and log in.
	aliceBody, _ := json.Marshal(map[string]string{
		"email": "alice@example.com", "initial_password": "alicepass1", "role": "user",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(aliceBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed alice status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	aliceCookie := sessionCookie(loginRR(t, f.Router, "alice@example.com", "alicepass1"))

	createProject := func(cookie, hostPath string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"host_path": hostPath})
		req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(body)), cookie)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create project %s status = %d (body=%s)", hostPath, rr.Code, rr.Body.String())
		}
		var created struct {
			PathHash string `json:"path_hash"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		if created.PathHash == "" {
			t.Fatalf("created project missing path_hash: %s", rr.Body.String())
		}
		return created.PathHash
	}

	do := func(cookie, method, path string, body []byte) int {
		t.Helper()
		req := withCookie(httptest.NewRequest(method, path, bytes.NewReader(body)), cookie)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		return rr.Code
	}

	patchBody := mustJSON(t, map[string]any{
		"settings": map[string]any{"exclude_patterns": []string{"vendor"}},
	})

	adminProj := createProject(adminCookie, "/tmp/admin-proj")
	aliceProj := createProject(aliceCookie, "/tmp/alice-proj")

	t.Run("non-owner cannot patch (404 hides existence)", func(t *testing.T) {
		if code := do(aliceCookie, http.MethodPatch, "/api/v1/projects/"+adminProj, patchBody); code != http.StatusNotFound {
			t.Errorf("alice PATCH admin's project = %d, want 404", code)
		}
	})
	t.Run("non-owner cannot delete (404 hides existence)", func(t *testing.T) {
		if code := do(aliceCookie, http.MethodDelete, "/api/v1/projects/"+adminProj, nil); code != http.StatusNotFound {
			t.Errorf("alice DELETE admin's project = %d, want 404", code)
		}
	})
	t.Run("owner can patch own project", func(t *testing.T) {
		if code := do(aliceCookie, http.MethodPatch, "/api/v1/projects/"+aliceProj, patchBody); code != http.StatusOK {
			t.Errorf("alice PATCH own project = %d, want 200", code)
		}
	})
	t.Run("admin can patch another user's project", func(t *testing.T) {
		if code := do(adminCookie, http.MethodPatch, "/api/v1/projects/"+aliceProj, patchBody); code != http.StatusOK {
			t.Errorf("admin PATCH alice's project = %d, want 200", code)
		}
	})
	t.Run("owner can delete own project", func(t *testing.T) {
		ownDel := createProject(aliceCookie, "/tmp/alice-proj-2")
		if code := do(aliceCookie, http.MethodDelete, "/api/v1/projects/"+ownDel, nil); code != http.StatusNoContent {
			t.Errorf("alice DELETE own project = %d, want 204", code)
		}
	})
	t.Run("admin can delete another user's project", func(t *testing.T) {
		if code := do(adminCookie, http.MethodDelete, "/api/v1/projects/"+aliceProj, nil); code != http.StatusNoContent {
			t.Errorf("admin DELETE alice's project = %d, want 204", code)
		}
	})
}

// TestExternalAndTokenEndpoints_AdminOnly locks the review's top findings:
// external-project administration and GitHub-token management are admin-only,
// so a regular user is rejected with 403 (the admin gate runs before any
// service-availability check, so this holds regardless of wiring).
func TestExternalAndTokenEndpoints_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	userBody, _ := json.Marshal(map[string]string{
		"email": "bob@example.com", "initial_password": "bobpass1234", "role": "user",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed user status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	userCookie := sessionCookie(loginRR(t, f.Router, "bob@example.com", "bobpass1234"))

	cases := []struct {
		name, method, path string
	}{
		{"add git-repo", http.MethodPost, "/api/v1/git-repos"},
		{"list github-tokens", http.MethodGet, "/api/v1/github-tokens"},
		{"create github-token", http.MethodPost, "/api/v1/github-tokens"},
		{"get project git-repo", http.MethodGet, "/api/v1/projects/deadbeefdeadbeef/git-repo"},
		{"reindex project", http.MethodPost, "/api/v1/projects/deadbeefdeadbeef/reindex"},
		{"force-stop project", http.MethodPost, "/api/v1/projects/deadbeefdeadbeef/force-stop"},
		{"webhook-info", http.MethodGet, "/api/v1/projects/deadbeefdeadbeef/webhook-info"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := withCookie(httptest.NewRequest(c.method, c.path, bytes.NewReader([]byte("{}"))), userCookie)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			f.Router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("user %s %s = %d, want 403", c.method, c.path, rr.Code)
			}
		})
	}
}

// TestLocalProjectDisabled_GatesCreateAndIndex locks the per-user restriction:
// a user with local_project_disabled=true cannot create local projects or
// index/reindex (403), while search/read of their existing projects and
// workspace creation stay allowed. Admins are always exempt.
func TestLocalProjectDisabled_GatesCreateAndIndex(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Seed alice (regular user) and log in.
	aliceBody, _ := json.Marshal(map[string]string{
		"email": "alice@example.com", "initial_password": "alicepass1", "role": "user",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(aliceBody)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed alice status = %d (body=%s)", rr.Code, rr.Body.String())
	}
	var alice struct {
		ID                   string `json:"id"`
		LocalProjectDisabled bool   `json:"local_project_disabled"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &alice)
	if alice.ID == "" {
		t.Fatalf("seed alice missing id: %s", rr.Body.String())
	}
	if alice.LocalProjectDisabled {
		t.Fatalf("new user should default to allowed (local_project_disabled=false)")
	}
	aliceCookie := sessionCookie(loginRR(t, f.Router, "alice@example.com", "alicepass1"))

	do := func(cookie, method, path string, body []byte) int {
		t.Helper()
		var rdr *bytes.Reader
		if body == nil {
			rdr = bytes.NewReader(nil)
		} else {
			rdr = bytes.NewReader(body)
		}
		req := withCookie(httptest.NewRequest(method, path, rdr), cookie)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		return rr.Code
	}
	createProject := func(cookie, hostPath string) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"host_path": hostPath})
		req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(body)), cookie)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		var created struct {
			PathHash string `json:"path_hash"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		return rr.Code, created.PathHash
	}
	setLPD := func(userID string, disabled bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"local_project_disabled": disabled})
		if code := do(adminCookie, http.MethodPatch, "/api/v1/admin/users/"+userID, body); code != http.StatusOK {
			t.Fatalf("admin PATCH local_project_disabled=%v = %d", disabled, code)
		}
	}

	// While allowed, alice can create a local project.
	code, aliceProj := createProject(aliceCookie, "/tmp/alice-lpd")
	if code != http.StatusCreated || aliceProj == "" {
		t.Fatalf("alice create (allowed) = %d, hash=%q", code, aliceProj)
	}

	// Restrict alice.
	setLPD(alice.ID, true)

	t.Run("restricted: create local project is 403", func(t *testing.T) {
		if code, _ := createProject(aliceCookie, "/tmp/alice-lpd-2"); code != http.StatusForbidden {
			t.Errorf("restricted create = %d, want 403", code)
		}
	})
	t.Run("restricted: index/begin is 403", func(t *testing.T) {
		if code := do(aliceCookie, http.MethodPost, "/api/v1/projects/"+aliceProj+"/index/begin", []byte("{}")); code != http.StatusForbidden {
			t.Errorf("restricted index/begin = %d, want 403", code)
		}
	})
	t.Run("restricted: index/files is 403", func(t *testing.T) {
		// Same guard as begin/finish — the gate runs before the body is read,
		// so an empty body still 403s. Covers the mid-protocol upload step.
		if code := do(aliceCookie, http.MethodPost, "/api/v1/projects/"+aliceProj+"/index/files", []byte("{}")); code != http.StatusForbidden {
			t.Errorf("restricted index/files = %d, want 403", code)
		}
	})
	t.Run("restricted: index/finish is 403", func(t *testing.T) {
		if code := do(aliceCookie, http.MethodPost, "/api/v1/projects/"+aliceProj+"/index/finish", []byte(`{"run_id":"x"}`)); code != http.StatusForbidden {
			t.Errorf("restricted index/finish = %d, want 403", code)
		}
	})
	t.Run("restricted: /auth/me reflects the flag", func(t *testing.T) {
		req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil), aliceCookie)
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("me status = %d", rr.Code)
		}
		var me struct {
			User struct {
				LocalProjectDisabled bool `json:"local_project_disabled"`
			} `json:"user"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &me)
		if !me.User.LocalProjectDisabled {
			t.Errorf("me.user.local_project_disabled = false, want true")
		}
	})
	t.Run("restricted: read (index/status) of own project is not gated", func(t *testing.T) {
		// Read access uses requireProjectAccess (the same gate search uses) and
		// is never blocked by local_project_disabled — must not be 403.
		if code := do(aliceCookie, http.MethodGet, "/api/v1/projects/"+aliceProj+"/index/status", nil); code == http.StatusForbidden {
			t.Errorf("restricted index/status = 403, want non-403 (read stays allowed)")
		}
	})
	t.Run("restricted: workspace creation stays allowed", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"name": "alice-ws"})
		if code := do(aliceCookie, http.MethodPost, "/api/v1/workspaces", body); code != http.StatusCreated {
			t.Errorf("restricted workspace create = %d, want 201", code)
		}
	})

	t.Run("admin is exempt even when flagged", func(t *testing.T) {
		// Flag the admin too — admins ignore the restriction.
		setLPD(f.UserID, true)
		if code, hash := createProject(adminCookie, "/tmp/admin-lpd"); code != http.StatusCreated || hash == "" {
			t.Errorf("flagged admin create = %d, want 201 (admins exempt)", code)
		}
		setLPD(f.UserID, false)
	})

	t.Run("re-enabling restores access", func(t *testing.T) {
		setLPD(alice.ID, false)
		if code, hash := createProject(aliceCookie, "/tmp/alice-lpd-3"); code != http.StatusCreated || hash == "" {
			t.Errorf("re-enabled create = %d, want 201", code)
		}
	})
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
		"email": "viewer@example.com", "initial_password": "viewerpass1", "role": "user",
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
	var created struct {
		PathHash string `json:"path_hash"`
	}
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
	v, err := f.Deps.Users.Create(context.Background(), "v@b.com", "viewerpass1", users.RoleUser, false)
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
