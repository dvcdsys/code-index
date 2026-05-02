package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// serverVersionHeader sets X-Server-Version on every response.
func serverVersionHeader(version string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Server-Version", version)
			next.ServeHTTP(w, r)
		})
	}
}

// publicPaths is the set of HTTP paths that bypass the API-key check. It
// matches both legacy probe behaviour (/health) and the documentation
// endpoints introduced when the OpenAPI spec was made the source of truth.
//
// Kept here (not in router.go) so the middleware is self-contained and
// testable in isolation.
var publicPaths = map[string]struct{}{
	"/health":       {},
	"/docs":         {},
	"/openapi.json": {},
}

// requireAPIKey enforces Bearer-token auth matching api/app/auth.py.
//
// Behaviour:
//   - The paths listed in publicPaths (probe + docs) are always exempt.
//   - Every other route requires `Authorization: Bearer <apiKey>`.
//   - Missing or mismatched tokens return 401 with
//     `{"detail":"Invalid or missing API key"}` — byte-identical to Python.
//
// There is no implicit bypass for an empty `apiKey`: callers that want to
// disable auth (local dev, tests) must arrange for this middleware NOT to
// be installed at all (see Server wiring in router.go and the
// CIX_AUTH_DISABLED config flag). Construction with an empty apiKey here
// is a programming error — the middleware would 401-storm every request.
func requireAPIKey(apiKey string) func(http.Handler) http.Handler {
	if apiKey == "" {
		// Defensive panic — installing this middleware with no key means
		// every authenticated request would 401. The router never does
		// this (see NewRouter), so reaching it indicates a refactor
		// mistake elsewhere.
		panic("httpapi: requireAPIKey installed with empty key — use Deps.AuthDisabled to opt out of auth instead")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			authz := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authz, prefix) || authz[len(prefix):] != apiKey {
				writeError(w, http.StatusUnauthorized, "Invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isPublicPath returns true when the path is exempt from API-key auth.
// Exact match for /health, /openapi.yaml; prefix match for /docs (so
// /docs/static/swagger-ui-bundle.js etc. are also reachable).
func isPublicPath(p string) bool {
	if _, ok := publicPaths[p]; ok {
		return true
	}
	if strings.HasPrefix(p, "/docs/") {
		return true
	}
	return false
}

// structuredLogger logs one line per request via slog at INFO level.
func structuredLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
				"client_version", r.Header.Get("X-Client-Version"),
			)
		})
	}
}
