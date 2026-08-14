package httpapi

import (
	"net/http"
	"strings"
)

// readOnlyPostSuffixes are the POST routes that do not write.
//
// This list is why the freeze is classified per route and never by HTTP
// method. Search is a POST — five of these are search — and so are file and
// tree reads. A method-based gate would refuse exactly the requests the
// feature promises to keep serving during a compaction.
//
// Everything not listed here is refused while frozen, which is the safe
// default in both directions: a future endpoint that reads is briefly
// unavailable during a rare two-minute window, whereas a future endpoint that
// writes would otherwise commit into a snapshot that is about to be discarded.
var readOnlyPostSuffixes = []string{
	"/search",
	"/search/definitions",
	"/search/files",
	"/search/references",
	"/search/symbols",
	"/file",
	"/tree",
}

// isReadOnlyRequest reports whether a request may proceed while writes are
// frozen.
func isReadOnlyRequest(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPost:
		if !strings.HasPrefix(path, "/api/v1/projects/") {
			return false
		}
		for _, suffix := range readOnlyPostSuffixes {
			if strings.HasSuffix(path, suffix) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// maintenanceGate refuses writes while a compaction holds the database.
//
// Installed *before* requireAuth, deliberately. The GitHub webhook route is a
// write that isPublicPath exempts from authentication, so a gate folded into
// the auth middleware would let deliveries write straight through the freeze
// and into a snapshot that is about to be thrown away.
//
// The 503 with Retry-After is what makes this survivable for clients: the CLI
// watcher already treats a failing server as "retry, then re-index pending
// changes on recovery", so a refused write is a delay rather than a loss.
func maintenanceGate(gate interface{ Frozen() bool }) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !gate.Frozen() || isReadOnlyRequest(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusServiceUnavailable,
				"the database is being compacted — reads are being served normally, "+
					"but changes are refused until it finishes")
		})
	}
}
