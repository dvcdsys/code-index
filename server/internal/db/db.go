// Package db opens the SQLite database used by the Go server. Pure-Go driver
// via modernc.org/sqlite (CGO-free). Parity with api/app/database.py on DDL
// and PRAGMAs (WAL + foreign_keys ON).
package db

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DriverName is the registered database/sql driver name for modernc.org/sqlite.
const DriverName = "sqlite"

// Open opens (and creates if necessary) the SQLite database at path, sets the
// required PRAGMAs via the DSN, and runs the schema migration. Pass ":memory:"
// for an in-memory DB (used by tests).
func Open(path string) (*sql.DB, error) {
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir db parent: %w", err)
		}
	}

	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	// modernc's sqlite driver holds per-connection pragmas, so force a single
	// connection for in-memory DBs (otherwise each new conn has an empty DB).
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		// m10 — cap the pool for file-backed DBs. modernc is WAL-safe with
		// multiple connections but leaving the pool unbounded lets burst
		// traffic spawn dozens of connections on contention. 8 writers + 4
		// idle is plenty for a single-node server.
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db.Ping: %w", err)
	}

	if _, err := db.Exec(Schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	// m7 — migrate existing databases that pre-date the path_hash column.
	// We add the column + index if absent, then backfill in a single pass.
	if err := migratePathHash(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate path_hash: %w", err)
	}

	// PR-E — add indexed_with_model to projects on pre-PR-E databases. Same
	// PRAGMA-table_info pattern as migratePathHash; no backfill (NULL means
	// "indexed before drift tracking landed" — UI renders this as Unknown,
	// not as a stale-model warning).
	if err := migrateIndexedWithModel(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate indexed_with_model: %w", err)
	}

	return db, nil
}

// migratePathHash brings older databases up to the current schema by adding
// the path_hash column when missing and backfilling it from host_path. The
// schema DDL is idempotent via CREATE TABLE IF NOT EXISTS so we rely on
// PRAGMA table_info to detect whether the column exists.
func migratePathHash(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(projects)`)
	if err != nil {
		return fmt.Errorf("table_info: %w", err)
	}
	haveColumn := false
	for rows.Next() {
		var (
			cid                 int
			name, typ           string
			notnull, pk         int
			dflt                sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "path_hash" {
			haveColumn = true
		}
	}
	rows.Close()

	if !haveColumn {
		if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN path_hash TEXT`); err != nil {
			return fmt.Errorf("add path_hash column: %w", err)
		}
	}

	// Always create the index — Schema.Exec no longer does it because a
	// pre-m7 projects table lacks the column and would fail the whole DDL
	// batch. IF NOT EXISTS makes this idempotent on fresh DBs.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_projects_path_hash ON projects(path_hash)`); err != nil {
		return fmt.Errorf("create path_hash index: %w", err)
	}

	// Backfill any NULL path_hash rows (covers both fresh migrations and
	// legacy rows inserted before Create() began populating the column).
	hostPaths := []string{}
	qr, err := db.Query(`SELECT host_path FROM projects WHERE path_hash IS NULL OR path_hash = ''`)
	if err != nil {
		return fmt.Errorf("select projects to backfill: %w", err)
	}
	for qr.Next() {
		var hp string
		if err := qr.Scan(&hp); err != nil {
			qr.Close()
			return err
		}
		hostPaths = append(hostPaths, hp)
	}
	qr.Close()
	for _, hp := range hostPaths {
		if _, err := db.Exec(`UPDATE projects SET path_hash = ? WHERE host_path = ?`, HashHostPath(hp), hp); err != nil {
			return fmt.Errorf("backfill path_hash: %w", err)
		}
	}
	return nil
}

// migrateIndexedWithModel adds projects.indexed_with_model to pre-PR-E
// databases. Idempotent: PRAGMA table_info first; ALTER only if absent. Rows
// stay NULL — the dashboard treats NULL as "indexed before drift tracking
// existed" and renders a neutral Unknown badge rather than the destructive
// drift highlight.
func migrateIndexedWithModel(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(projects)`)
	if err != nil {
		return fmt.Errorf("table_info: %w", err)
	}
	have := false
	for rows.Next() {
		var (
			cid         int
			name, typ   string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "indexed_with_model" {
			have = true
		}
	}
	rows.Close()
	if have {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN indexed_with_model TEXT`); err != nil {
		return fmt.Errorf("add indexed_with_model column: %w", err)
	}
	return nil
}

// HashHostPath returns the 16-char SHA1 prefix used as the URL segment for
// projects. Exported so projects.Create and the migration share one
// implementation (keep it byte-identical to projects.HashPath).
func HashHostPath(path string) string {
	h := sha1.New()
	h.Write([]byte(path))
	b := h.Sum(nil)
	const hexchars = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i*2] = hexchars[b[i]>>4]
		out[i*2+1] = hexchars[b[i]&0xf]
	}
	return string(out)
}

// buildDSN constructs a modernc.org/sqlite DSN with WAL, foreign keys on, and
// a 5-second busy timeout.
func buildDSN(path string) (string, error) {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "foreign_keys(ON)")
	v.Add("_pragma", "busy_timeout(5000)")

	if path == ":memory:" {
		return ":memory:?" + v.Encode(), nil
	}
	if path == "" {
		return "", fmt.Errorf("empty db path")
	}
	return "file:" + path + "?" + v.Encode(), nil
}
