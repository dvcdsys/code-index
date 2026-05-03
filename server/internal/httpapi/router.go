// Package httpapi wires the chi router and HTTP handlers for the Go server.
//
// All routes are described in doc/openapi.yaml; the generated chi shim in
// internal/httpapi/openapi mounts them onto the router and dispatches to
// methods on the Server struct (see server.go).
package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// EmbeddingsQuerier is the minimal surface the /search handler needs from the
// embeddings service. *embeddings.Service satisfies it; tests substitute a fake.
//
// Ready is consumed by /api/v1/status.model_loaded (m5) and by /health
// (optionally, when the full probe is wired) to report the sidecar's real
// state instead of a hard-coded `true`.
type EmbeddingsQuerier interface {
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
	Ready(ctx context.Context) error
}

// Compile-time assertion that *embeddings.Service still satisfies the surface.
var _ EmbeddingsQuerier = (*embeddings.Service)(nil)

// Deps bundles the runtime dependencies handlers need.
type Deps struct {
	DB             *sql.DB
	ServerVersion  string
	APIVersion     string
	Backend        string
	EmbeddingModel string
	Logger         *slog.Logger
	// AuthDisabled, when true, omits the auth middleware entirely — every
	// route becomes reachable without credentials. Off by default. Toggle
	// via CIX_AUTH_DISABLED=true (config.go) for local dev or tests.
	AuthDisabled bool
	// Users / Sessions / APIKeys back the dashboard auth model. Required
	// in production; tests may pass nil + AuthDisabled=true to skip the
	// gate.
	Users    *users.Service
	Sessions *sessions.Service
	APIKeys  *apikeys.Service
	// EmbeddingSvc is the in-process embeddings service. May be nil when the
	// server is started with CIX_EMBEDDINGS_ENABLED=false (e.g. in router
	// tests). Phase 5 uses it for semantic search.
	EmbeddingSvc EmbeddingsQuerier
	// VectorStore is the chromem-go backed vector store (Phase 4). Nil-safe:
	// semantic search returns empty results when absent.
	VectorStore *vectorstore.Store
	// Indexer drives the three-phase index protocol (Phase 5). Nil-safe: the
	// indexing endpoints return 503 when absent.
	Indexer *indexer.Service
}

// NewRouter builds the chi router with middleware and the generated
// OpenAPI-derived routes.
//
// Project paths contain slashes that cannot be embedded in plain URL segments.
// We follow the Python approach of SHA1-hashing them (first 16 hex chars) and
// using the hash as the URL key. See internal/projects.HashPath for details.
//
// Auth: every route except `GET /health` lives behind the `requireAPIKey`
// middleware. The generated chi-server mounts under a sub-router so the gate
// stays in one place.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(serverVersionHeader(d.ServerVersion))
	r.Use(structuredLogger(d.Logger))

	srv := &Server{Deps: d}

	// Auth — the middleware is installed unless AuthDisabled is true. Every
	// authenticated route accepts EITHER an active session cookie OR a
	// Bearer API key; admin-only routes additionally require role=admin.
	// requireAuth skips public paths (see isPublicPath in middleware.go):
	// /health, /docs, /docs/*, /openapi.json plus the bootstrap-status and
	// login endpoints.
	if !d.AuthDisabled {
		r.Use(requireAuth(d))
	} else if d.Logger != nil {
		// Loud signal — every authenticated request will pass without checks.
		// The startup banner in main.go also logs this; we duplicate here so
		// router-only test runs surface the same warning.
		d.Logger.Warn("auth disabled (CIX_AUTH_DISABLED=true) — every endpoint is reachable without an API key")
	}

	// Documentation — Swagger UI shell + the embedded OpenAPI spec served
	// from the bytes in openapi.gen.go. Both are public regardless of auth.
	r.Get("/docs", docsIndexHandler)
	r.Get("/docs/*", docsAssetsHandler)
	r.Get("/openapi.json", openapiSpecHandler)

	// All API operations — chi.HandlerFromMux walks the spec and registers
	// one chi route per OpenAPI operation, dispatching to Server methods.
	openapi.HandlerFromMux(srv, r)

	return r
}
