package vectorstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// ---------------------------------------------------------------------------
// Schema upgrade: v1 (or unstamped) -> v2.
//
// v2 differs from v1 in three ways, and none of them can be reached with ALTER
// TABLE:
//
//   - collections.id gains AUTOINCREMENT. SQLite stores that in the table's
//     declared SQL and in the sqlite_sequence table; there is no way to add it
//     to an existing table.
//   - vectors and vector_contents gain a REFERENCES ... ON DELETE CASCADE
//     clause. Foreign keys are part of the CREATE TABLE text too.
//   - auto_vacuum goes from NONE to INCREMENTAL, which SQLite only honours on
//     a database with no tables yet, or after a full VACUUM.
//
// So the upgrade is a rebuild: a sibling temp file gets the v2 schema and the
// v2 file pragmas, the old data is copied into it, and it is renamed over the
// original. The rebuild IS the vacuum — the copy lays every page down densely
// — which is why all three arrive in one pass and why doing it now, while the
// only database in existence is a development one, is as cheap as it will ever
// be.
//
// A v1 file may hold rows whose collection_id has no collections row (that is
// the bug v2 exists to stop). They cannot be copied into a database that
// enforces the constraint, so the copy filters them out and says how many it
// dropped. They were unreachable already: ListCollections joins FROM
// collections, so nothing has counted, sized, or searched them.
// ---------------------------------------------------------------------------

// upgradeTempSuffix names the in-progress rebuild. A leftover with this suffix
// is a rebuild that died; it is removed and the rebuild starts over, because
// the original is untouched until the rename.
const upgradeTempSuffix = ".upgrade-tmp"

// upgradeSchema brings an existing database at path up to schemaVersion,
// rebuilding it if needed. A file already at schemaVersion (or newer, which
// means a downgrade — left alone rather than destroyed) is untouched.
func upgradeSchema(drv driver.Driver, path string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	version, err := readUserVersion(drv, path)
	if err != nil {
		return err
	}
	if version >= schemaVersion {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("vectorstore: stat %s: %w", path, err)
	}
	// The rebuild writes a second copy beside the original and only then
	// replaces it, so the peak requirement is the file size itself.
	if err := checkFreeSpace(filepath.Dir(path), info.Size()); err != nil {
		return fmt.Errorf("vectorstore: cannot upgrade the vector store schema: %w", err)
	}

	// WARN for the same reason the legacy import logs at warn (see
	// importLegacyChromem): production runs at warn level and nothing answers
	// HTTP until the store is open, so an unannounced multi-minute rebuild is
	// indistinguishable from a hung server.
	started := time.Now()
	logger.Warn("upgrading vector store schema: rebuilding the database",
		"db", path, "from_version", version, "to_version", schemaVersion,
		"bytes", info.Size())

	tmp := path + upgradeTempSuffix
	if err := removeDBFiles(tmp); err != nil {
		return err
	}
	dropped, err := rebuildInto(drv, path, tmp)
	if err != nil {
		_ = removeDBFiles(tmp)
		return fmt.Errorf("vectorstore: rebuild %s to schema v%d: %w", path, schemaVersion, err)
	}
	if err := commitRebuild(tmp, path); err != nil {
		_ = removeDBFiles(tmp)
		return err
	}

	logger.Warn("vector store schema upgrade complete",
		"db", path, "version", schemaVersion,
		"orphan_rows_dropped", dropped,
		"elapsed", time.Since(started).Round(time.Millisecond).String())
	return nil
}

// readUserVersion opens path just long enough to read PRAGMA user_version.
// A database written before the stamp existed reports 0.
func readUserVersion(drv driver.Driver, path string) (int, error) {
	db := sql.OpenDB(&pragmaConnector{drv: drv, dsn: "file:" + path, pragmas: []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS),
	}})
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("vectorstore: read schema version of %s: %w", path, err)
	}
	return v, nil
}

// rebuildInto creates dst with the v2 schema and copies src into it, returning
// how many orphan rows were left behind.
//
// dst is deliberately NOT put into WAL mode: a rolled-back journal is gone by
// the time this returns, whereas a -wal would hold committed frames that the
// rename would strand. The first real open puts the renamed file into WAL.
func rebuildInto(drv driver.Driver, src, dst string) (dropped int64, err error) {
	db := sql.OpenDB(&pragmaConnector{drv: drv, dsn: "file:" + dst, pragmas: []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS),
		// FULL, not NORMAL: this connection's commit is the only durability
		// barrier before the rename publishes the file.
		"PRAGMA synchronous=FULL",
	}})
	defer db.Close()

	ctx := context.Background()
	// One connection for the whole rebuild: page_size, auto_vacuum and the
	// ATTACH are all connection- or file-scoped, and a pool would spread them
	// over connections that do not share them.
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// Before the first byte is written, exactly as on a fresh database.
	for _, p := range []string{
		fmt.Sprintf("PRAGMA page_size=%d", pageSize),
		"PRAGMA auto_vacuum=INCREMENTAL",
	} {
		if _, err := conn.ExecContext(ctx, p); err != nil {
			return 0, fmt.Errorf("apply %q: %w", p, err)
		}
	}
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return 0, fmt.Errorf("create schema: %w", err)
	}

	// foreign_keys stays OFF on this connection: the copy inserts parents
	// before children but the orphan filter, not the constraint, is what has
	// to decide what survives — and an enforced constraint would abort the
	// whole rebuild on the first orphan instead of reporting it.
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS old`, src); err != nil {
		return 0, fmt.Errorf("attach %s: %w", src, err)
	}
	defer conn.ExecContext(ctx, `DETACH DATABASE old`) //nolint:errcheck // best effort on the way out

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	// collections first: inserting the original ids into an AUTOINCREMENT
	// column carries the id space forward, so sqlite_sequence ends at the old
	// maximum and no id that was ever handed out can be handed out again.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO main.collections(id, name) SELECT id, name FROM old.collections`); err != nil {
		return 0, fmt.Errorf("copy collections: %w", err)
	}
	for _, c := range []struct {
		table string
		stmt  string
	}{
		{"vectors", `INSERT INTO main.vectors
			(collection_id, doc_id, file_path, start_line, end_line, chunk_type, symbol_name, language, embedding)
			SELECT collection_id, doc_id, file_path, start_line, end_line, chunk_type, symbol_name, language, embedding
			  FROM old.vectors
			 WHERE collection_id IN (SELECT id FROM old.collections)`},
		{"vector_contents", `INSERT INTO main.vector_contents (collection_id, doc_id, content)
			SELECT collection_id, doc_id, content
			  FROM old.vector_contents
			 WHERE collection_id IN (SELECT id FROM old.collections)`},
	} {
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM old.`+c.table).Scan(&total); err != nil {
			return 0, fmt.Errorf("count %s: %w", c.table, err)
		}
		res, err := tx.ExecContext(ctx, c.stmt)
		if err != nil {
			return 0, fmt.Errorf("copy %s: %w", c.table, err)
		}
		copied, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("copy %s: %w", c.table, err)
		}
		dropped += total - copied
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO main.migration_state(collection_name, migrated_at, docs)
		 SELECT collection_name, migrated_at, docs FROM old.migration_state`); err != nil {
		return 0, fmt.Errorf("copy migration_state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA main.user_version=%d", schemaVersion)); err != nil {
		return 0, fmt.Errorf("stamp schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return dropped, nil
}

// commitRebuild makes tmp the database at path: fsync the rebuilt file, drop
// the original's sidecars, rename the rebuilt file over the original, and
// fsync the directory so the rename itself survives a crash.
//
// The sidecars go BEFORE the rename so there is never a moment where the new
// database sits next to the old file's -wal/-shm. The old pair was fully
// checkpointed into the data the rebuild copied, so removing it first loses
// nothing — the crash window merely re-runs the rebuild — while removing it
// after would rely on SQLite ignoring a WAL whose salts don't match the
// renamed file's header. That happens to be true, but the ordering makes the
// argument unnecessary.
func commitRebuild(tmp, path string) error {
	if err := fsyncPath(tmp); err != nil {
		return fmt.Errorf("vectorstore: sync %s: %w", tmp, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("vectorstore: remove stale %s: %w", path+suffix, err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vectorstore: replace %s: %w", path, err)
	}
	if err := fsyncPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("vectorstore: sync %s: %w", filepath.Dir(path), err)
	}
	return nil
}

// removeDBFiles deletes a database and its sidecars, tolerating absence.
func removeDBFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("vectorstore: remove %s: %w", p, err)
		}
	}
	return nil
}

// fsyncPath flushes a file or directory to stable storage.
func fsyncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
