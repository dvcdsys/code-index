// Package httpapi wires the chi router and HTTP handlers for the Go server.
// Phase 1: /health and /api/v1/status.
// Phase 2: project CRUD + symbol/definition/reference/file search + summary.
package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/indexer"
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
	// APIKey is the shared secret compared against the `Authorization: Bearer`
	// header. When empty the server runs in dev mode and skips auth — matches
	// the behaviour advertised in cmd/cix-server/main.go's startup warning.
	APIKey string
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

// NewRouter builds the chi router with middleware and all Phase 1+2 routes.
//
// Project paths contain slashes that cannot be embedded in plain URL segments.
// We follow the Python approach of SHA1-hashing them (first 16 hex chars) and
// using the hash as the URL key. See internal/projects.HashPath for details.
//
// Route list:
//
//	GET    /health
//	GET    /api/v1/status
//	POST   /api/v1/projects                                 create project
//	GET    /api/v1/projects                                 list projects
//	GET    /api/v1/projects/{path}                          get project by hash
//	PATCH  /api/v1/projects/{path}                          patch project settings
//	DELETE /api/v1/projects/{path}                          delete project
//	POST   /api/v1/projects/{path}/search/symbols           symbol name search
//	POST   /api/v1/projects/{path}/search/definitions       go-to-definition
//	POST   /api/v1/projects/{path}/search/references        find references
//	POST   /api/v1/projects/{path}/search/files             file path search
//	GET    /api/v1/projects/{path}/summary                  project summary
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(serverVersionHeader(d.ServerVersion))
	r.Use(structuredLogger(d.Logger))

	// Public probe — no auth, matches Python api/app/routers/health.py.
	r.Get("/health", healthHandler(d))

	// Everything else lives behind the API-key middleware so the gate matches
	// Python's `Depends(verify_api_key)` applied in each router module.
	r.Group(func(pr chi.Router) {
		pr.Use(requireAPIKey(d.APIKey))

		// Phase 1 — status probe (authenticated, unlike /health).
		pr.Get("/api/v1/status", statusHandler(d))

		// Phase 2 — project CRUD.
		pr.Post("/api/v1/projects", createProjectHandler(d))
		pr.Get("/api/v1/projects", listProjectsHandler(d))

		// Project-scoped routes: {path} is a 16-char SHA1 hash of the host_path.
		pr.Get("/api/v1/projects/{path}", getProjectHandler(d))
		pr.Patch("/api/v1/projects/{path}", patchProjectHandler(d))
		pr.Delete("/api/v1/projects/{path}", deleteProjectHandler(d))

		// Phase 2 — search endpoints.
		pr.Post("/api/v1/projects/{path}/search/symbols", symbolSearchHandler(d))
		pr.Post("/api/v1/projects/{path}/search/definitions", definitionSearchHandler(d))
		pr.Post("/api/v1/projects/{path}/search/references", referenceSearchHandler(d))
		pr.Post("/api/v1/projects/{path}/search/files", fileSearchHandler(d))

		// Phase 5 — semantic search.
		pr.Post("/api/v1/projects/{path}/search", semanticSearchHandler(d))

		// Phase 5 — three-phase indexing protocol.
		pr.Post("/api/v1/projects/{path}/index/begin", indexBeginHandler(d))
		pr.Post("/api/v1/projects/{path}/index/files", indexFilesHandler(d))
		pr.Post("/api/v1/projects/{path}/index/finish", indexFinishHandler(d))
		pr.Get("/api/v1/projects/{path}/index/status", indexStatusHandler(d))

		// Phase 2 — summary.
		pr.Get("/api/v1/projects/{path}/summary", projectSummaryHandler(d))
	})

	return r
}
