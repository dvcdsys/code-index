package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/config"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/runtimecfg"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// adminFixture extends authTestFixture with a wired runtimecfg.Service so
// the admin handlers under test see a real DB-backed config layer.
type adminFixture struct {
	*authTestFixture
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	usrSvc := users.New(database)
	sessSvc := sessions.New(database)
	akSvc := apikeys.New(database)

	admin, err := usrSvc.Create(context.Background(), "admin@example.com", "secret-password", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	viewer, err := usrSvc.Create(context.Background(), "viewer@example.com", "secret-password", users.RoleViewer, false)
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	_ = viewer

	envCfg := &config.Config{
		EmbeddingModel:          "env/model",
		LlamaCtxSize:            4096,
		LlamaNGpuLayers:         8,
		LlamaNThreads:           4,
		MaxEmbeddingConcurrency: 2,
		LlamaBatchSize:          1024,
		GGUFCacheDir:            t.TempDir(),
	}

	deps := Deps{
		DB:             database,
		ServerVersion:  "0.0.0-test",
		APIVersion:     "v1",
		EmbeddingModel: envCfg.EmbeddingModel,
		Users:          usrSvc,
		Sessions:       sessSvc,
		APIKeys:        akSvc,
		RuntimeCfg:     runtimecfg.New(database, envCfg),
	}
	return &adminFixture{
		authTestFixture: &authTestFixture{
			Router:  NewRouter(deps),
			Deps:    deps,
			UserID:  admin.ID,
			FullKey: "",
		},
	}
}

func adminCookie(t *testing.T, f *adminFixture) string {
	t.Helper()
	rr := loginRR(t, f.Router, "admin@example.com", "secret-password")
	if rr.Code != http.StatusOK {
		t.Fatalf("admin login failed: %d (%s)", rr.Code, rr.Body.String())
	}
	c := sessionCookie(rr)
	if c == "" {
		t.Fatal("admin session cookie missing")
	}
	return c
}

func viewerCookie(t *testing.T, f *adminFixture) string {
	t.Helper()
	rr := loginRR(t, f.Router, "viewer@example.com", "secret-password")
	if rr.Code != http.StatusOK {
		t.Fatalf("viewer login failed: %d (%s)", rr.Code, rr.Body.String())
	}
	c := sessionCookie(rr)
	if c == "" {
		t.Fatal("viewer session cookie missing")
	}
	return c
}

// TestGetRuntimeConfig_AdminSeesEnvSources covers the "fresh install / no DB
// override" path — every field should be marked as sourced from env, and the
// recommended snapshot should round-trip. Pre-PUT.
func TestGetRuntimeConfig_AdminSeesEnvSources(t *testing.T) {
	f := newAdminFixture(t)
	cookie := adminCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/runtime-config", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		EmbeddingModel string            `json:"embedding_model"`
		LlamaCtxSize   int               `json:"llama_ctx_size"`
		Source         map[string]string `json:"source"`
		Recommended    map[string]any    `json:"recommended"`
		UpdatedAt      *string           `json:"updated_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.EmbeddingModel != "env/model" {
		t.Errorf("embedding_model = %q, want env/model", body.EmbeddingModel)
	}
	if body.LlamaCtxSize != 4096 {
		t.Errorf("llama_ctx_size = %d, want 4096", body.LlamaCtxSize)
	}
	for _, f := range []string{"embedding_model", "llama_ctx_size", "llama_n_gpu_layers", "llama_n_threads"} {
		if body.Source[f] != "env" {
			t.Errorf("source[%s] = %q, want env", f, body.Source[f])
		}
	}
	if body.Recommended == nil {
		t.Error("recommended block missing — UI relies on it for the 'Recommended' pill")
	}
	if body.UpdatedAt != nil {
		t.Errorf("updated_at = %v, want nil before any PUT", body.UpdatedAt)
	}
}

// TestGetRuntimeConfig_ViewerForbidden — the runtime config surface is
// admin-only; a viewer session must get 403, not the data.
func TestGetRuntimeConfig_ViewerForbidden(t *testing.T) {
	f := newAdminFixture(t)
	cookie := viewerCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/runtime-config", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestPutRuntimeConfig_RoundTrip exercises the dashboard's primary flow:
// admin saves a couple of overrides, GET reflects them with source="db",
// untouched fields keep source="env". Then a clear (empty + zero) returns
// the fields to env-sourced.
func TestPutRuntimeConfig_RoundTrip(t *testing.T) {
	f := newAdminFixture(t)
	cookie := adminCookie(t, f)

	patch := map[string]any{
		"embedding_model": "db/model-v2",
		"llama_ctx_size":  8192,
	}
	body, _ := json.Marshal(patch)
	req := withCookie(httptest.NewRequest(http.MethodPut, "/api/v1/admin/runtime-config", bytes.NewReader(body)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", rr.Code, rr.Body.String())
	}
	var got struct {
		EmbeddingModel string            `json:"embedding_model"`
		LlamaCtxSize   int               `json:"llama_ctx_size"`
		LlamaNThreads  int               `json:"llama_n_threads"`
		Source         map[string]string `json:"source"`
		UpdatedBy      *string           `json:"updated_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal PUT response: %v", err)
	}
	if got.EmbeddingModel != "db/model-v2" || got.Source["embedding_model"] != "db" {
		t.Errorf("model not from DB after PUT: %+v src=%q", got.EmbeddingModel, got.Source["embedding_model"])
	}
	if got.LlamaCtxSize != 8192 || got.Source["llama_ctx_size"] != "db" {
		t.Errorf("ctx not from DB after PUT: %d src=%q", got.LlamaCtxSize, got.Source["llama_ctx_size"])
	}
	if got.LlamaNThreads != 4 || got.Source["llama_n_threads"] != "env" {
		t.Errorf("untouched threads field shifted source: val=%d src=%q", got.LlamaNThreads, got.Source["llama_n_threads"])
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != "admin@example.com" {
		t.Errorf("updated_by = %v, want admin@example.com", got.UpdatedBy)
	}

	// Clear the model override; ctx override should remain.
	clearBody, _ := json.Marshal(map[string]any{"embedding_model": ""})
	req = withCookie(httptest.NewRequest(http.MethodPut, "/api/v1/admin/runtime-config", bytes.NewReader(clearBody)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT clear status = %d (%s)", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal clear: %v", err)
	}
	if got.EmbeddingModel != "env/model" || got.Source["embedding_model"] != "env" {
		t.Errorf("model didn't fall back to env after clear: %q src=%q", got.EmbeddingModel, got.Source["embedding_model"])
	}
	if got.LlamaCtxSize != 8192 || got.Source["llama_ctx_size"] != "db" {
		t.Errorf("ctx override lost during model clear: %d src=%q", got.LlamaCtxSize, got.Source["llama_ctx_size"])
	}
}

// TestSidecarStatus_DisabledWhenNoEmbedSvc — when the server boots with
// embeddings disabled, the dashboard still gets a meaningful status payload
// (state="disabled") so it can render the bootstrap-only banner.
func TestSidecarStatus_DisabledWhenNoEmbedSvc(t *testing.T) {
	f := newAdminFixture(t)
	cookie := adminCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/sidecar/status", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["state"] != "disabled" {
		t.Errorf("state = %v, want 'disabled' when EmbeddingSvc is nil", body["state"])
	}
}

// TestListModels_EmptyCache — fresh cache directory returns an empty list +
// the cache_dir so the UI can render the "no cached models, use a path"
// fallback without guessing.
func TestListModels_EmptyCache(t *testing.T) {
	f := newAdminFixture(t)
	cookie := adminCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/models", nil), cookie)
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Models   []any  `json:"models"`
		CacheDir string `json:"cache_dir"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if len(body.Models) != 0 {
		t.Errorf("models = %d, want 0 in fresh fixture", len(body.Models))
	}
	// EmbeddingSvc is nil in the fixture, so CacheDirFromService returns ""
	// regardless of envCfg.GGUFCacheDir. That's fine — UI treats it as
	// "no scan possible" and falls back to free-text input.
	if body.CacheDir != "" {
		t.Errorf("cache_dir = %q, want empty when EmbeddingSvc is nil", body.CacheDir)
	}
}
