package indexer

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/client"
)

// projectHash mirrors the client's URL-encoding logic (SHA1, first 16 hex chars).
func projectHash(path string) string {
	h := sha1.Sum([]byte(path))
	return fmt.Sprintf("%x", h)[:16]
}

// sha256hex computes the hex-encoded SHA-256 of b, matching discovery.hashFile.
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// writeFile creates a file with the given content inside dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
	return path
}

// indexHandler builds a test HTTP server that implements the three-phase index
// protocol.  beginHashes is returned verbatim from BeginIndex; filesCapture
// and finishCapture receive the request bodies for inspection.
type indexHandler struct {
	hash          string
	beginHashes   map[string]string
	FilesReceived []client.FilePayload
	DeletedPaths  []string
}

func (h *indexHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path

	switch {
	case strings.Contains(p, h.hash+"/index/begin"):
		json.NewEncoder(w).Encode(map[string]any{
			"run_id":        "run-test",
			"stored_hashes": h.beginHashes,
		})

	case strings.Contains(p, h.hash+"/index/files"):
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Files []client.FilePayload `json:"files"`
		}
		_ = json.Unmarshal(body, &payload)
		h.FilesReceived = append(h.FilesReceived, payload.Files...)
		json.NewEncoder(w).Encode(map[string]any{
			"files_accepted":        len(payload.Files),
			"chunks_created":        len(payload.Files),
			"files_processed_total": len(payload.Files),
		})

	case strings.Contains(p, h.hash+"/index/finish"):
		body, _ := io.ReadAll(r.Body)
		var finish struct {
			DeletedPaths []string `json:"deleted_paths"`
		}
		_ = json.Unmarshal(body, &finish)
		h.DeletedPaths = finish.DeletedPaths
		json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"files_processed": len(h.FilesReceived),
			"chunks_created":  len(h.FilesReceived),
		})

	default:
		http.NotFound(w, r)
	}
}

func newServer(t *testing.T, dir string, storedHashes map[string]string) (*httptest.Server, *indexHandler) {
	t.Helper()
	h := &indexHandler{hash: projectHash(dir), beginHashes: storedHashes}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRun_AddNewFile: file on disk, not in stored hashes → sent to server.
func TestRun_AddNewFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")

	srv, h := newServer(t, dir, map[string]string{})

	c := client.New(srv.URL, "test-key")
	result, err := Run(c, dir, false, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesDiscovered < 1 {
		t.Errorf("expected ≥1 discovered file, got %d", result.FilesDiscovered)
	}
	if len(h.FilesReceived) == 0 {
		t.Error("expected new file to be sent in SendFiles")
	}
	if len(h.DeletedPaths) != 0 {
		t.Errorf("expected no deleted paths, got %v", h.DeletedPaths)
	}
}

// TestRun_UpdatedFile: file exists on disk with a hash that differs from the
// stored one → file is included in the SendFiles batch.
func TestRun_UpdatedFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "lib.go", "package lib\n")

	srv, h := newServer(t, dir, map[string]string{
		path: "stale-hash-that-does-not-match",
	})

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, false, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, f := range h.FilesReceived {
		if f.Path == path {
			found = true
		}
	}
	if !found {
		t.Errorf("expected updated file %q in SendFiles, got %v", path, h.FilesReceived)
	}
}

// TestRun_DeletedFile: file listed in stored hashes but absent from disk →
// appears in deleted_paths sent to FinishIndex.
func TestRun_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "live.go", "package main\n")
	ghost := filepath.Join(dir, "removed.go") // not on disk

	srv, h := newServer(t, dir, map[string]string{
		ghost: "some-hash",
	})

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, false, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, p := range h.DeletedPaths {
		if p == ghost {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in deleted_paths, got %v", ghost, h.DeletedPaths)
	}
}

// TestRun_NoChanges: stored hashes match disk hashes exactly → SendFiles is
// not called, but FinishIndex is still called to close the session.
func TestRun_NoChanges(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n"
	path := writeFile(t, dir, "stable.go", content)

	// Provide the exact sha256 that discovery.hashFile would compute.
	storedHash := sha256hex([]byte(content))

	filesCalled := false
	hash := projectHash(dir)
	finishCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.Contains(p, hash+"/index/begin"):
			json.NewEncoder(w).Encode(map[string]any{
				"run_id":        "run-noop",
				"stored_hashes": map[string]string{path: storedHash},
			})
		case strings.Contains(p, hash+"/index/files"):
			filesCalled = true
			w.WriteHeader(500) // should not be reached
		case strings.Contains(p, hash+"/index/finish"):
			finishCalled = true
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "files_processed": 0, "chunks_created": 0,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, false, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filesCalled {
		t.Error("SendFiles should not be called when no files changed")
	}
	if !finishCalled {
		t.Error("FinishIndex should be called even when no files changed")
	}
}

// TestRun_FullReindex: full=true sends all files regardless of stored hashes.
func TestRun_FullReindex(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n"
	path := writeFile(t, dir, "main.go", content)

	// Stored hash matches → in incremental mode this file would be skipped.
	storedHash := sha256hex([]byte(content))

	srv, h := newServer(t, dir, map[string]string{path: storedHash})

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, true /* full */, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, f := range h.FilesReceived {
		if f.Path == path {
			found = true
		}
	}
	if !found {
		t.Errorf("full reindex should send all files; %q not found in %v", path, h.FilesReceived)
	}
}

// TestRun_ServerUnavailable: BeginIndex fails → Run returns an error without
// panicking.
func TestRun_ServerUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")

	// Closed server → connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, false, 0)
	if err == nil {
		t.Fatal("expected error when server is unavailable, got nil")
	}
}

// TestRun_ServerError5xx: a 503 from BeginIndex propagates as an error.
func TestRun_ServerError5xx(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	hash := projectHash(dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, hash+"/index/begin") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]string{"detail": "service unavailable"})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.New(srv.URL, "test-key")
	_, err := Run(c, dir, false, 0)
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "begin index") {
		t.Errorf("expected 'begin index' in error, got: %v", err)
	}
}

// TestRun_RecoveryAfterFailure: after a transient BeginIndex failure, the next
// Run call re-discovers all files on disk and sends them successfully (no
// changes are permanently lost).
func TestRun_RecoveryAfterFailure(t *testing.T) {
	dir := t.TempDir()
	filePath := writeFile(t, dir, "main.go", "package main\n")

	// Round 1: server is down.
	downSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	downSrv.Close()

	c1 := client.New(downSrv.URL, "test-key")
	if _, err := Run(c1, dir, false, 0); err == nil {
		t.Fatal("expected error on first run")
	}

	// Round 2: server is back; stored hashes are empty (previous run never
	// committed), so all files appear as new.
	srv, h := newServer(t, dir, map[string]string{})

	c2 := client.New(srv.URL, "test-key")
	_, err := Run(c2, dir, false, 0)
	if err != nil {
		t.Fatalf("expected recovery run to succeed: %v", err)
	}

	found := false
	for _, f := range h.FilesReceived {
		if f.Path == filePath {
			found = true
		}
	}
	if !found {
		t.Errorf("recovery run should include %q; got %v", filePath, h.FilesReceived)
	}
}

// TestRun_MultipleFiles: multiple new files are all sent in the batch.
func TestRun_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	writeFile(t, dir, "b.go", "package b\n")
	writeFile(t, dir, "c.go", "package c\n")

	srv, h := newServer(t, dir, map[string]string{})

	c := client.New(srv.URL, "test-key")
	result, err := Run(c, dir, false, 1 /* batchSize=1 to exercise batching */)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesDiscovered < 3 {
		t.Errorf("expected ≥3 discovered files, got %d", result.FilesDiscovered)
	}
	if len(h.FilesReceived) < 3 {
		t.Errorf("expected 3 files sent, got %d", len(h.FilesReceived))
	}
}