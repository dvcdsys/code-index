package vectorstore

import (
	"fmt"
	"os"
	"path/filepath"
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
