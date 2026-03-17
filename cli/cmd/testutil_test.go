package cmd

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// projectHash returns the same SHA1 prefix that the client uses for URL routing.
func projectHash(path string) string {
	h := sha1.Sum([]byte(path))
	return fmt.Sprintf("%x", h)[:16]
}

// mockServer starts a test HTTP server and registers cleanup.
func mockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// useAPI points the CLI at the given test server for the duration of the test.
func useAPI(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := apiURL
	prevKey := apiKey
	apiURL = srv.URL
	apiKey = "test-key"
	t.Cleanup(func() {
		apiURL = prev
		apiKey = prevKey
	})
}

// captureOutput captures stdout produced by fn and returns it as a string.
func captureOutput(fn func() error) (string, error) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String(), err
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// listProjectsHandler returns a handler that serves GET /api/v1/projects
// with the given project paths.
func listProjectsHandler(paths []string) http.HandlerFunc {
	type proj struct {
		HostPath string `json:"host_path"`
		Status   string `json:"status"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		projects := make([]proj, len(paths))
		for i, p := range paths {
			projects[i] = proj{HostPath: p, Status: "indexed"}
		}
		writeJSON(w, 200, map[string]any{
			"projects": projects,
			"total":    len(projects),
		})
	}
}

// apiError writes a standard API error response.
func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
