package embeddings

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestImportBootstrapGGUF_HappyPath covers the typical Docker scenario: a
// fresh cix-models volume + a bind-mounted source GGUF outside the cache.
// First import copies the file into the cache layout; the second call is
// a no-op (idempotent) and returns the existing path.
func TestImportBootstrapGGUF_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "cache")
	srcPath := filepath.Join(tmp, "src", "model.gguf")

	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	payload := []byte("not really a gguf, but bytes are bytes")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	repo := "owner/repo-Q8"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	got, err := importBootstrapGGUF(cacheDir, repo, srcPath, logger)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := filepath.Join(cacheDir, "owner__repo-Q8", "model.gguf")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(got); err != nil {
		t.Fatalf("read imported: %v", err)
	} else if string(data) != string(payload) {
		t.Errorf("imported file content mismatch")
	}

	// Second import = no-op: target already exists, must return same path.
	got2, err := importBootstrapGGUF(cacheDir, repo, srcPath, logger)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if got2 != want {
		t.Errorf("second call path = %q, want %q", got2, want)
	}

	// And no `.partial` file should be left around.
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "owner__repo-Q8", "*.partial"))
	if len(matches) > 0 {
		t.Errorf("leftover partials: %v", matches)
	}
}

// TestImportBootstrapGGUF_MissingSource — a missing source isn't an error;
// it returns ("", nil) so resolveGGUFPath can fall through to HF download.
// This matches the "operator set the env optimistically" use case.
func TestImportBootstrapGGUF_MissingSource(t *testing.T) {
	cacheDir := t.TempDir()
	got, err := importBootstrapGGUF(cacheDir, "owner/repo", filepath.Join(cacheDir, "missing.gguf"), slog.Default())
	if err != nil {
		t.Fatalf("missing source should not error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (so caller falls through to download)", got)
	}
}

// TestImportBootstrapGGUF_DirectoryRejected — passing a directory is a
// configuration mistake; we surface it loud so the operator notices.
func TestImportBootstrapGGUF_DirectoryRejected(t *testing.T) {
	cacheDir := t.TempDir()
	srcDir := filepath.Join(cacheDir, "not-a-file")
	_ = os.MkdirAll(srcDir, 0o755)
	_, err := importBootstrapGGUF(cacheDir, "owner/repo", srcDir, slog.Default())
	if err == nil {
		t.Fatal("expected error for directory source, got nil")
	}
}

// TestImportBootstrapGGUF_PreservesContents ensures we don't truncate or
// corrupt the file mid-copy. Uses a 1 MiB payload to exercise the io.Copy
// loop multiple times.
func TestImportBootstrapGGUF_PreservesContents(t *testing.T) {
	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "big.gguf")
	const size = 1 << 20 // 1 MiB
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	if err := os.WriteFile(srcPath, buf, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	got, err := importBootstrapGGUF(filepath.Join(tmp, "cache"), "x/y", srcPath, slog.Default())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Size() != size {
		t.Errorf("size = %d, want %d", info.Size(), size)
	}
	if info.Mode()&fs.ModeType != 0 {
		t.Errorf("target is not a regular file: mode=%v", info.Mode())
	}
}
