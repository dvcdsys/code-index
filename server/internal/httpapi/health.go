package httpapi

import (
	"encoding/json"
	"net/http"
)

// writeJSON encodes body as JSON and writes it with the given status code.
// Shared by every handler in this package; lives here because health.go is
// the smallest non-generated file and feels like the right home.
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError emits the canonical {"detail": "..."} error body. The shape is
// byte-identical to the Python FastAPI default and matches every error
// schema declared in doc/openapi.yaml.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"detail": msg})
}
