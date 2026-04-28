package cmd

import (
	"encoding/json"
	"io"
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
	searchMinScore = 0.4
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
	searchMinScore = 0.4
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
	searchMinScore = 0.4
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

func TestRunSearch_OutputUsesRelativePath(t *testing.T) {
	// Verifies the cosmetic-but-impactful path-shortening: output should
	// show paths relative to the project root, not the full /Users/.../foo
	// absolute path. Saves ~50 chars per result line in agent context.
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
						"file_path":  proj + "/server/internal/search.go",
						"language":   "go",
						"best_score": 0.88,
						"matches": []map[string]any{
							{
								"start_line":  1,
								"end_line":    5,
								"content":     "x",
								"score":       0.88,
								"chunk_type":  "function",
								"symbol_name": "Search",
							},
						},
					},
				},
				"total":         1,
				"query_time_ms": 1.0,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	resetSearchFlags()
	defer resetSearchFlags()
	searchProject = proj

	out, err := captureOutput(func() error { return runSearch(nil, []string{"q"}) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The result-header line must contain the relative path, NOT the full
	// absolute path. The exact line shape is "1. <path>  [best 0.88]  ...".
	if !strings.Contains(out, "server/internal/search.go") {
		t.Errorf("expected relative path in output, got:\n%s", out)
	}
	if strings.Contains(out, proj+"/server/internal/search.go") {
		t.Errorf("absolute path leaked into output:\n%s", out)
	}
}

func TestRunSearch_SuppressesScoreOnSingleMatch(t *testing.T) {
	// When a file has exactly one match and its score equals BestScore,
	// the renderer drops the redundant inner "[0.88]" line.
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search"):
			writeJSON(w, 200, map[string]any{
				"results": []map[string]any{{
					"file_path":  proj + "/x.go",
					"language":   "go",
					"best_score": 0.88,
					"matches": []map[string]any{{
						"start_line": 1, "end_line": 5, "content": "x", "score": 0.88,
						"chunk_type": "function", "symbol_name": "X",
					}},
				}},
				"total": 1, "query_time_ms": 1.0,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	resetSearchFlags()
	defer resetSearchFlags()
	searchProject = proj

	out, err := captureOutput(func() error { return runSearch(nil, []string{"q"}) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "[best 0.88]" must appear once (file header), not twice.
	if c := strings.Count(out, "0.88"); c != 1 {
		t.Errorf("expected score 0.88 to appear exactly once, got %d. Output:\n%s", c, out)
	}
}

func TestRunSearch_SendsExcludesToServer(t *testing.T) {
	// --exclude must end up in the search request body so the server can
	// honour it. Verifies the CLI → client → request body wiring.
	proj := t.TempDir()
	hash := projectHash(proj)

	var captured map[string]any
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &captured)
			writeJSON(w, 200, map[string]any{"results": []any{}, "total": 0, "query_time_ms": 1.0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	resetSearchFlags()
	defer resetSearchFlags()
	searchProject = proj
	searchExcludes = []string{"bench/fixtures", "legacy"}

	if _, err := captureOutput(func() error { return runSearch(nil, []string{"q"}) }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rawExcl, ok := captured["excludes"].([]any)
	if !ok {
		t.Fatalf("excludes missing from request body; got: %v", captured)
	}
	if len(rawExcl) != 2 {
		t.Errorf("expected 2 excludes, got %d", len(rawExcl))
	}
}

// resetSearchFlags clears the package-level cobra flag vars between tests.
// Without it, a value set in one test leaks into the next via the shared
// var captures inside searchCmd.
func resetSearchFlags() {
	searchProject = ""
	searchLimit = 10
	searchMinScore = 0.4
	searchLanguages = nil
	searchPaths = nil
	searchExcludes = nil
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
	searchMinScore = 0.4
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
