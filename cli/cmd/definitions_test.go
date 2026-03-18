package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunDefinitions_Results(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)
	sig := "func ValidateToken(token string) (*Claims, error)"

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/definitions"):
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{
					{
						"name":      "ValidateToken",
						"kind":      "function",
						"file_path": proj + "/auth/jwt.go",
						"line":      15,
						"end_line":  30,
						"language":  "go",
						"signature": sig,
					},
				},
				"total": 1,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := defProject
	defer func() { defProject = old }()
	defProject = proj
	defKind = ""
	defFile = ""
	defLimit = 10

	out, err := captureOutput(func() error {
		return runDefinitions(nil, []string{"ValidateToken"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ValidateToken") {
		t.Errorf("expected symbol name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "jwt.go") {
		t.Errorf("expected file path in output, got:\n%s", out)
	}
	if !strings.Contains(out, sig) {
		t.Errorf("expected signature in output, got:\n%s", out)
	}
}

func TestRunDefinitions_NoResults(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/definitions"):
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := defProject
	defer func() { defProject = old }()
	defProject = proj
	defKind = ""
	defFile = ""
	defLimit = 10

	out, err := captureOutput(func() error {
		return runDefinitions(nil, []string{"GhostSymbol"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No definitions") {
		t.Errorf("expected 'No definitions', got:\n%s", out)
	}
}

func TestRunDefinitions_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/definitions"):
			apiError(w, 500, "internal error")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := defProject
	defer func() { defProject = old }()
	defProject = proj
	defKind = ""
	defFile = ""
	defLimit = 10

	_, err := captureOutput(func() error {
		return runDefinitions(nil, []string{"Anything"})
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "search failed") {
		t.Errorf("expected 'search failed' in error, got: %v", err)
	}
}
