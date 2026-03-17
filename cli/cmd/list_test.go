package cmd

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunList_Projects(t *testing.T) {
	now := time.Now()

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{
						"host_path":       "/home/user/projectA",
						"status":          "indexed",
						"languages":       []string{"go"},
						"stats":           map[string]any{"total_files": 50, "total_chunks": 200, "total_symbols": 30},
						"last_indexed_at": now.Format(time.RFC3339),
					},
					{
						"host_path": "/home/user/projectB",
						"status":    "created",
						"languages": []string{},
						"stats":     map[string]any{"total_files": 0, "total_chunks": 0, "total_symbols": 0},
					},
				},
				"total": 2,
			})
			return
		}
		http.NotFound(w, r)
	})
	useAPI(t, srv)

	out, err := captureOutput(func() error {
		return runList(nil, nil)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "projectA") {
		t.Errorf("expected projectA in output, got:\n%s", out)
	}
	if !strings.Contains(out, "projectB") {
		t.Errorf("expected projectB in output, got:\n%s", out)
	}
	if !strings.Contains(out, "2 project") {
		t.Errorf("expected project count in output, got:\n%s", out)
	}
	// indexed project should show ✓
	if !strings.Contains(out, "✓") {
		t.Errorf("expected ✓ icon for indexed project, got:\n%s", out)
	}
}

func TestRunList_Empty(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
			return
		}
		http.NotFound(w, r)
	})
	useAPI(t, srv)

	out, err := captureOutput(func() error {
		return runList(nil, nil)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No projects") {
		t.Errorf("expected 'No projects', got:\n%s", out)
	}
}

func TestRunList_APIError(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "internal error")
	})
	useAPI(t, srv)

	_, err := captureOutput(func() error {
		return runList(nil, nil)
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "list projects") {
		t.Errorf("expected 'list projects' in error, got: %v", err)
	}
}
