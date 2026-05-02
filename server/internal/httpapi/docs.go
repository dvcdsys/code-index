package httpapi

import (
	"io/fs"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/httpapi/docs"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// docsContentTypeMap overrides the default Go mime detection for the
// swagger-ui bundle. Without this, .js sometimes resolves to
// "application/javascript" on older systems and Safari refuses to execute.
var docsContentTypeMap = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "application/javascript; charset=utf-8",
	".png":  "image/png",
}

// docsFS is the embedded Swagger UI bundle, rooted at swagger-ui/ (i.e.
// "index.html" → swagger-ui/index.html). Computed once at package init.
var docsFS = func() fs.FS {
	sub, err := fs.Sub(docs.Assets, "swagger-ui")
	if err != nil {
		// embed.FS must contain "swagger-ui/" — codegen will catch a typo
		// at build time, so a runtime panic here means someone deleted
		// the bundle without bumping the embed directive.
		panic("docs: swagger-ui bundle missing from embed: " + err.Error())
	}
	return sub
}()

// docsIndexHandler serves the Swagger UI shell on GET /docs (and /docs/).
// The browser then fetches static assets via /docs/<asset> (handled by
// docsAssetsHandler) and the spec via /openapi.json (handled by
// openapiSpecHandler).
func docsIndexHandler(w http.ResponseWriter, r *http.Request) {
	serveDocsFile(w, r, "index.html")
}

// docsAssetsHandler serves anything under /docs/<asset>. Strips the /docs/
// prefix and looks the file up in the embedded bundle.
func docsAssetsHandler(w http.ResponseWriter, r *http.Request) {
	const prefix = "/docs/"
	name := r.URL.Path[len(prefix):]
	if name == "" || name == "/" {
		serveDocsFile(w, r, "index.html")
		return
	}
	serveDocsFile(w, r, name)
}

// serveDocsFile is the common path that reads from the embedded bundle and
// sets the right Content-Type for the file extension.
func serveDocsFile(w http.ResponseWriter, _ *http.Request, name string) {
	data, err := fs.ReadFile(docsFS, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if ct := contentTypeFor(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Static bundle is keyed by version in the URL implicitly (we don't
	// version it), so a short cache is the safest default — long enough
	// to avoid request storms, short enough to pick up a fresh deploy.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

// contentTypeFor returns the explicit content type for the given filename,
// or "" if Go's stdlib detection is good enough.
func contentTypeFor(name string) string {
	for ext, ct := range docsContentTypeMap {
		if hasSuffix(name, ext) {
			return ct
		}
	}
	return ""
}

// hasSuffix is a tiny inline replacement for strings.HasSuffix to avoid
// pulling the strings import into this single-purpose file.
func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// openapiSpecHandler serves the OpenAPI spec at /openapi.json. The spec
// is decoded from the gzip+base64 blob embedded in openapi.gen.go (via
// the embedded-spec oapi-codegen flag) — there is no separate file on
// disk to drift out of sync.
func openapiSpecHandler(w http.ResponseWriter, _ *http.Request) {
	data, err := openapi.GetSpecJSON()
	if err != nil {
		http.Error(w, "spec unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}
