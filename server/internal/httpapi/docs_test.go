package httpapi

import (
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

// newDocsTestServer wires a router with full auth services so we can
// verify that the docs endpoints are reachable WITHOUT credentials —
// otherwise a passing test could just be the dev-mode skip in
// requireAuth.
func newDocsTestServer(t *testing.T) http.Handler {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewRouter(Deps{
		DB:            database,
		ServerVersion: "0.0.0-test",
		APIVersion:    "v1",
		Users:         users.New(database),
		Sessions:      sessions.New(database),
		APIKeys:       apikeys.New(database),
	})
}

// TestDocs_IndexServesHTML verifies GET /docs returns the Swagger UI shell
// without requiring an Authorization header.
func TestDocs_IndexServesHTML(t *testing.T) {
	srv := newDocsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (docs must be public)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Swagger UI") {
		t.Errorf("body missing 'Swagger UI' marker; first 200B: %q", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "/openapi.json") {
		t.Errorf("body should reference /openapi.json as spec source")
	}
}

// TestDocs_StaticAssetServed verifies the JS bundle is reachable under
// /docs/<asset>. Pulls swagger-ui-bundle.js because it's the largest
// asset and the most-likely-to-break in any future refactor.
func TestDocs_StaticAssetServed(t *testing.T) {
	srv := newDocsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/docs/swagger-ui-bundle.js", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want application/javascript prefix", ct)
	}
	if rr.Body.Len() < 1000 {
		t.Errorf("bundle body too small (%d bytes) — embed may have failed", rr.Body.Len())
	}
}

// TestDocs_StaticAssetNotFound verifies that requests for a non-existent
// asset return 404 rather than panicking or echoing back unrelated content.
func TestDocs_StaticAssetNotFound(t *testing.T) {
	srv := newDocsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/docs/does-not-exist.js", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestOpenAPISpec_ServesValidJSON verifies the spec endpoint returns valid
// JSON with the expected info.title and info.version. This catches both
// embed regressions (spec missing from binary) and accidental contract
// drift (info section overwritten).
func TestOpenAPISpec_ServesValidJSON(t *testing.T) {
	srv := newDocsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (openapi.json must be public)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Errorf("openapi version = %q, want 3.x", doc.OpenAPI)
	}
	if doc.Info.Title != "cix-server API" {
		t.Errorf("info.title = %q, want %q", doc.Info.Title, "cix-server API")
	}
	if doc.Info.Version != "v1" {
		t.Errorf("info.version = %q, want v1", doc.Info.Version)
	}
	if len(doc.Paths) < 13 {
		t.Errorf("paths count = %d, expected at least 13", len(doc.Paths))
	}
}

// TestDocs_IsPublic — defense-in-depth: explicitly verify the three docs
// endpoints work WITHOUT an Authorization header, even though
// TestAuth_StatusRejectsMissingKey already covers the inverse case for the
// API routes.
func TestDocs_IsPublic(t *testing.T) {
	srv := newDocsTestServer(t)
	for _, p := range []string{"/docs", "/docs/swagger-ui-bundle.js", "/openapi.json"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("%s returned 401 — must be public", p)
		}
	}
}
