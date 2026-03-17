package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunReferences_Results(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/references"):
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{
					{
						"file_path":   proj + "/main.go",
						"start_line":  5,
						"end_line":    10,
						"content":     "token, err := ValidateToken(raw)\nif err != nil { ... }",
						"chunk_type":  "function",
						"symbol_name": "main",
						"language":    "go",
					},
					{
						"file_path":   proj + "/middleware.go",
						"start_line":  22,
						"end_line":    28,
						"content":     "claims := ValidateToken(header)",
						"chunk_type":  "function",
						"symbol_name": "AuthMiddleware",
						"language":    "go",
					},
				},
				"total": 2,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := refsProject
	defer func() { refsProject = old }()
	refsProject = proj
	refsLimit = 30
	refsFile = ""

	out, err := captureOutput(func() error {
		return runReferences(nil, []string{"ValidateToken"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "2 reference") {
		t.Errorf("expected reference count, got:\n%s", out)
	}
	if !strings.Contains(out, "main.go") {
		t.Errorf("expected main.go in output, got:\n%s", out)
	}
	if !strings.Contains(out, "middleware.go") {
		t.Errorf("expected middleware.go in output, got:\n%s", out)
	}
}

func TestRunReferences_NoResults(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/references"):
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := refsProject
	defer func() { refsProject = old }()
	refsProject = proj
	refsLimit = 30
	refsFile = ""

	out, err := captureOutput(func() error {
		return runReferences(nil, []string{"UnusedSymbol"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No references") {
		t.Errorf("expected 'No references', got:\n%s", out)
	}
}

func TestRunReferences_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/references"):
			apiError(w, 500, "internal error")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := refsProject
	defer func() { refsProject = old }()
	refsProject = proj
	refsLimit = 30
	refsFile = ""

	_, err := captureOutput(func() error {
		return runReferences(nil, []string{"Foo"})
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
