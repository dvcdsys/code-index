// Package storage holds boot-time, OS-level migrations of on-disk storage
// artefacts (the SQLite system DB file and the chromem-go vector-store
// directories). These run BEFORE the DB / vector store are opened, so
// they operate on plain files rather than live handles — which is why
// they live here rather than in internal/db or internal/vectorstore.
//
// Background. Earlier builds (a Python-era artefact ported 1:1 to Go)
// namespaced BOTH the SQLite DB and the chroma dir by the embedding model
// name. That was only safe while the model was a fixed env var; runtime
// model/provider switching turned it into a bug (vectors of different
// dimensions colliding in one collection) and a footgun (a model change
// silently spawning a parallel DB with empty accounts). The unified
// design keeps ONE model-independent system DB and namespaces ONLY the
// vector store by the active provider identity. These migrations move
// existing deployments onto that layout without a reindex.
package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"

	_ "modernc.org/sqlite"
)

// AdoptLegacyModelDB makes the model-independent system DB at target the
// canonical store, adopting a legacy per-model DB file when present.
//
// target is the literal cfg.SQLitePath (no model suffix). legacy is the
// old per-model filename (cfg.LegacyDynamicSQLitePath()). The function is
// idempotent and safe to run on every boot.
//
// Cases:
//   - legacy == target, or legacy missing → nothing to do.
//   - target missing → adopt legacy (checkpoint its WAL, rename it in).
//   - target present AND a real unified DB (has schema_migrations AND
//     users) → leave it; if a legacy file also lingers, warn that it is
//     now stale.
//   - target present but a pre-auth FOSSIL (lacks those tables) → move the
//     fossil aside to <target>.pre-unify-<timestamp> (with its WAL/SHM
//     sidecars) and adopt legacy in its place.
//
// On adoption the legacy WAL is drained via PRAGMA wal_checkpoint(TRUNCATE)
// and only the main .db file is renamed; the regenerable -wal/-shm
// sidecars are removed rather than carried across (they are
// host/inode-sensitive).
//
// LEGACY-MIGRATION (remove next release): one-time adoption shim. Once
// every deployment has booted on the unified layout, delete this function
// and its call in cmd/cix-server/main.go (and Config.ModelSafeName /
// Config.LegacyDynamicSQLitePath, which exist only to feed it).
func AdoptLegacyModelDB(target, legacy string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if target == "" || legacy == "" || legacy == target {
		return nil
	}
	if !fileExists(legacy) {
		return nil // fresh install or already adopted
	}

	if fileExists(target) {
		real, err := db.HasTables(target, "schema_migrations", "users")
		if err != nil {
			return fmt.Errorf("inspect target db %s: %w", target, err)
		}
		if real {
			logger.Warn("storage: legacy per-model DB still present but target is already the unified system DB; leaving legacy untouched (safe to delete manually)",
				"target", target, "legacy", legacy)
			return nil
		}
		// Fossil occupying the target path — move it aside.
		aside := fmt.Sprintf("%s.pre-unify-%s", target, time.Now().UTC().Format("20060102-150405"))
		logger.Warn("storage: moving pre-unification fossil DB aside to free the system DB path",
			"fossil", target, "moved_to", aside)
		if err := renameWithSidecars(target, aside); err != nil {
			return fmt.Errorf("move fossil aside: %w", err)
		}
	}

	logger.Info("storage: adopting legacy per-model DB as the model-independent system DB",
		"legacy", legacy, "target", target)
	if err := CheckpointWAL(legacy); err != nil {
		return fmt.Errorf("checkpoint legacy db %s: %w", legacy, err)
	}
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", legacy, target, err)
	}
	// The legacy WAL was truncated by the checkpoint; remove any leftover
	// regenerable sidecars so they cannot shadow the moved DB.
	removeIfExists(legacy + "-wal")
	removeIfExists(legacy + "-shm")
	return nil
}

// CheckpointWAL opens path with a single connection, drains its WAL into
// the main database file via wal_checkpoint(TRUNCATE), and closes. After
// this the main .db file is self-contained and safe to rename.
//
// Exported for internal/dbmaint, whose boot-time swap has the same
// requirement and the same trap behind it: a database renamed away from its
// -wal leaves the log shadowing whatever file later takes its name.
func CheckpointWAL(path string) error {
	// WAL + busy_timeout via the DSN; single connection so the checkpoint
	// is not racing a sibling connection.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	sdb, err := sql.Open(db.DriverName, dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer sdb.Close()
	sdb.SetMaxOpenConns(1)
	if _, err := sdb.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal_checkpoint: %w", err)
	}
	return nil
}

// renameWithSidecars renames a SQLite DB file and its -wal/-shm sidecars
// (best effort for the sidecars — they may not exist).
func renameWithSidecars(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if fileExists(from + sfx) {
			if err := os.Rename(from+sfx, to+sfx); err != nil {
				return fmt.Errorf("rename sidecar %s: %w", from+sfx, err)
			}
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeIfExists(path string) {
	if fileExists(path) {
		_ = os.Remove(path)
	}
}
