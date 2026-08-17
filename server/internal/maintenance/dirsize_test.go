package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeBytes_SumsRegularFiles(t *testing.T) {
	dir := t.TempDir()
	writeFileOfSize(t, filepath.Join(dir, "a"), 100)
	writeFileOfSize(t, filepath.Join(dir, "sub", "b"), 50)

	n, ok := DirSizeBytes(context.Background(), dir)
	if !ok {
		t.Fatal("ok = false on a readable tree")
	}
	if n != 150 {
		t.Errorf("total = %d, want 150", n)
	}
}

func TestDirSizeBytes_MissingRoot_ReportsNotOK(t *testing.T) {
	n, ok := DirSizeBytes(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if ok {
		t.Error("ok = true on a missing directory — 'unreadable' and 'empty' must stay distinguishable")
	}
	if n != 0 {
		t.Errorf("total = %d, want 0", n)
	}
}

func TestDirSizeBytes_UnreadableSubtree_ReturnsPartial(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	writeFileOfSize(t, filepath.Join(dir, "a"), 100)
	locked := filepath.Join(dir, "locked")
	writeFileOfSize(t, filepath.Join(locked, "hidden"), 999)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	n, ok := DirSizeBytes(context.Background(), dir)
	if !ok {
		t.Fatal("ok = false — one unreadable subtree must not throw the whole number away")
	}
	if n != 100 {
		t.Errorf("total = %d, want 100 (the readable part)", n)
	}
}

func TestDirSizeBytes_CancelledContext_ReportsNotOK(t *testing.T) {
	dir := t.TempDir()
	// Enough entries to guarantee the every-512-entries context check fires.
	for i := range 600 {
		writeFileOfSize(t, filepath.Join(dir, fmt.Sprintf("f%04d", i)), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := DirSizeBytes(ctx, dir); ok {
		t.Error("ok = true on a cancelled context, want false so callers omit the number")
	}
}

func writeFileOfSize(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
