package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunSearch_Results(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search") && r.Method == "POST":
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{
					{
						"file_path":  proj + "/api/auth.go",
						"language":   "go",
						"best_score": 0.92,
						"matches": []map[string]any{
							{
								"start_line":  10,
								"end_line":    25,
								"content":     "func AuthMiddleware() {}",
								"score":       0.92,
								"chunk_type":  "function",
								"symbol_name": "AuthMiddleware",
							},
						},
					},
				},
				"total":         1,
				"query_time_ms": 12.5,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := searchProject
	defer func() { searchProject = old }()
	searchProject = proj
	searchLimit = 10
	searchMinScore = 0.1
	searchLanguages = nil
	searchPaths = nil

	out, err := captureOutput(func() error {
		return runSearch(nil, []string{"authentication middleware"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "AuthMiddleware") {
		t.Errorf("expected symbol name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "auth.go") {
		t.Errorf("expected file path in output, got:\n%s", out)
	}
	if !strings.Contains(out, "1 file") {
		t.Errorf("expected file count in output, got:\n%s", out)
	}
}

func TestRunSearch_EmptyResults(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search"):
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0, "query_time_ms": 5.0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := searchProject
	defer func() { searchProject = old }()
	searchProject = proj
	searchLimit = 10
	searchMinScore = 0.1
	searchLanguages = nil
	searchPaths = nil

	out, err := captureOutput(func() error {
		return runSearch(nil, []string{"something obscure"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No results") {
		t.Errorf("expected 'No results', got:\n%s", out)
	}
}

func TestRunSearch_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search"):
			apiError(w, 404, "Project not found")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := searchProject
	defer func() { searchProject = old }()
	searchProject = proj
	searchLimit = 10
	searchMinScore = 0.1
	searchLanguages = nil
	searchPaths = nil

	_, err := captureOutput(func() error {
		return runSearch(nil, []string{"query"})
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "search failed") {
		t.Errorf("expected 'search failed' in error, got: %v", err)
	}
}

func TestRunSearch_SubdirectoryResolvesToProject(t *testing.T) {
	proj := t.TempDir()
	sub := proj + "/src/api"
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			// Return proj as a registered project
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{{"host_path": proj, "status": "indexed"}},
				"total":    1,
			})
		case strings.Contains(r.URL.Path, hash+"/search"):
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0, "query_time_ms": 1.0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := searchProject
	defer func() { searchProject = old }()
	// Set project to a subdirectory — should resolve to proj root
	searchProject = sub
	searchLimit = 10
	searchMinScore = 0.1
	searchLanguages = nil
	searchPaths = nil

	out, err := captureOutput(func() error {
		return runSearch(nil, []string{"anything"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A successful response (not 404) confirms the correct project hash was used.
	// With empty results the output should say "No results".
	if !strings.Contains(out, "No results") {
		t.Errorf("expected 'No results' confirming project root was resolved correctly, got:\n%s", out)
	}
}
