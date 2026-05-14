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

	// FTS5 virtual tables create implementation-detail shadow tables
	// (chunks_fts_config, chunks_fts_content, chunks_fts_data,
	// chunks_fts_docsize, chunks_fts_idx). Exclude them — we only audit
	// the tables we explicitly declare.
	rows, err := database.Query(
		`SELECT name FROM sqlite_master
		 WHERE type='table'
		   AND name NOT LIKE 'sqlite_%'
		   AND name NOT LIKE 'chunks_fts_%'`,
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

// TestOpenMigratesPreEDB simulates a pre-PR-E database (projects table without
// indexed_with_model column, no runtime_settings table) and verifies Open
// migrates it cleanly + the new column is queryable on existing rows.
func TestOpenMigratesPreEDB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-e.db")

	// Stage a pre-PR-E projects table that already has path_hash (post-m7)
	// but lacks indexed_with_model. No runtime_settings table at all.
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
		last_indexed_at TEXT,
		path_hash TEXT
	)`); err != nil {
		t.Fatalf("seed CREATE TABLE: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		 VALUES ('/legacy/proj', '/legacy/proj', '2024-01-01', '2024-01-01', 'abc')`,
	); err != nil {
		t.Fatalf("seed INSERT: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-PR-E DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	// indexed_with_model column exists and is queryable. Pre-existing rows
	// must stay NULL — UI relies on this to render the neutral "Unknown"
	// badge instead of the destructive drift highlight.
	var model sql.NullString
	if err := database.QueryRow(
		`SELECT indexed_with_model FROM projects WHERE host_path = ?`, "/legacy/proj",
	).Scan(&model); err != nil {
		t.Fatalf("select indexed_with_model: %v", err)
	}
	if model.Valid {
		t.Errorf("legacy row indexed_with_model = %q, want NULL", model.String)
	}

	// runtime_settings table exists with the single-row CHECK in place.
	if _, err := database.Exec(
		`INSERT INTO runtime_settings (id, embedding_model, updated_at) VALUES (1, 'foo', '2026-01-01')`,
	); err != nil {
		t.Fatalf("runtime_settings insert: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO runtime_settings (id, embedding_model, updated_at) VALUES (2, 'bar', '2026-01-01')`,
	); err == nil {
		t.Error("expected CHECK(id=1) violation on second row, got nil")
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

// TestMigrate_SplitWorkspaceRepos verifies the conversion from the
// legacy workspace_repos table into the new git_repos + workspace_projects
// shape. Three legacy rows are seeded covering all three flavours that
// existed before the split: an owned external repo, a linked external
// repo (is_linked=1), and a local-path repo (host_path doesn't match
// github.com/owner/repo@branch). After Open():
//
//   - workspace_repos is gone.
//   - git_repos has exactly one row — for the owned external; linked +
//     local rows must not seed git_repos.
//   - workspace_projects has three rows — every legacy membership is
//     preserved, regardless of flavour.
//   - The on-disk clone directory for the owned row is renamed from
//     {workspace_repos.id} to {path_hash}; the linked + local IDs have
//     no on-disk artifacts to begin with so the migration leaves the
//     filesystem alone for them.
func TestMigrate_SplitWorkspaceRepos(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "repos", "owned-id"), 0o755); err != nil {
		t.Fatalf("mkdir owned clone: %v", err)
	}

	raw, err := sql.Open(DriverName, "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}

	// Lay down the post-PR13 / pre-split shape (matches what an upgraded
	// prod DB looks like just before this migration ran for the first time).
	legacy := `
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE workspace_repos (
			id              TEXT PRIMARY KEY,
			workspace_id    TEXT NOT NULL,
			github_url      TEXT NOT NULL,
			branch          TEXT NOT NULL,
			project_path    TEXT NOT NULL,
			token_id        TEXT,
			webhook_secret  TEXT NOT NULL,
			webhook_id      INTEGER,
			auto_webhook    INTEGER NOT NULL DEFAULT 0,
			webhook_mode    TEXT NOT NULL DEFAULT 'manual',
			status          TEXT NOT NULL DEFAULT 'pending',
			last_sha        TEXT,
			last_error      TEXT,
			last_indexed_at TEXT,
			is_linked       INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			UNIQUE(workspace_id, github_url, branch),
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		);
		INSERT INTO workspaces (id, name, created_at, updated_at) VALUES
			('ws-a', 'alpha', '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z'),
			('ws-b', 'beta',  '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z');
		INSERT INTO workspace_repos
			(id, workspace_id, github_url, branch, project_path,
			 webhook_secret, status, is_linked, webhook_mode,
			 created_at, updated_at)
		VALUES
			('owned-id', 'ws-a', 'https://github.com/x/y', 'main',
				'github.com/x/y@main', 's-owned', 'indexed', 0, 'manual',
				'2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z'),
			('linked-id', 'ws-b', 'https://github.com/x/y', 'main',
				'github.com/x/y@main', 's-linked', 'indexed', 1, 'disabled',
				'2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z'),
			('local-id', 'ws-a', '/Users/x/local-proj', '',
				'/Users/x/local-proj', 's-local', 'indexed', 1, 'disabled',
				'2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z');
	`
	if _, err := raw.Exec(legacy); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy: %v", err)
	}
	_ = raw.Close()

	migrated, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	defer migrated.Close()

	// workspace_repos is gone.
	var n int
	if err := migrated.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_repos'`,
	).Scan(&n); err != nil {
		t.Fatalf("count workspace_repos: %v", err)
	}
	if n != 0 {
		t.Fatalf("workspace_repos should be dropped after migration, count=%d", n)
	}

	// git_repos has exactly the one owned-external row.
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM git_repos`).Scan(&n); err != nil {
		t.Fatalf("count git_repos: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 git_repos row, got %d", n)
	}
	var gh, branch, secret string
	if err := migrated.QueryRow(
		`SELECT github_url, branch, webhook_secret FROM git_repos WHERE project_path = ?`,
		"github.com/x/y@main",
	).Scan(&gh, &branch, &secret); err != nil {
		t.Fatalf("read git_repos: %v", err)
	}
	if gh != "https://github.com/x/y" || branch != "main" || secret != "s-owned" {
		t.Fatalf("git_repos row mismatch: gh=%q branch=%q secret=%q", gh, branch, secret)
	}

	// workspace_projects holds three rows — one per legacy workspace_repos.
	if err := migrated.QueryRow(`SELECT COUNT(*) FROM workspace_projects`).Scan(&n); err != nil {
		t.Fatalf("count workspace_projects: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 workspace_projects rows, got %d", n)
	}

	// On-disk clone for the owned row was renamed from {old id} → {path_hash}.
	expectedPath := filepath.Join(dataDir, "repos", HashHostPath("github.com/x/y@main"))
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("clone dir was not renamed to %s: %v", expectedPath, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "repos", "owned-id")); err == nil {
		t.Fatalf("legacy clone dir still exists after rename")
	}

	// Re-running Open() must be a no-op — workspace_repos is already
	// gone so the migration short-circuits at tableExists().
	migrated.Close()
	again, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
	if err != nil {
		t.Fatalf("second OpenWith: %v", err)
	}
	defer again.Close()
}
