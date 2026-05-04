package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// authTestFixture bundles a router plus the seeded admin user + a fresh
// API key for that user. Used by every test that needs to exercise the
// real auth path (cookie OR Bearer) instead of bypassing via
// AuthDisabled=true.
//
// Deps is exposed so tests can poke directly at the services to seed
// extra fixtures (other users, extra keys, etc.) without going through
// HTTP for setup-time arrangements.
type authTestFixture struct {
	Router  http.Handler
	Deps    Deps
	UserID  string
	FullKey string
}

func newAuthFixture(t *testing.T) *authTestFixture {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	usrSvc := users.New(database)
	sessSvc := sessions.New(database)
	akSvc := apikeys.New(database)

	u, err := usrSvc.Create(context.Background(), "admin@example.com", "secret-password", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	full, _, err := akSvc.Generate(context.Background(), u.ID, "test-key")
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	deps := Deps{
		DB:             database,
		ServerVersion:  "0.0.0-test",
		APIVersion:     "v1",
		EmbeddingModel: "test-model",
		Users:          usrSvc,
		Sessions:       sessSvc,
		APIKeys:        akSvc,
	}
	return &authTestFixture{Router: NewRouter(deps), Deps: deps, UserID: u.ID, FullKey: full}
}

// newAuthDisabledServer mirrors the old "empty key + AuthDisabled" path.
// Some legacy tests still want a router that lets every request through
// without any wiring — this is the single helper that supports it.
func newAuthDisabledServer(t *testing.T) http.Handler {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewRouter(Deps{
		DB:             database,
		ServerVersion:  "0.0.0-test",
		APIVersion:     "v1",
		EmbeddingModel: "test-model",
		AuthDisabled:   true,
	})
}

func TestAuth_HealthIsPublic(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (health must be public)", rr.Code)
	}
}

func TestAuth_StatusRejectsMissingKey(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v (body=%s)", err, rr.Body.String())
	}
	if body["detail"] != "Authentication required" {
		t.Errorf("detail = %v, want 'Authentication required'", body["detail"])
	}
}

func TestAuth_StatusRejectsWrongKey(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer cix_not-the-right-key-at-all-1234567890ab")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAuth_StatusAcceptsCorrectKey(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+f.FullKey)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestAuth_DisabledFlagSkipsCheck — explicit dev-mode opt-out via
// AuthDisabled. With the flag on, NewRouter omits the requireAuth
// middleware entirely so every endpoint succeeds without credentials.
func TestAuth_DisabledFlagSkipsCheck(t *testing.T) {
	srv := newAuthDisabledServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with AuthDisabled=true", rr.Code)
	}
}

// TestLimitBodySize_RejectsLargePayloadAtLogin sends a request with a
// declared Content-Length above the default 1 MiB cap and expects 413
// before the login handler ever runs. Crucially, this fires at the
// public /auth/login path so an unauthenticated attacker cannot force
// the server to read an unbounded body.
func TestLimitBodySize_RejectsLargePayloadAtLogin(t *testing.T) {
	f := newAuthFixture(t)
	// The body itself doesn't matter — the middleware checks
	// Content-Length first and returns 413 without reading.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", http.NoBody)
	req.ContentLength = (2 << 20) // 2 MiB > 1 MiB default
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestLimitBodySize_AllowsLargerIndexingPayload confirms the per-route
// override: the indexing endpoint accepts payloads up to 32 MiB, well
// past the 1 MiB default. Sending exactly 2 MiB should pass the size
// check and reach the auth handler (which 401s for an unauthenticated
// request — but past the 413 gate, which is what we're testing).
func TestLimitBodySize_AllowsLargerIndexingPayload(t *testing.T) {
	f := newAuthFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/abc123/index/files", http.NoBody)
	req.ContentLength = (2 << 20) // 2 MiB
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("indexing endpoint should not 413 at 2 MiB (got %d)", rr.Code)
	}
}
