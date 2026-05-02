package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apidb "github.com/dvcdsys/code-index/server/internal/db"
)

// newAuthTestServer builds a router wired with the given API key. An empty
// key now requires AuthDisabled=true to match production semantics — the
// router would otherwise panic from requireAPIKey's empty-key guard.
func newAuthTestServer(t *testing.T, apiKey string) http.Handler {
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
		APIKey:         apiKey,
		AuthDisabled:   apiKey == "",
	})
}

func TestAuth_HealthIsPublic(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (health must be public)", rr.Code)
	}
}

func TestAuth_StatusRejectsMissingKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v (body=%s)", err, rr.Body.String())
	}
	if body["detail"] != "Invalid or missing API key" {
		t.Errorf("detail = %v, want %q", body["detail"], "Invalid or missing API key")
	}
}

func TestAuth_StatusRejectsWrongKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer not-the-right-key")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAuth_StatusAcceptsCorrectKey(t *testing.T) {
	srv := newAuthTestServer(t, "secret-key")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestAuth_DisabledFlagSkipsCheck — explicit dev-mode opt-out via the
// AuthDisabled Deps flag (which is itself fed by CIX_AUTH_DISABLED). With
// the flag on, NewRouter omits the requireAPIKey middleware entirely, so
// even unauthenticated routes succeed.
func TestAuth_DisabledFlagSkipsCheck(t *testing.T) {
	srv := newAuthTestServer(t, "") // helper sets AuthDisabled=true on empty key
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with AuthDisabled=true", rr.Code)
	}
}
