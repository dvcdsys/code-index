package vectorstore

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DetectLegacyAndBackup checks whether dir contains a ChromaDB layout (Python
// backend) and, if so, renames it to a timestamped backup path so the Go
// server can start fresh. Returns backed=true when a backup was made.
//
// ChromaDB marker: chroma.sqlite3 exists AND no *.gob files exist (chromem-go
// format). We check the positive+negative so that a partially-migrated dir is
// not accidentally backed up twice.
func DetectLegacyAndBackup(dir string) (backed bool, err error) {
	if _, err := os.Stat(filepath.Join(dir, "chroma.sqlite3")); os.IsNotExist(err) {
		return false, nil
	}

	gobFiles, _ := filepath.Glob(filepath.Join(dir, "*.gob"))
	if len(gobFiles) > 0 {
		return false, nil
	}

	backup := fmt.Sprintf("%s.python-backup.%s", dir, time.Now().Format("20060102-150405"))
	if err := os.Rename(dir, backup); err != nil {
		return false, fmt.Errorf("backup legacy chroma dir: %w", err)
	}
	return true, nil
}

// WarnIfNamespaceOrphaned logs a prominent warning when the active vector-
// store directory (activeDir) does not exist yet but sibling chroma
// namespace dirs do. That pattern means vectors were previously indexed
// under a different provider identity (or the pre-unification layout) and
// the active provider will start empty — so search silently returns
// nothing on an otherwise-healthy server. Surfacing it turns a silent
// data-orphan into an actionable log line: the operator can switch back to
// the prior provider or reindex. Best-effort; returns an error only when
// the directory scan itself fails.
func WarnIfNamespaceOrphaned(chromaBase, activeDir string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if chromaBase == "" {
		return nil
	}
	// Active namespace already present → nothing orphaned.
	if _, err := os.Stat(activeDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	parent := filepath.Dir(chromaBase)
	prefix := filepath.Base(chromaBase) + "_"
	activeName := filepath.Base(activeDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing indexed yet
		}
		return err
	}
	var siblings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == activeName || !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.Contains(name, ".python-backup.") {
			continue // already-backed-up legacy layout, handled separately
		}
		siblings = append(siblings, name)
	}
	if len(siblings) > 0 {
		logger.Warn("active embedding-provider vector namespace is empty but other namespaces exist — prior vectors are under a different provider identity/layout; switch back to the prior provider or reindex to populate this one",
			"active_dir", activeName, "other_namespaces", siblings)
	}
	return nil
}
