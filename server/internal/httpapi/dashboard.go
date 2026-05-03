package httpapi

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/httpapi/dashboard"
)

// dashboardFS is the embedded SPA bundle, rooted at "dist/" so callers
// reference paths like "index.html" or "assets/index-abcd.js".
var dashboardFS = func() fs.FS {
	sub, err := fs.Sub(dashboard.Assets, "dist")
	if err != nil {
		// embed.FS must contain "dist/" — codegen would have caught this
		// at build time. A runtime panic here means the embed directive
		// was edited without keeping the path in sync.
		panic("dashboard: dist/ missing from embed: " + err.Error())
	}
	return sub
}()

// dashboardContentTypeMap mirrors docsContentTypeMap — pin the Content-Type
// for the handful of asset extensions Vite emits. Without this, Safari
// occasionally refuses to execute .js served as "application/javascript"
// (no charset).
var dashboardContentTypeMap = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".mjs":   "application/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".map":   "application/json; charset=utf-8",
}

// dashboardIndexHandler serves the SPA shell on GET /dashboard and /dashboard/.
// The browser then loads /dashboard/assets/* and the runtime React Router
// takes over for client-side navigation.
func dashboardIndexHandler(w http.ResponseWriter, r *http.Request) {
	serveDashboardIndex(w, r)
}

// dashboardAssetsHandler serves anything under /dashboard/<path>. Three cases:
//
//  1. /dashboard/assets/<file> — return the embedded asset
//  2. /dashboard/<path-with-extension> e.g. /dashboard/favicon.svg — return
//     the embedded file if it exists, 404 otherwise
//  3. /dashboard/<path-without-extension> e.g. /dashboard/projects/abc — fall
//     back to index.html so the SPA's HTML5 history routing keeps working
//     across browser refreshes
func dashboardAssetsHandler(w http.ResponseWriter, r *http.Request) {
	const prefix = "/dashboard/"
	name := strings.TrimPrefix(r.URL.Path, prefix)
	if name == "" {
		serveDashboardIndex(w, r)
		return
	}

	// History fallback — anything that doesn't look like a file extension
	// is treated as an in-app route.
	if !strings.Contains(name, ".") {
		serveDashboardIndex(w, r)
		return
	}

	data, err := fs.ReadFile(dashboardFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := dashboardContentTypeFor(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Vite emits hashed filenames under assets/, so they're safe to cache
	// hard. Everything else (PLACEHOLDER.html, future favicon) gets a short
	// max-age so the operator picks up changes within a few minutes.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	_, _ = w.Write(data)
}

// dashboardPlaceholderHTML is shown when dist/index.html is missing — i.e.
// `go build` ran without `make dashboard-build` first. We could embed a
// separate file but inlining keeps the embed.FS minimal and removes the
// fragility of vite's emptyOutDir wiping any committed placeholder file.
const dashboardPlaceholderHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>cix dashboard — not built</title>
<style>
:root { color-scheme: light dark; }
body {
  font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
  max-width: 38rem; margin: 4rem auto; padding: 0 1.5rem; line-height: 1.6;
}
h1 { font-size: 1.25rem; font-weight: 600; margin-bottom: 0.5rem; }
code, pre {
  background: rgba(0,0,0,0.06); padding: 0.15rem 0.4rem; border-radius: 0.3rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em;
}
pre { padding: 0.75rem 1rem; overflow-x: auto; }
@media (prefers-color-scheme: dark) {
  body { background: #111; color: #eee; }
  code, pre { background: rgba(255,255,255,0.08); }
}
</style>
</head>
<body>
<h1>cix dashboard placeholder</h1>
<p>The React dashboard hasn’t been built into this server binary yet.</p>
<p>From the repo root run:</p>
<pre><code>cd server &amp;&amp; make dashboard-build &amp;&amp; make build</code></pre>
<p>Then restart the server. If you’re seeing this in production, your CI build forgot to run the dashboard stage.</p>
</body>
</html>`

// serveDashboardIndex returns dist/index.html when present, otherwise the
// inline placeholder above. This lets `go build` succeed on a fresh clone
// without `make dashboard-build` first — the operator gets a clear "you
// need to build the dashboard" message instead of a blank page.
//
// The HTML is never cached: a fresh deploy must invalidate it immediately
// so the new asset hashes are picked up on the next request.
func serveDashboardIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if data, err := fs.ReadFile(dashboardFS, "index.html"); err == nil {
		_, _ = w.Write(data)
		return
	}
	_, _ = w.Write([]byte(dashboardPlaceholderHTML))
}

// dashboardContentTypeFor returns the explicit Content-Type for the given
// filename, or "" if Go's stdlib detection is good enough.
func dashboardContentTypeFor(name string) string {
	for ext, ct := range dashboardContentTypeMap {
		if strings.HasSuffix(name, ext) {
			return ct
		}
	}
	return ""
}
