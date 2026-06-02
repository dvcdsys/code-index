package vectorstore

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWarnIfNamespaceOrphaned(t *testing.T) {
	mkdir := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("warns when active namespace empty but siblings exist", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "chroma")
		// A prior provider's namespace exists on disk; the active one does not.
		mkdir(t, base+"_ollama_m")
		active := base + "_voyage_voyage_code_3_2048_float"

		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		if err := WarnIfNamespaceOrphaned(base, active, logger); err != nil {
			t.Fatalf("WarnIfNamespaceOrphaned: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "other namespaces") || !strings.Contains(out, "chroma_ollama_m") {
			t.Errorf("expected an orphan warning naming the sibling dir, got:\n%s", out)
		}
	})

	t.Run("silent when active namespace already present", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "chroma")
		active := base + "_voyage_x"
		mkdir(t, active)           // active dir exists
		mkdir(t, base+"_ollama_m") // and a sibling too — still must not warn

		var buf bytes.Buffer
		if err := WarnIfNamespaceOrphaned(base, active, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
			t.Fatalf("WarnIfNamespaceOrphaned: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("expected no log output when active dir present, got:\n%s", buf.String())
		}
	})

	t.Run("silent on a fresh install with no namespaces", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "chroma")
		active := base + "_ollama_m"

		var buf bytes.Buffer
		if err := WarnIfNamespaceOrphaned(base, active, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
			t.Fatalf("WarnIfNamespaceOrphaned: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("expected no log output on fresh install, got:\n%s", buf.String())
		}
	})

	t.Run("ignores python-backup dirs as siblings", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "chroma")
		mkdir(t, base+"_ollama_m.python-backup.20250101-000000")
		active := base + "_voyage_x"

		var buf bytes.Buffer
		if err := WarnIfNamespaceOrphaned(base, active, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
			t.Fatalf("WarnIfNamespaceOrphaned: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("python-backup dir should not count as an orphaned namespace, got:\n%s", buf.String())
		}
	})
}
