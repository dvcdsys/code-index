package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	apidb "github.com/dvcdsys/code-index/server/internal/db"
)

func newTestServer(t *testing.T) http.Handler {
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
		Backend:        "go",
		EmbeddingModel: "test-model",
	})
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("X-Server-Version"); got != "0.0.0-test" {
		t.Errorf("X-Server-Version = %q", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v (body=%s)", err, rr.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %v", body["status"])
	}
}

func TestStatusEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, k := range []string{"status", "backend", "server_version", "api_version", "projects", "active_indexing_jobs"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing field %q in status response: %v", k, body)
		}
	}
	if body["backend"] != "go" {
		t.Errorf("backend = %v, want go", body["backend"])
	}
	if body["server_version"] != "0.0.0-test" {
		t.Errorf("server_version = %v", body["server_version"])
	}
	if body["projects"].(float64) != 0 {
		t.Errorf("projects = %v, want 0", body["projects"])
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := newTestServer(t)
	// /api/v1/nonexistent is not registered by any handler.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		_, _ = io.ReadAll(rr.Body)
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
