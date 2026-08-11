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
	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/embeddingscfg"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/repolocks"
	"github.com/dvcdsys/code-index/server/internal/runtimecfg"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/tunnelcfg"
	"github.com/dvcdsys/code-index/server/internal/tunnels"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/versioncheck"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
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
	// Groups backs the view-group auth model (admin-managed sets of users
	// that external projects + workspaces are shared to). Required in
	// production; nil + AuthDisabled is tolerated by tests.
	Groups *groups.Service
	// EmbeddingSvc is the in-process embeddings service. May be nil when the
	// server is started with CIX_EMBEDDINGS_ENABLED=false (e.g. in router
	// tests). Phase 5 uses it for semantic search.
	EmbeddingSvc EmbeddingsQuerier
	// VectorStore is the chromem-go backed vector store (Phase 4). Nil-safe:
	// semantic search returns empty results when absent. Typed as the
	// vectorstore.Interface so production can supply a *vectorstore.Holder
	// (swappable on provider switch) while tests pass a raw *Store.
	VectorStore vectorstore.Interface
	// Indexer drives the three-phase index protocol (Phase 5). Nil-safe: the
	// indexing endpoints return 503 when absent.
	Indexer *indexer.Service
	// RuntimeCfg backs the dashboard's /admin/runtime-config endpoints. Nil
	// in router-only tests; admin handlers return 503 when absent.
	RuntimeCfg *runtimecfg.Service
	// EmbeddingsCfg persists the pluggable-provider selection + config
	// blob in runtime_settings. Read by the /embedding-providers admin
	// handlers; nil in router-only tests (those handlers 503 when
	// absent).
	EmbeddingsCfg *embeddingscfg.Service
	// VersionCheck polls GitHub for newer server releases. Nil = feature
	// off; GetStatus then omits the version-check fields entirely.
	VersionCheck *versioncheck.Service

	// Workspaces + GithubTokens services. Both are always wired by
	// main; handlers fall back to 503 only if a service is nil (e.g.
	// secrets boot returned nil for GithubTokens because the
	// encryption key path failed). Test setups that don't need these
	// pass nil and the handlers respond consistently.
	Workspaces   *workspaces.Service
	GithubTokens *githubtokens.Service
	// GitRepos owns clone + webhook metadata for external projects;
	// WorkspaceProjects owns the workspace ↔ project junction. Jobs is
	// the persistent background queue.
	GitRepos          *gitrepos.Service
	WorkspaceProjects *workspaceprojects.Service
	Jobs              *jobs.Service
	// DataDir is the base directory under which external repos are cloned
	// (<DataDir>/repos/<path_hash>/). Source: cfg.WorkspacesDataDir. The
	// file/tree read handlers read from this on-disk checkout. Empty in
	// router-only tests; those handlers then 409 (no checkout on disk).
	DataDir string
	// Cfg is the process-wide env-derived config, for handlers that need to
	// resolve on-disk locations (SQLitePath, ChromaPersistDir, GGUFCacheDir,
	// ChromaDirFor). The alternative — type-asserting EmbeddingSvc to
	// *embeddings.Service and calling Config() — silently yields nothing when
	// embeddings are disabled or a fake is installed, which is how the
	// project-detail card ended up reporting no vector-store size at all.
	// Nil in router-only tests; the resource handlers then 503.
	Cfg *config.Config
	// DBMaint carries the hooks database compaction needs to reach the rest
	// of the process. Zero value disables compaction while leaving the
	// reporting and reclaim endpoints working, which is what router-only
	// tests get.
	DBMaint DBMaintHooks
	// RepoLocks serialises file reads against the clone worker's worktree
	// rewrite. Shared (same instance) with repojobs.Deps.RepoLocks. Nil-safe:
	// NewRouter allocates a private registry when unset (tests).
	RepoLocks *repolocks.Locks
	// PublicBaseURL is the operator-set externally-reachable URL of the
	// server. Used to build the webhook URL surfaced when adding a repo
	// — when empty, handlers return the path-only form and rely on the
	// operator to prepend their tunnel origin manually. Source:
	// CIX_PUBLIC_URL.
	PublicBaseURL string
	// GithubAPIBaseURL overrides the GitHub REST API base for the
	// per-request client constructed inside token / webhook handlers.
	// Empty in production (the client defaults to https://api.github.com).
	// Tests set this to an httptest server so they can assert the
	// scopes / validation flow without hitting the real API.
	GithubAPIBaseURL string

	// Tunnel manages the optional managed-tunnel provider (Cloudflare).
	// Nil when CIX_TUNNEL_ENABLED=false — tunnel handlers then report a
	// disabled status and buildWebhookURL falls back to PublicBaseURL.
	// When live, its public URL takes precedence over PublicBaseURL for
	// webhook delivery URLs.
	Tunnel *tunnels.Manager
	// WebhookReconciler re-registers webhook_mode=auto repos against the
	// current public base URL. Nil in tests / when tunnels are off.
	WebhookReconciler *tunnels.Reconciler
	// TunnelConfig persists the dashboard-managed tunnel settings. Nil in
	// tests; the config handlers return 503 when absent.
	TunnelConfig *tunnelcfg.Service
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
	// Ensure handlers can call d.Logger.* without nil-checking everywhere.
	// Tests routinely leave Logger zero — fall back to the global slog
	// default which writes to stderr.
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	// File/tree handlers and the clone worker share one lock registry. When
	// unset (router-only tests), give this router its own so reads still lock.
	if d.RepoLocks == nil {
		d.RepoLocks = repolocks.New()
	}
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(serverVersionHeader(d.ServerVersion))
	r.Use(structuredLogger(d.Logger))
	r.Use(limitBodySize())

	srv := &Server{
		Deps:         d,
		loginLimiter: newLoginLimiter(),
		maintenance:  newMaintenanceService(d),
		dbmaint:      newDBMaintService(d),
	}

	// Write freeze — installed before auth on purpose: the GitHub webhook
	// route is a write that isPublicPath exempts from authentication, and a
	// gate inside requireAuth would never see it.
	r.Use(maintenanceGate(d.DBMaint.Gate))

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

	// Dashboard — embedded React SPA (Vite build under
	// internal/httpapi/dashboard/dist). Static assets are public; every API
	// call the SPA makes still travels through requireAuth above, so the
	// pages render but show the login screen until /auth/me succeeds.
	r.Get("/dashboard", dashboardIndexHandler)
	r.Get("/dashboard/*", dashboardAssetsHandler)

	// All API operations — chi.HandlerFromMux walks the spec and registers
	// one chi route per OpenAPI operation, dispatching to Server methods.
	// This includes the embedding-provider admin endpoints and the admin
	// password-reset endpoint; both used to be mounted directly here while
	// the committed openapi.gen.go lagged the spec, but the file is now
	// regenerated so the generated mux owns them (a direct mount on top
	// would double-register and panic).
	openapi.HandlerFromMux(srv, r)

	return r
}
