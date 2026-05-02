package openapi

import (
	"testing"
)

// TestSpecLoadsAndValidates exercises the embedded spec loader. If
// doc/openapi.yaml drifts from a parseable OpenAPI 3.x document, this
// fails before any handler test does.
func TestSpecLoadsAndValidates(t *testing.T) {
	swagger, err := GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	if swagger == nil || swagger.Info == nil {
		t.Fatal("nil swagger or info section")
	}
	if got := swagger.Info.Title; got != "cix-server API" {
		t.Errorf("info.title = %q, want %q", got, "cix-server API")
	}
	if got := swagger.Info.Version; got != "v1" {
		t.Errorf("info.version = %q, want %q", got, "v1")
	}

	// Sanity: every operation in the ServerInterface has a matching path
	// in the spec. We check by counting operations rather than naming
	// each one — keeps the test from drifting whenever an endpoint is
	// added.
	if swagger.Paths == nil {
		t.Fatal("paths section missing")
	}
	pathCount := swagger.Paths.Len()
	const wantMin = 13 // 18 endpoints share a few path keys (CRUD on same path)
	if pathCount < wantMin {
		t.Errorf("paths.len() = %d, expected at least %d (spec may have lost endpoints)", pathCount, wantMin)
	}
}
