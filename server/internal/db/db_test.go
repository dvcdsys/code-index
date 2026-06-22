package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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

// TestMigrate_IndexedWithModelProviderPrefix covers the backfill that
// the pluggable-provider PR adds: legacy rows whose indexed_with_model
// is a bare model name ("awhiteside/CodeRankEmbed-Q8_0-GGUF") must
// be rewritten to the prefixed form ("ollama:awhiteside/...") so the
// drift-detector and dashboard see a match with the live Provider.ID().
// Rows that already start with a known kind prefix (ollama:/openai:/
// voyage:) must be left untouched — important for idempotency and for
// DBs partially upgraded before this migration shipped. A legacy
// Ollama-style name that merely contains a colon (e.g.
// "nomic-embed-text:latest") is NOT yet prefixed and MUST still get the
// "ollama:" prefix.
func TestMigrate_IndexedWithModelProviderPrefix(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "indexed-prefix.db")

	// Stage a minimal projects table at the migration-12 layout (i.e.
	// indexed_with_model column already exists) so we exercise just
	// the prefix backfill without crossing other migrations' concerns.
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
		path_hash TEXT,
		indexed_with_model TEXT
	)`); err != nil {
		t.Fatalf("seed CREATE TABLE: %v", err)
	}
	rows := []struct {
		host, model string
	}{
		{"/legacy/bare", "awhiteside/CodeRankEmbed-Q8_0-GGUF"},             // should get "ollama:" prefix
		{"/legacy/colon", "nomic-embed-text:latest"},                       // colon, but no kind prefix → should get "ollama:"
		{"/already/prefixed", "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF"}, // untouched
		{"/already/voyage", "voyage:voyage-code-3:1024:float"},             // untouched
	}
	for _, r := range rows {
		if _, err := seed.Exec(
			`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash, indexed_with_model)
			 VALUES (?, ?, '2024-01-01', '2024-01-01', ?, ?)`,
			r.host, r.host, r.host, r.model,
		); err != nil {
			t.Fatalf("seed INSERT %s: %v", r.host, err)
		}
	}
	// Row with NULL model should also be left alone (legacy pre-PR-E projects).
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		 VALUES ('/legacy/null', '/legacy/null', '2024-01-01', '2024-01-01', 'null')`,
	); err != nil {
		t.Fatalf("seed INSERT null: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	expectations := map[string]sql.NullString{
		"/legacy/bare":      {String: "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF", Valid: true},
		"/legacy/colon":     {String: "ollama:nomic-embed-text:latest", Valid: true},
		"/already/prefixed": {String: "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF", Valid: true},
		"/already/voyage":   {String: "voyage:voyage-code-3:1024:float", Valid: true},
		"/legacy/null":      {Valid: false},
	}
	for host, want := range expectations {
		var got sql.NullString
		if err := database.QueryRow(
			`SELECT indexed_with_model FROM projects WHERE host_path = ?`, host,
		).Scan(&got); err != nil {
			t.Fatalf("select %s: %v", host, err)
		}
		if got.Valid != want.Valid || got.String != want.String {
			t.Errorf("%s: indexed_with_model = %v (valid=%v), want %v (valid=%v)",
				host, got.String, got.Valid, want.String, want.Valid)
		}
	}
}

// TestOpenMigratesPreM9DB simulates a pre-m9 database (git_repos without the
// polling columns — i.e. pre git_repos_polling migration) and verifies Open
// adds them + the scheduler index without crashing, and that an existing row
// reads back with the polling defaults.
func TestOpenMigratesPreM9DB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m9.db")

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
		path_hash TEXT
	)`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	// pre-m9 git_repos: has indexed_sha (post-m7) but no polling columns.
	if _, err := seed.Exec(`CREATE TABLE git_repos (
		project_path   TEXT PRIMARY KEY,
		github_url     TEXT NOT NULL,
		branch         TEXT NOT NULL,
		token_id       TEXT,
		webhook_secret TEXT NOT NULL,
		webhook_id     INTEGER,
		webhook_mode   TEXT NOT NULL DEFAULT 'manual',
		auto_webhook   INTEGER NOT NULL DEFAULT 0,
		last_sha       TEXT,
		indexed_sha    TEXT,
		last_error     TEXT,
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("seed git_repos: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		 VALUES ('github.com/x/y@main', 'github.com/x/y@main', '2024-01-01', '2024-01-01', 'deadbeefdeadbeef')`,
	); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES ('github.com/x/y@main', 'https://github.com/x/y', 'main', 'sekret', '2024-01-01', '2024-01-01')`,
	); err != nil {
		t.Fatalf("seed git_repos row: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m9 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	// Columns must be queryable and default correctly on the existing row.
	var (
		pollEnabled int
		pollSecs    sql.NullInt64
		nextPollAt  sql.NullString
	)
	if err := database.QueryRow(
		`SELECT polling_enabled, poll_interval_seconds, next_poll_at
		   FROM git_repos WHERE project_path = ?`, "github.com/x/y@main",
	).Scan(&pollEnabled, &pollSecs, &nextPollAt); err != nil {
		t.Fatalf("select polling columns: %v", err)
	}
	if pollEnabled != 0 || pollSecs.Valid || nextPollAt.Valid {
		t.Errorf("defaults wrong: enabled=%d secs=%v next=%v", pollEnabled, pollSecs, nextPollAt)
	}

	var idxCount int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_git_repos_due'`,
	).Scan(&idxCount); err != nil {
		t.Fatalf("idx count: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("idx_git_repos_due count = %d, want 1", idxCount)
	}

	// Second Open is a no-op (idempotent) — must not error.
	database.Close()
	again, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	again.Close()
}

// TestOpenMigratesPreAuthModelDB simulates a pre-m10 database (no owner_user_id
// columns, users with the old 'viewer' role, one local + one external project,
// one workspace) and verifies the auth-model backfill: every user becomes
// admin, local projects + workspaces are assigned to the first active admin,
// and external projects (git_repos peer) stay ownerless.
func TestOpenMigratesPreAuthModelDB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m10.db")
	seed, err := sql.Open(DriverName, "file:"+tmp)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	// Pre-m10 shapes: no owner_user_id anywhere; role defaults to 'viewer'.
	mustExec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, email TEXT NOT NULL, password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer', must_change_password INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, disabled_at TEXT)`)
	mustExec(`CREATE TABLE projects (
		host_path TEXT PRIMARY KEY, container_path TEXT NOT NULL,
		languages TEXT DEFAULT '[]', settings TEXT DEFAULT '{}', stats TEXT DEFAULT '{}',
		status TEXT DEFAULT 'created', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		last_indexed_at TEXT, path_hash TEXT)`)
	mustExec(`CREATE TABLE workspaces (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	mustExec(`CREATE TABLE git_repos (
		project_path TEXT PRIMARY KEY, github_url TEXT NOT NULL, branch TEXT NOT NULL,
		token_id TEXT, webhook_secret TEXT NOT NULL, webhook_id INTEGER,
		webhook_mode TEXT NOT NULL DEFAULT 'manual', auto_webhook INTEGER NOT NULL DEFAULT 0,
		last_sha TEXT, indexed_sha TEXT, last_error TEXT,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)

	// alice is older than bob → alice is the first active admin after backfill.
	mustExec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at)
		VALUES ('u_alice', 'alice@x.com', 'h', 'viewer', '2024-01-01', '2024-01-01')`)
	mustExec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at)
		VALUES ('u_bob', 'bob@x.com', 'h', 'viewer', '2024-06-01', '2024-06-01')`)
	mustExec(`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		VALUES ('/local/proj', '/local/proj', '2024-02-01', '2024-02-01', 'aaaa')`)
	mustExec(`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		VALUES ('github.com/x/y@main', 'github.com/x/y@main', '2024-02-01', '2024-02-01', 'bbbb')`)
	mustExec(`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		VALUES ('github.com/x/y@main', 'https://github.com/x/y', 'main', 'sekret', '2024-02-01', '2024-02-01')`)
	mustExec(`INSERT INTO workspaces (id, name, created_at, updated_at)
		VALUES ('ws_1', 'team', '2024-03-01', '2024-03-01')`)
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m10 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	// 1. every user is now admin.
	var nonAdmins int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role <> 'admin'`).Scan(&nonAdmins); err != nil {
		t.Fatalf("count non-admins: %v", err)
	}
	if nonAdmins != 0 {
		t.Errorf("non-admin users after migration = %d, want 0", nonAdmins)
	}

	// 2. local project owned by the first active admin (alice).
	var localOwner sql.NullString
	if err := database.QueryRow(
		`SELECT owner_user_id FROM projects WHERE host_path = '/local/proj'`,
	).Scan(&localOwner); err != nil {
		t.Fatalf("select local owner: %v", err)
	}
	if !localOwner.Valid || localOwner.String != "u_alice" {
		t.Errorf("local project owner = %v, want u_alice", localOwner)
	}

	// 3. external project stays ownerless.
	var extOwner sql.NullString
	if err := database.QueryRow(
		`SELECT owner_user_id FROM projects WHERE host_path = 'github.com/x/y@main'`,
	).Scan(&extOwner); err != nil {
		t.Fatalf("select external owner: %v", err)
	}
	if extOwner.Valid {
		t.Errorf("external project owner = %q, want NULL", extOwner.String)
	}

	// 4. workspace owned by the first active admin.
	var wsOwner sql.NullString
	if err := database.QueryRow(
		`SELECT owner_user_id FROM workspaces WHERE id = 'ws_1'`,
	).Scan(&wsOwner); err != nil {
		t.Fatalf("select workspace owner: %v", err)
	}
	if !wsOwner.Valid || wsOwner.String != "u_alice" {
		t.Errorf("workspace owner = %v, want u_alice", wsOwner)
	}

	// 5. new tables exist.
	for _, tbl := range []string{"view_groups", "view_group_members", "project_group_shares", "workspace_group_shares"} {
		var n int
		if err := database.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migration", tbl)
		}
	}

	// 6. second Open is idempotent.
	database.Close()
	again, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	again.Close()
}

// TestOpenMigratesLocalProjectDisabled seeds a legacy users table without the
// local_project_disabled column and confirms migration #14 adds it and
// backfills existing rows to 0 (allowed) — the backward-compat guarantee.
func TestOpenMigratesLocalProjectDisabled(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m14.db")
	seed, err := sql.Open(DriverName, "file:"+tmp)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	// Legacy users table: no local_project_disabled column.
	if _, err := seed.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, email TEXT NOT NULL, password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user', must_change_password INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, disabled_at TEXT)`); err != nil {
		t.Fatalf("seed CREATE TABLE users: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at)
		VALUES ('u_legacy', 'legacy@x.com', 'h', 'user', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed INSERT user: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m14 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	// Existing row backfilled to allowed (0).
	var disabled int
	if err := database.QueryRow(
		`SELECT local_project_disabled FROM users WHERE id = 'u_legacy'`,
	).Scan(&disabled); err != nil {
		t.Fatalf("select local_project_disabled: %v", err)
	}
	if disabled != 0 {
		t.Errorf("legacy user local_project_disabled = %d, want 0 (allowed)", disabled)
	}

	// New INSERT that omits the column also defaults to 0.
	if _, err := database.Exec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at)
		VALUES ('u_new', 'new@x.com', 'h', 'user', '2024-07-01', '2024-07-01')`); err != nil {
		t.Fatalf("insert new user: %v", err)
	}
	if err := database.QueryRow(
		`SELECT local_project_disabled FROM users WHERE id = 'u_new'`,
	).Scan(&disabled); err != nil {
		t.Fatalf("select new local_project_disabled: %v", err)
	}
	if disabled != 0 {
		t.Errorf("new user local_project_disabled = %d, want 0 (default)", disabled)
	}

	// Second Open is idempotent.
	database.Close()
	again, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	again.Close()
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

// TestMigrate_SplitWorkspaceRepos_PartialRename covers Fix #3 of the
// branch review: when a clone-dir rename hard-fails (permissions, EROFS,
// …) the migration must NOT drop workspace_repos. The old code dropped
// the table first and then attempted renames; a kill -9 in the gap
// stranded clone dirs and silently re-cloned on next start. The new
// code does renames before the SQL transaction and returns an error
// on any real failure, so the next process start retries end-to-end.
//
// On Unix we drop write on the repos parent dir to force EACCES from
// rename(2). Skipped on Windows where the permission model differs and
// we can't reliably trigger the same failure mode portably.
func TestMigrate_SplitWorkspaceRepos_PartialRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based rename failure not portable to Windows")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	dataDir := filepath.Join(dir, "data")
	reposDir := filepath.Join(dataDir, "repos")
	if err := os.MkdirAll(filepath.Join(reposDir, "owned-id"), 0o755); err != nil {
		t.Fatalf("mkdir owned clone: %v", err)
	}

	raw, err := sql.Open(DriverName, "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
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
			('ws-a', 'alpha', '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z');
		INSERT INTO workspace_repos
			(id, workspace_id, github_url, branch, project_path,
			 webhook_secret, status, is_linked, webhook_mode,
			 created_at, updated_at)
		VALUES
			('owned-id', 'ws-a', 'https://github.com/x/y', 'main',
				'github.com/x/y@main', 's-owned', 'indexed', 0, 'manual',
				'2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z');
	`
	if _, err := raw.Exec(legacy); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy: %v", err)
	}
	_ = raw.Close()

	// Drop write on the parent of source+target. rename(2) needs write
	// on both parents (which here are the same dir) so the syscall
	// will return EACCES. Restore in cleanup so t.TempDir can run.
	if err := os.Chmod(reposDir, 0o500); err != nil {
		t.Fatalf("chmod %s to read-only: %v", reposDir, err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(reposDir, 0o755)
	})

	// OpenWith must surface the rename failure and refuse to drop
	// workspace_repos.
	_, err = OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
	if err == nil {
		t.Fatalf("expected OpenWith to fail when rename is denied, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("expected error to mention rename, got: %v", err)
	}

	// Restore perms so we can inspect the DB and run the retry.
	if err := os.Chmod(reposDir, 0o755); err != nil {
		t.Fatalf("chmod %s restore: %v", reposDir, err)
	}

	raw2, err := sql.Open(DriverName, "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("raw open #2: %v", err)
	}
	var n int
	if err := raw2.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_repos'`,
	).Scan(&n); err != nil {
		_ = raw2.Close()
		t.Fatalf("count workspace_repos: %v", err)
	}
	_ = raw2.Close()
	if n != 1 {
		t.Fatalf("workspace_repos should still exist (migration must be retry-safe), got count=%d", n)
	}

	// Source dir untouched (still under old id, no target created).
	if _, err := os.Stat(filepath.Join(reposDir, "owned-id")); err != nil {
		t.Fatalf("source clone dir should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reposDir, HashHostPath("github.com/x/y@main"))); err == nil {
		t.Fatalf("target clone dir should not have been created on failure")
	}

	// Retry — with perms restored, OpenWith finishes the migration
	// end-to-end. INSERT OR IGNORE on workspace_projects / git_repos
	// and skip-target-exists on rename keep it idempotent.
	db, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
	if err != nil {
		t.Fatalf("OpenWith retry: %v", err)
	}
	defer db.Close()

	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_repos'`,
	).Scan(&n); err != nil {
		t.Fatalf("count workspace_repos post-retry: %v", err)
	}
	if n != 0 {
		t.Fatalf("workspace_repos should be dropped after successful retry, got count=%d", n)
	}
	expected := filepath.Join(reposDir, HashHostPath("github.com/x/y@main"))
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("clone dir was not renamed on retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reposDir, "owned-id")); err == nil {
		t.Fatalf("legacy clone dir still exists after retry")
	}
}

// TestMigrate_SplitWorkspaceRepos_EdgeCases — Fix #14 coverage. Each
// subtest exercises one corner of the migration's rename loop or DB-side
// insert pass:
//
//   - PreExistingTarget_Skipped: target dir already exists under the new
//     path_hash (prior partial-run did this rename). The migration must
//     skip the rename — neither overwriting the existing target nor
//     erroring — and the DB split must proceed.
//   - MissingSourceDir_Skipped: DB row references a clone that never
//     materialised on disk (legacy clone job died before mkdir). The
//     migration must treat the missing source as a no-op rename and
//     still complete the DB split.
//   - DuplicatePathInLegacy: two legacy rows in the same workspace map
//     to the same (workspace_id, project_path) tuple. INSERT OR IGNORE
//     into workspace_projects must collapse them to a single membership
//     row without erroring on the PK collision.
//
// PartialRename_ReturnsError (the fifth review-doc case) is already
// covered by TestMigrate_SplitWorkspaceRepos_PartialRename above; not
// duplicated here.
//
// TokenDeletedConcurrently (review-doc case 5) is intentionally NOT
// implemented — simulating a token row vanishing mid-migration requires
// either a mock of the github_tokens FK or a goroutine deleting from a
// parallel connection, both of which are out of scope for a DB-only
// migration test. Tracked as a TODO; reintroduce when there's a
// repojobs integration suite that can drive token lifecycle
// alongside the migration.
func TestMigrate_SplitWorkspaceRepos_EdgeCases(t *testing.T) {
	t.Run("PreExistingTarget_Skipped", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "test.db")
		dataDir := filepath.Join(dir, "data")
		reposDir := filepath.Join(dataDir, "repos")

		projectPath := "github.com/x/y@main"
		targetHash := HashHostPath(projectPath)

		// Legacy source dir with a sentinel so we can tell whether it
		// survived (skip path) or got renamed away (would be a regression).
		if err := os.MkdirAll(filepath.Join(reposDir, "owned-id"), 0o755); err != nil {
			t.Fatalf("mkdir source: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(reposDir, "owned-id", "src.sentinel"), []byte("src"), 0o644,
		); err != nil {
			t.Fatalf("write source sentinel: %v", err)
		}

		// Pre-existing TARGET dir under the new path_hash with its own
		// sentinel — simulates a partial-run that already finished this
		// rename. The migration must preserve this content untouched.
		if err := os.MkdirAll(filepath.Join(reposDir, targetHash), 0o755); err != nil {
			t.Fatalf("mkdir pre-existing target: %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(reposDir, targetHash, "target.sentinel"), []byte("preserved"), 0o644,
		); err != nil {
			t.Fatalf("write target sentinel: %v", err)
		}

		seedSplitLegacyDB(t, dbPath, false, []legacyRow{{
			id: "owned-id", workspaceID: "ws-a",
			githubURL: "https://github.com/x/y", branch: "main",
			projectPath: projectPath,
			isLinked:    0,
		}})

		d, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
		if err != nil {
			t.Fatalf("OpenWith: %v", err)
		}
		defer d.Close()

		// Target survived with its original content (rename skipped).
		got, err := os.ReadFile(filepath.Join(reposDir, targetHash, "target.sentinel"))
		if err != nil {
			t.Errorf("target dir / sentinel missing after migration: %v", err)
		} else if string(got) != "preserved" {
			t.Errorf("target sentinel overwritten: got %q, want %q", got, "preserved")
		}
		// Source dir still there (the rename was a no-op, didn't move it).
		if _, err := os.Stat(filepath.Join(reposDir, "owned-id")); err != nil {
			t.Errorf("source dir was unexpectedly removed: %v", err)
		}
		// DB split happened — workspace_repos dropped, git_repos populated.
		assertWorkspaceReposDropped(t, d)
		assertGitReposCount(t, d, 1)
		assertWorkspaceProjectsCount(t, d, 1)
	})

	t.Run("MissingSourceDir_Skipped", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "test.db")
		dataDir := filepath.Join(dir, "data")
		reposDir := filepath.Join(dataDir, "repos")
		// reposDir exists but no per-id subdir — simulates a legacy clone
		// job that died before mkdir, leaving an orphan workspace_repos
		// row pointing at a directory that never materialised.
		if err := os.MkdirAll(reposDir, 0o755); err != nil {
			t.Fatalf("mkdir reposDir: %v", err)
		}

		projectPath := "github.com/x/y@main"
		seedSplitLegacyDB(t, dbPath, false, []legacyRow{{
			id: "ghost-id", workspaceID: "ws-a",
			githubURL: "https://github.com/x/y", branch: "main",
			projectPath: projectPath,
			isLinked:    0,
		}})

		d, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
		if err != nil {
			t.Fatalf("OpenWith: %v", err)
		}
		defer d.Close()

		// No target dir got created — the rename was skipped because
		// there was no source. A future clone_repo job will mkdir the
		// target when it runs against the new path_hash.
		targetHash := HashHostPath(projectPath)
		if _, err := os.Stat(filepath.Join(reposDir, targetHash)); err == nil {
			t.Errorf("target dir created from nothing: %s", filepath.Join(reposDir, targetHash))
		}
		// DB split still happened — the absent on-disk clone doesn't
		// block the SQL inserts.
		assertWorkspaceReposDropped(t, d)
		assertGitReposCount(t, d, 1)
		assertWorkspaceProjectsCount(t, d, 1)
	})

	t.Run("DuplicatePathInLegacy", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "test.db")
		dataDir := filepath.Join(dir, "data")

		// Two legacy rows in ws-a that map to the same project_path —
		// possible on a pre-PR12/13 schema (no UNIQUE on workspace_id +
		// github_url + branch) or on a hand-edited DB. The owned row
		// uses the bare URL, the linked row uses the ".git" suffix
		// variant; both deterministically produce the same project_path
		// `github.com/x/y@main` and so collide on the workspace_projects
		// PK (workspace_id, project_path).
		seedSplitLegacyDB(t, dbPath, true /* skipUnique */, []legacyRow{
			{
				id: "row-owned", workspaceID: "ws-a",
				githubURL: "https://github.com/x/y", branch: "main",
				projectPath: "github.com/x/y@main",
				isLinked:    0,
			},
			{
				id: "row-linked", workspaceID: "ws-a",
				githubURL: "https://github.com/x/y.git", branch: "main",
				projectPath: "github.com/x/y@main",
				isLinked:    1,
			},
		})

		d, err := OpenWith(OpenOptions{Path: dbPath, DataDir: dataDir})
		if err != nil {
			t.Fatalf("OpenWith: %v", err)
		}
		defer d.Close()

		// Exactly one workspace_projects row survived the INSERT OR
		// IGNORE pass; the second legacy row was suppressed by the
		// (workspace_id, project_path) PK collision.
		var n int
		if err := d.QueryRow(
			`SELECT COUNT(*) FROM workspace_projects WHERE workspace_id = ? AND project_path = ?`,
			"ws-a", "github.com/x/y@main",
		).Scan(&n); err != nil {
			t.Fatalf("count membership: %v", err)
		}
		if n != 1 {
			t.Errorf("workspace_projects membership count for duplicate path: got %d, want 1", n)
		}
		// git_repos also collapses — only the owned row contributes (linked
		// rows skip the git_repos insert), and the project_path PK guards
		// against double-insert anyway.
		assertGitReposCount(t, d, 1)
		assertWorkspaceReposDropped(t, d)
	})
}

// legacyRow models the minimal subset of pre-split workspace_repos fields
// the migration cares about. Defaults that don't matter to the migration
// (webhook_mode, status, timestamps) are filled in by seedSplitLegacyDB.
type legacyRow struct {
	id          string
	workspaceID string
	githubURL   string
	branch      string
	projectPath string
	isLinked    int
}

// seedSplitLegacyDB lays down the pre-split schema (workspaces +
// workspace_repos in the post-PR13 shape) and inserts the given rows.
// When skipUnique is true the composite UNIQUE on (workspace_id,
// github_url, branch) is omitted so duplicate-path scenarios can be
// constructed; otherwise the schema mirrors what
// migrateWorkspaceReposLinked leaves behind.
func seedSplitLegacyDB(t *testing.T, dbPath string, skipUnique bool, rows []legacyRow) {
	t.Helper()
	raw, err := sql.Open(DriverName, "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer raw.Close()

	uniqueClause := "UNIQUE(workspace_id, github_url, branch),"
	if skipUnique {
		uniqueClause = ""
	}
	schema := `
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
			` + uniqueClause + `
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		);
		INSERT INTO workspaces (id, name, created_at, updated_at) VALUES
			('ws-a', 'alpha', '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z');
	`
	if _, err := raw.Exec(schema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	for _, r := range rows {
		if _, err := raw.Exec(`
			INSERT INTO workspace_repos (id, workspace_id, github_url, branch, project_path,
				webhook_secret, status, is_linked, webhook_mode, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'indexed', ?, 'manual',
				'2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z')`,
			r.id, r.workspaceID, r.githubURL, r.branch, r.projectPath,
			"secret-"+r.id, r.isLinked,
		); err != nil {
			t.Fatalf("seed legacy row %s: %v", r.id, err)
		}
	}
}

func assertWorkspaceReposDropped(t *testing.T, d *sql.DB) {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_repos'`,
	).Scan(&n); err != nil {
		t.Fatalf("check workspace_repos drop: %v", err)
	}
	if n != 0 {
		t.Errorf("workspace_repos should be dropped, count=%d", n)
	}
}

func assertGitReposCount(t *testing.T, d *sql.DB, want int) {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM git_repos`).Scan(&n); err != nil {
		t.Fatalf("count git_repos: %v", err)
	}
	if n != want {
		t.Errorf("git_repos count: got %d, want %d", n, want)
	}
}

func assertWorkspaceProjectsCount(t *testing.T, d *sql.DB, want int) {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM workspace_projects`).Scan(&n); err != nil {
		t.Fatalf("count workspace_projects: %v", err)
	}
	if n != want {
		t.Errorf("workspace_projects count: got %d, want %d", n, want)
	}
}

// TestApplyMigrations_FreshDBRecordsAll — fresh Open() must record every
// registered migration in schema_migrations. The acceptance criterion for
// Fix #7: `SELECT version FROM schema_migrations` returns the full ledger
// in order. Pins both the count (regression guard against silent skipping)
// and the name sequence (regression guard against accidental renumber).
func TestApplyMigrations_FreshDBRecordsAll(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	rows, err := d.Query(
		`SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()

	type entry struct {
		version int
		name    string
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.version, &e.name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(got) != len(registeredMigrations) {
		t.Fatalf("schema_migrations row count = %d, want %d (got=%+v)",
			len(got), len(registeredMigrations), got)
	}
	for i, m := range registeredMigrations {
		if got[i].version != m.version || got[i].name != m.name {
			t.Errorf("row %d = {%d, %q}, want {%d, %q}",
				i, got[i].version, got[i].name, m.version, m.name)
		}
	}
}

// TestApplyMigrations_ReopenIsNoOp — opening the same file-backed DB twice
// must leave schema_migrations untouched: same row count, same applied_at
// strings. If a migration accidentally re-ran on warm boot, applied_at
// would be re-stamped to the second open's time. The acceptance criterion
// from Fix #7: "повторний запуск — no-op".
func TestApplyMigrations_ReopenIsNoOp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reopen.db")

	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first := readMigrationLedger(t, d)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	second := readMigrationLedger(t, again)

	if len(first) != len(second) {
		t.Fatalf("ledger row count changed: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("ledger row %d changed: first=%+v second=%+v",
				i, first[i], second[i])
		}
	}
}

// TestApplyMigrations_BootstrapFromLegacyDB — production DBs created before
// the schema_migrations ledger landed start out with no rows in
// schema_migrations even though their tables are already at the modern
// shape. applyMigrations must bootstrap them by running every registered
// migration (each idempotent, so the SQL is mostly a no-op on already-current
// state) and recording all version rows. Without this, every boot would
// re-run every migration forever.
//
// We simulate the pre-ledger state by opening the DB via Schema.Exec alone
// (skipping applyMigrations), then re-opening via the normal path.
func TestApplyMigrations_BootstrapFromLegacyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Seed: schema applied, but schema_migrations not populated (mimicking
	// any prod DB that booted before this ledger existed).
	raw, err := sql.Open(DriverName, "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(Schema); err != nil {
		_ = raw.Close()
		t.Fatalf("exec Schema: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE IF EXISTS schema_migrations`); err != nil {
		_ = raw.Close()
		t.Fatalf("drop schema_migrations: %v", err)
	}
	_ = raw.Close()

	// Confirm the seeded DB really has no ledger before we bootstrap.
	seedDB, err := sql.Open(DriverName, "file:"+dbPath)
	if err != nil {
		t.Fatalf("verify seed open: %v", err)
	}
	var seedHasLedger int
	if err := seedDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`,
	).Scan(&seedHasLedger); err != nil {
		_ = seedDB.Close()
		t.Fatalf("verify seed: %v", err)
	}
	_ = seedDB.Close()
	if seedHasLedger != 0 {
		t.Fatalf("seed precondition: schema_migrations should be absent, found %d", seedHasLedger)
	}

	// Now boot through the normal path — applyMigrations should bootstrap.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	ledger := readMigrationLedger(t, d)
	if len(ledger) != len(registeredMigrations) {
		t.Fatalf("post-bootstrap ledger has %d rows, want %d",
			len(ledger), len(registeredMigrations))
	}
	for i, m := range registeredMigrations {
		if ledger[i].version != m.version || ledger[i].name != m.name {
			t.Errorf("ledger row %d = {%d, %q}, want {%d, %q}",
				i, ledger[i].version, ledger[i].name, m.version, m.name)
		}
	}
}

// migrationLedgerRow mirrors a schema_migrations row for test assertions.
type migrationLedgerRow struct {
	version   int
	name      string
	appliedAt string
}

// readMigrationLedger snapshots the schema_migrations table in version order.
func readMigrationLedger(t *testing.T, d *sql.DB) []migrationLedgerRow {
	t.Helper()
	rows, err := d.Query(
		`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()
	var out []migrationLedgerRow
	for rows.Next() {
		var r migrationLedgerRow
		if err := rows.Scan(&r.version, &r.name, &r.appliedAt); err != nil {
			t.Fatalf("scan ledger row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ledger rows.Err: %v", err)
	}
	return out
}

// TestOpenMigratesPreM11DB verifies the machine-identity migration adds
// display_path/machine_id/machine_label and backfills display_path = host_path
// for pre-existing rows.
func TestOpenMigratesPreM11DB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m11.db")
	seed, err := sql.Open(DriverName, "file:"+tmp)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE projects (
		host_path TEXT PRIMARY KEY, container_path TEXT NOT NULL,
		languages TEXT DEFAULT '[]', settings TEXT DEFAULT '{}', stats TEXT DEFAULT '{}',
		status TEXT DEFAULT 'created', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		last_indexed_at TEXT, path_hash TEXT)`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		 VALUES ('/Users/dev/legacy', '/Users/dev/legacy', '2024-01-01', '2024-01-01', 'cafef00dcafef00d')`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m11 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	var displayPath sql.NullString
	var machineID sql.NullString
	if err := database.QueryRow(
		`SELECT display_path, machine_id FROM projects WHERE host_path = '/Users/dev/legacy'`,
	).Scan(&displayPath, &machineID); err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if !displayPath.Valid || displayPath.String != "/Users/dev/legacy" {
		t.Errorf("display_path = %v, want /Users/dev/legacy", displayPath)
	}
	if machineID.Valid {
		t.Errorf("legacy machine_id = %q, want NULL", machineID.String)
	}
}

// TestOpenMigratesPreM18DB verifies migration 18 adds full_sync_required /
// full_sync_reason and backfills every existing project to require a full
// resync (the chunker-format-change trigger). A fresh DB has no rows, so the
// backfill is a no-op there — covered separately by the default-0 column.
func TestOpenMigratesPreM18DB(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "pre-m18.db")
	seed, err := sql.Open(DriverName, "file:"+tmp)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE projects (
		host_path TEXT PRIMARY KEY, container_path TEXT NOT NULL,
		languages TEXT DEFAULT '[]', settings TEXT DEFAULT '{}', stats TEXT DEFAULT '{}',
		status TEXT DEFAULT 'created', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		last_indexed_at TEXT, path_hash TEXT)`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := seed.Exec(
		`INSERT INTO projects (host_path, container_path, created_at, updated_at, path_hash)
		 VALUES ('/Users/dev/legacy', '/Users/dev/legacy', '2024-01-01', '2024-01-01', 'cafef00dcafef00d')`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	seed.Close()

	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open migrates pre-m18 DB: %v", err)
	}
	defer database.Close()
	defer os.Remove(tmp)

	var fullSyncRequired int
	var reason sql.NullString
	if err := database.QueryRow(
		`SELECT full_sync_required, full_sync_reason FROM projects WHERE host_path = '/Users/dev/legacy'`,
	).Scan(&fullSyncRequired, &reason); err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if fullSyncRequired != 1 {
		t.Errorf("full_sync_required = %d, want 1 (existing project flagged for resync)", fullSyncRequired)
	}
	if !reason.Valid || reason.String == "" {
		t.Errorf("full_sync_reason = %v, want non-empty explanation", reason)
	}
}
