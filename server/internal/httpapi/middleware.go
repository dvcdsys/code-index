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

// requireAPIKey enforces Bearer-token auth matching api/app/auth.py.
//
// Behaviour:
//   - `GET /health` is public (probe endpoint) — it is wired outside this
//     middleware in NewRouter.
//   - All other routes require `Authorization: Bearer <apiKey>`.
//   - Missing or mismatched tokens return 401 with
//     `{"detail":"Invalid or missing API key"}` — byte-identical to Python.
//   - If apiKey is empty the check is skipped (dev mode); cmd/cix-server/main.go
//     logs a warning on startup.
func requireAPIKey(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
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
