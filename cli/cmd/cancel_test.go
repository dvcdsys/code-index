package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunCancel_ActiveSession(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/index/cancel") && r.Method == http.MethodPost:
			writeJSON(w, 200, map[string]any{"cancelled": true})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := cancelProject
	defer func() { cancelProject = old }()
	cancelProject = proj

	out, err := captureOutput(func() error {
		return runCancel(nil, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Cancelled active indexing session") {
		t.Errorf("expected success message, got:\n%s", out)
	}
}

func TestRunCancel_NoActiveSession(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/index/cancel"):
			writeJSON(w, 200, map[string]any{"cancelled": false})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := cancelProject
	defer func() { cancelProject = old }()
	cancelProject = proj

	out, err := captureOutput(func() error {
		return runCancel(nil, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No active session") {
		t.Errorf("expected idempotent message, got:\n%s", out)
	}
}

func TestRunCancel_APIError(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)

	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/index/cancel"):
			apiError(w, 500, "internal error")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	old := cancelProject
	defer func() { cancelProject = old }()
	cancelProject = proj

	_, err := captureOutput(func() error {
		return runCancel(nil, nil)
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("expected 'cancel' in error, got: %v", err)
	}
}
