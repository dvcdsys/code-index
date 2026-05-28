package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/embeddingscfg"
)

// TestListEmbeddingProviders_AdminSeesAllThree confirms the registered
// kinds (ollama, openai, voyage) show up with their schemas and the
// secret-env readiness flags.
func TestListEmbeddingProviders_AdminSeesAllThree(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := adminCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/embedding-providers", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}

	var body struct {
		Providers []struct {
			Kind       string          `json:"kind"`
			Schema     json.RawMessage `json:"schema"`
			SecretEnvs []struct {
				Name string `json:"name"`
				Set  bool   `json:"set"`
			} `json:"secret_envs"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, p := range body.Providers {
		got[p.Kind] = true
		if len(p.Schema) == 0 {
			t.Errorf("kind %s: empty schema", p.Kind)
		}
	}
	for _, want := range []string{"ollama", "openai", "voyage"} {
		if !got[want] {
			t.Errorf("missing kind %q in providers list", want)
		}
	}
}

func TestListEmbeddingProviders_ViewerForbidden(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := viewerCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/embedding-providers", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
}

// The following three tests close the per-endpoint 403 gating gap
// required by the project's auth rule: a non-admin (viewer) caller must
// be rejected on EVERY admin embedding-provider route, not just the list.

func TestGetActiveEmbeddingProvider_ViewerForbidden(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := viewerCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/embedding-providers/active", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestSwitchEmbeddingProvider_ViewerForbidden(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := viewerCookie(t, f)

	body, _ := json.Marshal(map[string]any{"kind": "ollama", "config": map[string]any{}})
	req := withCookie(httptest.NewRequest(http.MethodPut,
		"/api/v1/admin/embedding-providers/active", bytes.NewReader(body)), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestTestEmbeddingProvider_ViewerForbidden(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := viewerCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/embedding-providers/voyage/test", bytes.NewReader([]byte(`{}`))), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestSwitchEmbeddingProvider_RejectsUnknownKind(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := adminCookie(t, f)

	body, _ := json.Marshal(map[string]any{
		"kind":   "nonexistent",
		"config": map[string]any{},
	})
	req := withCookie(httptest.NewRequest(http.MethodPut,
		"/api/v1/admin/embedding-providers/active", bytes.NewReader(body)), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	// EmbeddingSvc is nil in this fixture — handler should still validate
	// kind first when EmbeddingSvc is missing it returns 503 instead.
	// Accept either as long as we don't get 200.
	if rr.Code == http.StatusOK || rr.Code == http.StatusAccepted {
		t.Fatalf("unknown kind accepted: status %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestTestEmbeddingProvider_BadKind(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := adminCookie(t, f)

	body := []byte(`{}`)
	req := withCookie(httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/embedding-providers/garbage/test", bytes.NewReader(body)), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestTestEmbeddingProvider_MissingAPIKey(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.EmbeddingsCfg = embeddingscfg.New(f.Deps.DB)
	f.Router = NewRouter(f.Deps)
	cookie := adminCookie(t, f)

	// Submit a voyage config naming an env var that almost certainly isn't
	// set in CI. Expect a 400 with ErrMissingAPIKey wrapped in the body.
	body, _ := json.Marshal(map[string]any{
		"api_key_env":      "CIX_TEST_MISSING_VOYAGE_KEY_XYZ",
		"model":            "voyage-3",
		"output_dimension": 1024,
		"output_dtype":     "float",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/embedding-providers/voyage/test", bytes.NewReader(body)), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}
