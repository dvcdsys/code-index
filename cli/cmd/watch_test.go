package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProjectPath_NoArgs_UsesCwd(t *testing.T) {
	srv := mockServer(t, listProjectsHandler(nil))
	useAPI(t, srv)

	got, err := resolveProjectPath(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cwd, _ := os.Getwd()
	if got != cwd {
		t.Errorf("expected %q, got %q", cwd, got)
	}
}

func TestResolveProjectPath_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	srv := mockServer(t, listProjectsHandler(nil))
	useAPI(t, srv)

	got, err := resolveProjectPath([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != dir {
		t.Errorf("expected %q, got %q", dir, got)
	}
}

func TestResolveProjectPath_NonExistentPath(t *testing.T) {
	srv := mockServer(t, listProjectsHandler(nil))
	useAPI(t, srv)

	_, err := resolveProjectPath([]string{"/no/such/path/xyzzy"})
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' in error, got: %v", err)
	}
}

func TestResolveProjectPath_SubdirResolvesToProjectRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src", "api")
	os.MkdirAll(sub, 0755)

	srv := mockServer(t, listProjectsHandler([]string{root}))
	useAPI(t, srv)

	got, err := resolveProjectPath([]string{sub})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != root {
		t.Errorf("expected subdirectory %q to resolve to project root %q, got %q", sub, root, got)
	}
}

func TestResolveProjectPath_DeepSubdirResolvesToProjectRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "src", "api", "handlers", "auth")
	os.MkdirAll(deep, 0755)

	srv := mockServer(t, listProjectsHandler([]string{root}))
	useAPI(t, srv)

	got, err := resolveProjectPath([]string{deep})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != root {
		t.Errorf("expected deep subdirectory %q to resolve to project root %q, got %q", deep, root, got)
	}
}

func TestResolveProjectPath_UnregisteredProject_ReturnsAsIs(t *testing.T) {
	dir := t.TempDir()

	srv := mockServer(t, listProjectsHandler([]string{"/some/other/project"}))
	useAPI(t, srv)

	got, err := resolveProjectPath([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != dir {
		t.Errorf("expected unregistered path %q returned as-is, got %q", dir, got)
	}
}

func TestResolveProjectPath_APIUnavailable_ReturnsAbsPath(t *testing.T) {
	dir := t.TempDir()

	// No useAPI — apiURL/apiKey are empty, getClient will fail
	prev, prevKey := apiURL, apiKey
	apiURL = ""
	apiKey = ""
	defer func() { apiURL = prev; apiKey = prevKey }()

	got, err := resolveProjectPath([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != dir {
		t.Errorf("expected fallback to abs path %q, got %q", dir, got)
	}
}