// Package db opens the SQLite database used by the Go server. Pure-Go driver
// via modernc.org/sqlite (CGO-free). Parity with api/app/database.py on DDL
// and PRAGMAs (WAL + foreign_keys ON).
package db

import (
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
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db.Ping: %w", err)
	}

	if _, err := db.Exec(Schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return db, nil
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
