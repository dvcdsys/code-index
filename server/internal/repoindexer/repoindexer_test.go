package repoindexer

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests focus on the parts that don't require the embeddings sidecar:
// file filtering, walk pruning, and binary detection. The full pipeline
// (IndexDir) is exercised by the integration test in httpapi that
// stands up a fake indexer service.

func TestBuildPayloadSkipsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.go")
	// 600 KiB — over the 512 KiB default cap.
	if err := os.WriteFile(bigPath, make([]byte, 600*1024), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, err := buildPayload(bigPath, "big.go", DefaultFilter())
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if ok {
		t.Fatalf("expected oversized file to be skipped")
	}
}

func TestBuildPayloadSkipsBinaries(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "x.go")
	// Embed a NUL near the start — flips the binary heuristic.
	content := append([]byte("package x\n"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(binPath, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, err := buildPayload(binPath, "x.go", DefaultFilter())
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if ok {
		t.Fatalf("expected binary-looking content to be skipped")
	}
}

func TestBuildPayloadAcceptsRegular(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	src := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fp, ok, err := buildPayload(p, "main.go", DefaultFilter())
	if err != nil || !ok {
		t.Fatalf("expected ok payload, got ok=%v err=%v", ok, err)
	}
	if fp.Language != "go" {
		t.Fatalf("language detection wrong: %q", fp.Language)
	}
	if fp.Content != src {
		t.Fatalf("content mismatch")
	}
	if fp.ContentHash == "" {
		t.Fatalf("hash empty")
	}
	if fp.Path != "main.go" {
		t.Fatalf("path mismatch: %q", fp.Path)
	}
}

func TestBuildPayloadSkipsUnknownLanguage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "README.unknown_ext_zz")
	if err := os.WriteFile(p, []byte("plain text"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, _ := buildPayload(p, "README.unknown_ext_zz", DefaultFilter())
	if ok {
		t.Fatalf("expected unknown extension to be skipped")
	}
}

func TestShouldSkipDir(t *testing.T) {
	f := DefaultFilter()
	root := "/tmp/root"
	cases := map[string]bool{
		"node_modules": true,
		".git":         true,
		".venv":        true,
		"vendor":       true,
		"src":          false,
		"pkg":          false,
	}
	for name, want := range cases {
		got := f.shouldSkipDir(filepath.Join(root, name), root, name)
		if got != want {
			t.Errorf("shouldSkipDir(%q) = %v, want %v", name, got, want)
		}
	}
	// Root itself must never be skipped, even if its name matches.
	if f.shouldSkipDir(root, root, ".git") {
		t.Fatal("root directory should never be skipped")
	}
}
