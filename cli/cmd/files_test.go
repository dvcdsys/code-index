package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunFiles_Results(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/files"):
			writeJSON(w, 200, map[string]any{
				"files": []map[string]any{
					{"path": proj + "/config/app.yaml", "language": "yaml"},
					{"path": proj + "/config/db.yaml", "language": "yaml"},
				},
				"total": 2,
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := filesProject
	defer func() { filesProject = old }()
	filesProject = proj
	filesLimit = 20

	out, err := captureOutput(func() error {
		return runFiles(nil, []string{"config"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "app.yaml") {
		t.Errorf("expected app.yaml in output, got:\n%s", out)
	}
	if !strings.Contains(out, "db.yaml") {
		t.Errorf("expected db.yaml in output, got:\n%s", out)
	}
	if !strings.Contains(out, "2 file") {
		t.Errorf("expected file count in output, got:\n%s", out)
	}
}

func TestRunFiles_EmptyResults(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/files"):
			writeJSON(w, 200, map[string]any{"files": []any{}, "total": 0})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := filesProject
	defer func() { filesProject = old }()
	filesProject = proj
	filesLimit = 20

	out, err := captureOutput(func() error {
		return runFiles(nil, []string{"nonexistent"})
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No files") {
		t.Errorf("expected 'No files', got:\n%s", out)
	}
}

func TestRunFiles_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/search/files"):
			apiError(w, 404, "project not found")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := filesProject
	defer func() { filesProject = old }()
	filesProject = proj
	filesLimit = 20

	_, err := captureOutput(func() error {
		return runFiles(nil, []string{"config"})
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
