package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenInMemoryAppliesSchema(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	rows, err := database.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	sort.Strings(got)
	want := append([]string(nil), ExpectedTables...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("table count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("table[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var fk int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// TestOpenMigratesPreM7DB simulates a pre-m7 database (projects table without
// path_hash column, no idx_projects_path_hash index) and verifies Open
// migrates it cleanly. This regression-tests the 2026-04-25 production
// incident where a CREATE INDEX inside the Schema const ran against a
// pre-m7 DB and crashed with "no such column: path_hash".
func TestOpenMigratesPreM7DB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m7.db")

	// Stage a pre-m7 projects table manually so we don't depend on the
	// current Schema const. Using the raw driver avoids going through Open().
	seed, err := sql.Open(DriverName, "file:"+tmp)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE projects (
		host_path TEXT PRIMARY KEY,
		container_path TEXT NOT NULL,
		languages TEXT DEFAULT '[]',
		settings TEXT DEFAULT '{}',
		stats TEXT DEFAULT '{}',
		status TEXT DEFAULT 'created',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_indexed_at TEXT
	)`); err != nil {
		t.Fatalf("seed CREATE TABLE: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES ('/legacy/proj', '/legacy/proj', '2024-01-01', '2024-01-01')`,
	); err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}
	seed.Close()

	// Open must migrate (not crash) and backfill path_hash.
	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m7 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	var hash sql.NullString
	if err := database.QueryRow(
		`SELECT path_hash FROM projects WHERE host_path = ?`, "/legacy/proj",
	).Scan(&hash); err != nil {
		t.Fatalf("select path_hash: %v", err)
	}
	if !hash.Valid || hash.String == "" {
		t.Fatalf("path_hash not backfilled: %+v", hash)
	}
	if want := HashHostPath("/legacy/proj"); hash.String != want {
		t.Errorf("path_hash = %q, want %q", hash.String, want)
	}

	var idxCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_projects_path_hash'`,
	).Scan(&idxCount); err != nil {
		t.Fatalf("idx count: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("idx_projects_path_hash count = %d, want 1", idxCount)
	}
}

func TestSymbolsIndexExists(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	row := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_symbols_project_name'`,
	)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 1 {
		t.Errorf("idx_symbols_project_name count = %d, want 1", n)
	}
}
