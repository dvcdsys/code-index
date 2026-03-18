package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunSummary(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/summary"):
			writeJSON(w, 200, map[string]any{
				"host_path":      proj,
				"status":         "indexed",
				"languages":      []string{"go", "python"},
				"total_files":    72,
				"total_chunks":   155,
				"total_symbols":  0,
				"top_directories": []map[string]any{
					{"path": proj + "/api", "file_count": 30.0},
					{"path": proj + "/cli", "file_count": 20.0},
				},
				"recent_symbols": []map[string]any{
					{"name": "IndexerService", "kind": "class"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := summaryProject
	defer func() { summaryProject = old }()
	summaryProject = proj

	out, err := captureOutput(func() error {
		return runSummary(nil, nil)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "72") {
		t.Errorf("expected file count in output, got:\n%s", out)
	}
	if !strings.Contains(out, "go") {
		t.Errorf("expected language in output, got:\n%s", out)
	}
	if !strings.Contains(out, "IndexerService") {
		t.Errorf("expected symbol in output, got:\n%s", out)
	}
}

func TestRunSummary_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/summary"):
			apiError(w, 404, "not found")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := summaryProject
	defer func() { summaryProject = old }()
	summaryProject = proj

	_, err := captureOutput(func() error {
		return runSummary(nil, nil)
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "get summary") {
		t.Errorf("expected 'get summary' in error, got: %v", err)
	}
}
