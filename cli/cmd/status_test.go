package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func projectResponse(proj string, status string) map[string]any {
	return map[string]any{
		"host_path": proj,
		"status":    status,
		"languages": []string{"go", "python"},
		"stats": map[string]any{
			"total_files":   100,
			"indexed_files": 100,
			"total_chunks":  450,
			"total_symbols": 82,
		},
	}
}

func TestRunStatus_Indexed(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash) && r.Method == "GET":
			writeJSON(w, 200, projectResponse(proj, "indexed"))
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := statusProject
	defer func() { statusProject = old }()
	statusProject = proj

	out, err := captureOutput(func() error {
		return runStatus(nil, nil)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "✓ Indexed") {
		t.Errorf("expected indexed status, got:\n%s", out)
	}
	if !strings.Contains(out, "100") {
		t.Errorf("expected file count in output, got:\n%s", out)
	}
}

func TestRunStatus_Indexing(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/index/status"):
			writeJSON(w, 200, map[string]any{
				"status": "indexing",
				"progress": map[string]any{
					"phase":               "embedding",
					"files_processed":     40.0,
					"files_total":         100.0,
					"chunks_created":      120.0,
					"elapsed_seconds":     5.0,
					"estimated_remaining": 7.5,
				},
			})
		case strings.Contains(r.URL.Path, hash) && r.Method == "GET":
			writeJSON(w, 200, projectResponse(proj, "indexing"))
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := statusProject
	defer func() { statusProject = old }()
	statusProject = proj

	out, err := captureOutput(func() error {
		return runStatus(nil, nil)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "⏳ Indexing") {
		t.Errorf("expected indexing status, got:\n%s", out)
	}
	if !strings.Contains(out, "40/100") {
		t.Errorf("expected progress in output, got:\n%s", out)
	}
}

func TestRunStatus_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash):
			apiError(w, 404, "Project not found")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := statusProject
	defer func() { statusProject = old }()
	statusProject = proj

	_, err := captureOutput(func() error {
		return runStatus(nil, nil)
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "get project") {
		t.Errorf("expected 'get project' in error, got: %v", err)
	}
}
