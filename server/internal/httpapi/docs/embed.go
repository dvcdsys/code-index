// Package docs holds the Swagger UI bundle that is served at /docs.
//
// The OpenAPI spec itself is NOT embedded here — it ships compressed inside
// the generated openapi.gen.go (via the embedded-spec oapi-codegen flag) and
// is served via openapi.GetSpecJSON() at /openapi.json. Keeping these
// separate avoids the spec being duplicated in two places that can drift.
//
// The bundle was fetched from jsdelivr (swagger-ui-dist@5.18.2). To bump,
// re-run `make swagger-ui-fetch` in server/.
package docs

import "embed"

//go:embed swagger-ui
var Assets embed.FS
