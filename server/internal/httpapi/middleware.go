package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
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

// publicPaths is the set of HTTP paths that bypass the auth check.
// Includes the bootstrap probe + login (callers MUST be able to reach
// these without a valid session) plus the documentation, health, and
// dashboard static-asset endpoints. The dashboard's API calls still go
// through the auth gate — only the SPA shell + Vite-built assets are
// public so the login form can render.
var publicPaths = map[string]struct{}{
	"/health":                       {},
	"/docs":                         {},
	"/openapi.json":                 {},
	"/dashboard":                    {},
	"/api/v1/auth/bootstrap-status": {},
	"/api/v1/auth/login":            {},
}

// authContextKey is the context key under which the authenticated user
// is stashed by requireAuth. Handlers retrieve it via userFromCtx; the
// "session" or "api_key" auth method is recorded alongside so /auth/me
// can report which path the caller arrived through.
type authContextKey struct{}

type authContext struct {
	User    users.User
	Method  string // "session" | "api_key"
	Session *sessions.Session
	APIKey  *apikeys.ApiKey
}

func withAuth(ctx context.Context, ac *authContext) context.Context {
	return context.WithValue(ctx, authContextKey{}, ac)
}

func authFromCtx(ctx context.Context) (*authContext, bool) {
	v, ok := ctx.Value(authContextKey{}).(*authContext)
	return v, ok
}

// requireAuth gates every non-public route. Order of checks: session
// cookie first (most common for browsers), then Bearer API key.
//
// Either path attaches the resolved user to the request context. Hands
// off to next on success; writes 401 with `{"detail":"..."}` on failure.
func requireAuth(d Deps) func(http.Handler) http.Handler {
	if d.Users == nil || d.Sessions == nil || d.APIKeys == nil {
		// Defensive panic: if a deployment forgets to wire any of the
		// three services, every request would 401 silently. Fail loud
		// at startup instead.
		panic("httpapi: requireAuth installed without Users+Sessions+APIKeys services — set Deps.AuthDisabled=true to opt out")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ip := clientIP(r)
			ua := r.UserAgent()

			// 1. Session cookie. The cookie is HttpOnly + SameSite=Strict
			// so any browser sending it has the right origin; we still
			// validate the id against the sessions table.
			if c, err := r.Cookie(sessions.CookieName); err == nil {
				sess, u, sErr := d.Sessions.Get(r.Context(), c.Value)
				if sErr == nil {
					_ = d.Sessions.Touch(r.Context(), sess.ID, ip, ua)
					ac := &authContext{User: u, Method: "session", Session: &sess}
					next.ServeHTTP(w, r.WithContext(withAuth(r.Context(), ac)))
					return
				}
				// If the cookie was present but invalid (expired, deleted,
				// user-disabled), fall through to Bearer auth — some CLI
				// clients also set a cookie for unrelated reasons.
				_ = sErr
			}

			// 2. Bearer API key.
			if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
				key := strings.TrimSpace(authz[len("Bearer "):])
				if key != "" {
					u, ak, aErr := d.APIKeys.Authenticate(r.Context(), key)
					if aErr == nil {
						_ = d.APIKeys.Touch(r.Context(), ak.ID, ip, ua)
						ac := &authContext{User: u, Method: "api_key", APIKey: &ak}
						next.ServeHTTP(w, r.WithContext(withAuth(r.Context(), ac)))
						return
					}
					if errors.Is(aErr, apikeys.ErrUserDisabled) {
						writeError(w, http.StatusUnauthorized, "API key owner is disabled")
						return
					}
				}
			}

			writeError(w, http.StatusUnauthorized, "Authentication required")
		})
	}
}

// requireRole rejects callers whose attached user does not have the
// expected role. Always paired with requireAuth — must be installed
// further down the chain.
func requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := authFromCtx(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "Authentication required")
				return
			}
			if ac.User.Role != role {
				writeError(w, http.StatusForbidden, "This action requires role: "+role)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isPublicPath returns true when the path is exempt from auth.
func isPublicPath(p string) bool {
	if _, ok := publicPaths[p]; ok {
		return true
	}
	if strings.HasPrefix(p, "/docs/") {
		return true
	}
	if strings.HasPrefix(p, "/dashboard/") {
		return true
	}
	return false
}

// clientIP returns the best-effort remote IP for audit logging. Honours
// X-Forwarded-For (first hop) when present, otherwise falls back to the
// raw RemoteAddr. Not used for any security decision — only stored as
// metadata in sessions.last_seen_ip / api_keys.last_used_ip.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
