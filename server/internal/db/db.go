// Package db opens the SQLite database used by the Go server. Pure-Go driver
// via modernc.org/sqlite (CGO-free). Parity with api/app/database.py on DDL
// and PRAGMAs (WAL + foreign_keys ON).
package db

import (
	"crypto/sha1"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// migrationFn applies a single schema migration. The opts parameter carries
// OpenOptions through to migrations that touch on-disk artefacts (the split
// migration renames clone dirs under opts.DataDir); migrations that only
// touch SQL ignore it.
type migrationFn func(*sql.DB, OpenOptions) error

// migration is one row in the registeredMigrations slice. version is the
// permanent identifier recorded in schema_migrations; name is a human-readable
// label surfaced in error messages and ops logs.
type migration struct {
	version int
	name    string
	fn      migrationFn
}

// registeredMigrations is the canonical migration ledger, in apply order.
// schema_migrations records (version, name) after each successful run; on
// subsequent boots applyMigrations skips every entry whose version is
// <= MAX(schema_migrations.version), so each migration runs at most once
// per database.
//
// Rules for editing this list:
//
//  1. Append new migrations with the next sequential version number. Never
//     renumber, never remove — production schema_migrations rows reference
//     these version/name tuples and a collision would silently skip work
//     that was supposed to run.
//  2. Keep each migration idempotent. Bootstrap (DB exists but
//     schema_migrations is empty) runs all of them from scratch, so each
//     must detect already-applied state via PRAGMA / sqlite_master /
//     IF NOT EXISTS and short-circuit.
//  3. Migrations run outside any wrapping transaction; some take their own
//     internal tx (the split migration does). If a migration fails part-way,
//     its schema_migrations row is NOT inserted, so the next boot retries
//     end-to-end — which is why idempotency is non-negotiable.
var registeredMigrations = []migration{
	{1, "path_hash", func(db *sql.DB, _ OpenOptions) error { return migratePathHash(db) }},
	{2, "indexed_with_model", func(db *sql.DB, _ OpenOptions) error { return migrateIndexedWithModel(db) }},
	{3, "webhook_mode", func(db *sql.DB, _ OpenOptions) error { return migrateWebhookMode(db) }},
	{4, "workspace_repos_linked", func(db *sql.DB, _ OpenOptions) error { return migrateWorkspaceReposLinked(db) }},
	{5, "split_workspace_repos", func(db *sql.DB, opts OpenOptions) error { return migrateSplitWorkspaceRepos(db, opts.DataDir) }},
	{6, "drop_communities", func(db *sql.DB, _ OpenOptions) error { return migrateDropCommunities(db) }},
	{7, "git_repos_indexed_sha", func(db *sql.DB, _ OpenOptions) error { return migrateGitReposIndexedSHA(db) }},
	{8, "tunnel_config", func(db *sql.DB, _ OpenOptions) error { return migrateTunnelConfig(db) }},
	{9, "git_repos_polling", func(db *sql.DB, _ OpenOptions) error { return migrateGitReposPolling(db) }},
	{10, "auth_groups_ownership", func(db *sql.DB, _ OpenOptions) error { return migrateAuthGroupsOwnership(db) }},
	{11, "project_machine_identity", func(db *sql.DB, _ OpenOptions) error { return migrateProjectMachineIdentity(db) }},
	{12, "embedding_provider", func(db *sql.DB, _ OpenOptions) error { return migrateEmbeddingProvider(db) }},
	{13, "indexed_with_model_provider_prefix", func(db *sql.DB, _ OpenOptions) error { return migrateIndexedWithModelProviderPrefix(db) }},
	{14, "user_local_project_disabled", func(db *sql.DB, _ OpenOptions) error { return migrateUserLocalProjectDisabled(db) }},
}

// DriverName is the registered database/sql driver name for modernc.org/sqlite.
const DriverName = "sqlite"

// OpenOptions configures Open. DataDir is only consulted by migrations
// that need to rename on-disk artefacts (e.g. the split of workspace_repos
// into git_repos renamed clone directories from {workspace_repos.id} to
// {path_hash}). Empty DataDir means "skip filesystem-touching migrations
// in this call" — tests use this.
type OpenOptions struct {
	Path    string
	DataDir string
}

// Open is the conventional entry point used everywhere except main.go.
// It defers to OpenWith with an empty DataDir, so any migration that
// wants to rename on-disk files becomes a no-op for tests + in-memory DBs.
func Open(path string) (*sql.DB, error) {
	return OpenWith(OpenOptions{Path: path})
}

// OpenWith opens (and creates if necessary) the SQLite database at
// opts.Path, sets the required PRAGMAs via the DSN, and runs the
// schema migrations.
func OpenWith(opts OpenOptions) (*sql.DB, error) {
	path := opts.Path
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

	if err := applyMigrations(db, opts); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// HasTables reports whether the SQLite database at path contains ALL of
// the named tables. It opens the file read-write (so any pending WAL is
// recovered cleanly) with a busy timeout, runs NO migrations, and closes
// before returning. A missing file is not an error — it returns
// (false, nil). Used by the boot-time DB adoption migration
// (internal/storage) to tell a real unified system DB (has both
// schema_migrations and users) apart from a pre-auth fossil that merely
// happens to occupy the target path.
func HasTables(path string, names ...string) (bool, error) {
	if path == "" || len(names) == 0 {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	dsn, err := buildDSN(path)
	if err != nil {
		return false, err
	}
	sdb, err := sql.Open(DriverName, dsn)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer sdb.Close()
	sdb.SetMaxOpenConns(1)
	for _, name := range names {
		var got string
		err := sdb.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, name,
		).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check table %q in %s: %w", name, path, err)
		}
	}
	return true, nil
}

// applyMigrations runs every entry in registeredMigrations whose version is
// greater than the current high-water mark in schema_migrations. Each
// successful migration records a (version, name, applied_at) row so the
// same migration never runs twice on the same database.
//
// Bootstrap behaviour: when schema_migrations is empty (fresh DB or any
// production DB that pre-dates this ledger), MAX(version) reads as 0 and
// every registered migration runs. The migrations are individually
// idempotent — they short-circuit on already-current state — so this
// is the same cost as the pre-ledger code path. The benefit kicks in
// from the SECOND boot onwards: applyMigrations sees MAX = N and skips
// every entry <= N, turning warm boots into a single SELECT.
//
// schema_migrations itself is created here, not in Schema, so the bootstrap
// path on a legacy DB (which never ran Schema with the row) still gets a
// ledger. Schema.Exec runs first in OpenWith and uses IF NOT EXISTS, so the
// table is harmlessly recreated by this function on the same boot.
func applyMigrations(db *sql.DB, opts OpenOptions) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INTEGER PRIMARY KEY,
        name       TEXT    NOT NULL,
        applied_at TEXT    NOT NULL
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var currentMax sql.NullInt64
	if err := db.QueryRow(
		`SELECT MAX(version) FROM schema_migrations`,
	).Scan(&currentMax); err != nil {
		return fmt.Errorf("read schema_migrations max version: %w", err)
	}
	threshold := currentMax.Int64

	for _, m := range registeredMigrations {
		if int64(m.version) <= threshold {
			continue
		}
		if err := m.fn(db, opts); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// migrateGitReposIndexedSHA adds git_repos.indexed_sha to pre-incremental
// databases. NULL on existing rows is the explicit signal "never indexed
// with the new pipeline" — the next clone_repo job sees IndexedSHA=""
// and routes through the full-reindex branch, setting indexed_sha on
// success. Idempotent via PRAGMA table_info: skip the ALTER if the
// column is already present.
// migrateTunnelConfig creates the single-row tunnel_config table on
// existing DBs. Idempotent — CREATE TABLE IF NOT EXISTS matches the shape
// in schema.go, so a fresh DB that already has the table is a no-op.
func migrateTunnelConfig(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS tunnel_config (
    id              INTEGER PRIMARY KEY CHECK(id=1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    provider        TEXT    NOT NULL DEFAULT 'cloudflare',
    mode            TEXT    NOT NULL DEFAULT 'quick',
    hostname        TEXT    NOT NULL DEFAULT '',
    encrypted_token BLOB,
    updated_at      TEXT    NOT NULL,
    updated_by      TEXT
)`)
	if err != nil {
		return fmt.Errorf("create tunnel_config: %w", err)
	}
	return nil
}

func migrateGitReposIndexedSHA(db *sql.DB) error {
	exists, err := tableExists(db, "git_repos")
	if err != nil {
		return err
	}
	if !exists {
		// Fresh DB before workspaces feature ever ran — Schema's
		// CREATE TABLE IF NOT EXISTS already laid the new shape down
		// elsewhere in this boot.
		return nil
	}
	rows, err := db.Query(`PRAGMA table_info(git_repos)`)
	if err != nil {
		return fmt.Errorf("table_info git_repos: %w", err)
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
		if name == "indexed_sha" {
			have = true
		}
	}
	rows.Close()
	if have {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE git_repos ADD COLUMN indexed_sha TEXT`); err != nil {
		return fmt.Errorf("add indexed_sha column: %w", err)
	}
	return nil
}

// migrateGitReposPolling adds the polling-sync columns to git_repos and the
// scheduler index. Idempotent: each ALTER is guarded by a PRAGMA table_info
// check, and the index uses IF NOT EXISTS. On a fresh DB the columns already
// exist (Schema's CREATE TABLE laid them down), so this only ALTERs existing
// pre-m9 databases. The index lives here rather than in Schema because pre-m9
// rows lack the columns when Schema.Exec runs (same constraint as path_hash).
func migrateGitReposPolling(db *sql.DB) error {
	exists, err := tableExists(db, "git_repos")
	if err != nil {
		return err
	}
	if !exists {
		// Fresh DB — Schema's CREATE TABLE IF NOT EXISTS already created
		// git_repos with the polling columns this boot.
		return nil
	}

	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(git_repos)`)
	if err != nil {
		return fmt.Errorf("table_info git_repos: %w", err)
	}
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
		have[name] = true
	}
	rows.Close()

	adds := []struct{ col, ddl string }{
		{"polling_enabled", `ALTER TABLE git_repos ADD COLUMN polling_enabled INTEGER NOT NULL DEFAULT 0`},
		{"poll_interval_seconds", `ALTER TABLE git_repos ADD COLUMN poll_interval_seconds INTEGER`},
		{"next_poll_at", `ALTER TABLE git_repos ADD COLUMN next_poll_at TEXT`},
	}
	for _, a := range adds {
		if have[a.col] {
			continue
		}
		if _, err := db.Exec(a.ddl); err != nil {
			return fmt.Errorf("add %s column: %w", a.col, err)
		}
	}

	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_git_repos_due ON git_repos(polling_enabled, next_poll_at)`,
	); err != nil {
		return fmt.Errorf("create idx_git_repos_due: %w", err)
	}
	return nil
}

// columnExists reports whether table has a column named col. Returns false
// (no error) when the table itself does not exist.
func columnExists(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("table_info %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, typ   string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateProjectMachineIdentity adds display_path / machine_id / machine_label
// to projects so local-project identity can be namespaced per machine
// (host_path becomes "local:{machine_id}:{path}" for projects created after
// this change; the CLI computes the matching path_hash). Existing rows keep
// their host_path as-is and get display_path backfilled from it so the
// dashboard has something to show — they remain reachable until re-init.
// Idempotent via columnExists.
func migrateProjectMachineIdentity(db *sql.DB) error {
	adds := []struct{ col, ddl string }{
		{"display_path", `ALTER TABLE projects ADD COLUMN display_path TEXT`},
		{"machine_id", `ALTER TABLE projects ADD COLUMN machine_id TEXT`},
		{"machine_label", `ALTER TABLE projects ADD COLUMN machine_label TEXT`},
	}
	for _, a := range adds {
		have, err := columnExists(db, "projects", a.col)
		if err != nil {
			return err
		}
		if have {
			continue
		}
		if _, err := db.Exec(a.ddl); err != nil {
			return fmt.Errorf("add projects.%s: %w", a.col, err)
		}
	}
	// Backfill display_path = host_path for any row missing it (existing
	// projects predate the column; their host_path is still the real path).
	if _, err := db.Exec(
		`UPDATE projects SET display_path = host_path WHERE display_path IS NULL`,
	); err != nil {
		return fmt.Errorf("backfill display_path: %w", err)
	}
	return nil
}

// migrateAuthGroupsOwnership upgrades pre-auth-model databases to the
// owner + view-group sharing model:
//
//   - adds projects.owner_user_id and workspaces.owner_user_id (NULL on
//     existing rows);
//   - creates view_groups / view_group_members / project_group_shares /
//     workspace_group_shares (matching schema.go; CREATE TABLE IF NOT EXISTS
//     so a fresh DB that already has them is a no-op);
//   - one-time data backfill: every existing user becomes admin (pre-migration
//     everyone had full access, so this preserves it — the operator demotes
//     afterwards); existing LOCAL projects (no git_repos row) and ALL existing
//     workspaces are assigned to the first active admin; existing EXTERNAL
//     projects intentionally stay ownerless (NULL).
//
// Idempotent: column adds are guarded by columnExists, table creates use
// IF NOT EXISTS. The data backfill is naturally a no-op on a fresh DB (no
// users/projects yet — bootstrap runs after Open) and runs exactly once on an
// upgraded DB because schema_migrations records version 10.
func migrateAuthGroupsOwnership(db *sql.DB) error {
	// 1. owner columns (ALTER ADD COLUMN with a NULL-default REFERENCES clause
	// is permitted by SQLite).
	if have, err := columnExists(db, "projects", "owner_user_id"); err != nil {
		return err
	} else if !have {
		if _, err := db.Exec(
			`ALTER TABLE projects ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL`,
		); err != nil {
			return fmt.Errorf("add projects.owner_user_id: %w", err)
		}
	}
	if have, err := columnExists(db, "workspaces", "owner_user_id"); err != nil {
		return err
	} else if !have {
		if _, err := db.Exec(
			`ALTER TABLE workspaces ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL`,
		); err != nil {
			return fmt.Errorf("add workspaces.owner_user_id: %w", err)
		}
	}

	// 2. view-group + sharing tables (mirror schema.go).
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS view_groups (
            id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT,
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS view_group_members (
            group_id TEXT NOT NULL, user_id TEXT NOT NULL, added_at TEXT NOT NULL,
            PRIMARY KEY (group_id, user_id),
            FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE,
            FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS idx_view_group_members_user ON view_group_members(user_id)`,
		`CREATE TABLE IF NOT EXISTS project_group_shares (
            project_path TEXT NOT NULL, group_id TEXT NOT NULL, created_at TEXT NOT NULL,
            PRIMARY KEY (project_path, group_id),
            FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE,
            FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS idx_project_shares_group ON project_group_shares(group_id)`,
		`CREATE TABLE IF NOT EXISTS workspace_group_shares (
            workspace_id TEXT NOT NULL, group_id TEXT NOT NULL, created_at TEXT NOT NULL,
            PRIMARY KEY (workspace_id, group_id),
            FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
            FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS idx_workspace_shares_group ON workspace_group_shares(group_id)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("create auth-model table/index: %w", err)
		}
	}

	// 3. one-time data backfill.
	if _, err := db.Exec(`UPDATE users SET role = 'admin'`); err != nil {
		return fmt.Errorf("promote existing users to admin: %w", err)
	}
	var firstAdmin sql.NullString
	if err := db.QueryRow(
		`SELECT id FROM users WHERE role = 'admin' AND disabled_at IS NULL ORDER BY created_at ASC LIMIT 1`,
	).Scan(&firstAdmin); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("select first active admin: %w", err)
	}
	if firstAdmin.Valid && firstAdmin.String != "" {
		// Local projects (no git_repos peer) → first admin; external stay NULL.
		if _, err := db.Exec(
			`UPDATE projects SET owner_user_id = ?
			 WHERE owner_user_id IS NULL
			   AND host_path NOT IN (SELECT project_path FROM git_repos)`,
			firstAdmin.String,
		); err != nil {
			return fmt.Errorf("backfill local project owners: %w", err)
		}
		if _, err := db.Exec(
			`UPDATE workspaces SET owner_user_id = ? WHERE owner_user_id IS NULL`,
			firstAdmin.String,
		); err != nil {
			return fmt.Errorf("backfill workspace owners: %w", err)
		}
	}
	return nil
}

// migrateDropCommunities removes the PR5–PR12 communities +
// community_members tables. The PR14 fan-out search doesn't need
// them; leaving them around would just confuse anyone reading the
// schema. Idempotent via IF EXISTS, child rows in community_members
// go first to avoid FK-on-DELETE noise.
func migrateDropCommunities(db *sql.DB) error {
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS community_members`,
		`DROP TABLE IF EXISTS communities`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}

// migrateWorkspaceReposLinked brings pre-PR13 workspace_repos tables up to
// the current shape: adds the is_linked column and removes the legacy
// global UNIQUE on project_path so the same indexed project can live in
// multiple workspaces. Two cases:
//
//  1. Table doesn't exist yet (fresh DB) — nothing to migrate; Schema's
//     CREATE TABLE IF NOT EXISTS already laid the new shape down.
//  2. Table has the old shape (project_path declared UNIQUE inline). We
//     read the stored DDL from sqlite_master, and if it still contains
//     "project_path TEXT NOT NULL UNIQUE", do the standard SQLite
//     table-rebuild dance inside a transaction. is_linked is folded into
//     the new table so we avoid a second ALTER pass.
//  3. Table has the new shape but is_linked is still missing (operator
//     applied a partial migration manually) — ALTER TABLE ADD COLUMN.
//
// The check is conservative: any DDL string that doesn't contain the
// legacy UNIQUE marker is treated as already-migrated.
func migrateWorkspaceReposLinked(db *sql.DB) error {
	tableExists, haveIsLinked, err := workspaceReposColumns(db)
	if err != nil {
		return err
	}
	if !tableExists {
		return nil
	}

	needRebuild, err := workspaceReposNeedsUniqueDrop(db)
	if err != nil {
		return err
	}
	if needRebuild {
		return rebuildWorkspaceReposWithoutGlobalUnique(db)
	}
	if !haveIsLinked {
		if _, err := db.Exec(
			`ALTER TABLE workspace_repos ADD COLUMN is_linked INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("add is_linked column: %w", err)
		}
	}
	return nil
}

// workspaceReposColumns reports whether workspace_repos exists and
// whether the is_linked column is already present.
func workspaceReposColumns(db *sql.DB) (tableExists, haveIsLinked bool, err error) {
	rows, qerr := db.Query(`PRAGMA table_info(workspace_repos)`)
	if qerr != nil {
		return false, false, fmt.Errorf("table_info workspace_repos: %w", qerr)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid         int
			name, typ   string
			notnull, pk int
			dflt        sql.NullString
		)
		if scanErr := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); scanErr != nil {
			return false, false, scanErr
		}
		tableExists = true
		if name == "is_linked" {
			haveIsLinked = true
		}
	}
	return tableExists, haveIsLinked, rows.Err()
}

// workspaceReposNeedsUniqueDrop returns true when the stored DDL for
// workspace_repos still has project_path declared as inline-UNIQUE.
// String inspection is the only reasonable signal — PRAGMA index_list
// also lists the auto-index from the composite UNIQUE so column-level
// detection is unreliable.
func workspaceReposNeedsUniqueDrop(db *sql.DB) (bool, error) {
	var ddl sql.NullString
	row := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'workspace_repos'`,
	)
	if err := row.Scan(&ddl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read workspace_repos ddl: %w", err)
	}
	if !ddl.Valid {
		return false, nil
	}
	// Whitespace varies (formatting, indentation) — collapse and
	// uppercase to make the substring match robust.
	normalised := strings.ToUpper(strings.Join(strings.Fields(ddl.String), " "))
	return strings.Contains(normalised, "PROJECT_PATH TEXT NOT NULL UNIQUE"), nil
}

// rebuildWorkspaceReposWithoutGlobalUnique creates a new
// workspace_repos table with the current shape (no global UNIQUE on
// project_path, is_linked present), copies all rows from the old
// table, drops the old one, renames the new one, and recreates the
// indices. Wrapped in a transaction so a mid-rebuild failure leaves
// the original table intact.
func rebuildWorkspaceReposWithoutGlobalUnique(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin rebuild tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`CREATE TABLE workspace_repos_new (
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
        FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
        FOREIGN KEY (token_id) REFERENCES github_tokens(id) ON DELETE SET NULL
    )`); err != nil {
		return fmt.Errorf("create workspace_repos_new: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO workspace_repos_new
        (id, workspace_id, github_url, branch, project_path,
         token_id, webhook_secret, webhook_id, auto_webhook, webhook_mode,
         status, last_sha, last_error, last_indexed_at,
         created_at, updated_at)
        SELECT id, workspace_id, github_url, branch, project_path,
               token_id, webhook_secret, webhook_id, auto_webhook, webhook_mode,
               status, last_sha, last_error, last_indexed_at,
               created_at, updated_at
          FROM workspace_repos`); err != nil {
		return fmt.Errorf("copy workspace_repos rows: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE workspace_repos`); err != nil {
		return fmt.Errorf("drop old workspace_repos: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE workspace_repos_new RENAME TO workspace_repos`); err != nil {
		return fmt.Errorf("rename workspace_repos_new: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_workspace_repos_workspace ON workspace_repos(workspace_id)`); err != nil {
		return fmt.Errorf("recreate workspace index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_workspace_repos_project ON workspace_repos(project_path)`); err != nil {
		return fmt.Errorf("recreate project index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild tx: %w", err)
	}
	return nil
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
			cid         int
			name, typ   string
			notnull, pk int
			dflt        sql.NullString
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

// migrateEmbeddingProvider adds the pluggable-provider columns to
// runtime_settings:
//   - embedding_provider TEXT — kind selector ("ollama"/"openai"/"voyage")
//   - embedding_provider_config TEXT — provider-specific JSON blob
//
// Rows stay NULL until the admin first persists a non-default provider;
// boot logic in main.go then falls through to the env-derived ollama
// defaults exactly as before. Idempotent — checked via PRAGMA
// table_info, ALTER only on absence.
func migrateEmbeddingProvider(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runtime_settings)`)
	if err != nil {
		return fmt.Errorf("table_info runtime_settings: %w", err)
	}
	have := map[string]bool{}
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
		have[name] = true
	}
	rows.Close()
	if !have["embedding_provider"] {
		if _, err := db.Exec(`ALTER TABLE runtime_settings ADD COLUMN embedding_provider TEXT`); err != nil {
			return fmt.Errorf("add embedding_provider column: %w", err)
		}
	}
	if !have["embedding_provider_config"] {
		if _, err := db.Exec(`ALTER TABLE runtime_settings ADD COLUMN embedding_provider_config TEXT`); err != nil {
			return fmt.Errorf("add embedding_provider_config column: %w", err)
		}
	}
	return nil
}

// migrateIndexedWithModelProviderPrefix backfills projects indexed
// before the pluggable-provider refactor (migration 12). Pre-refactor
// the indexer wrote a bare model name like
// "awhiteside/CodeRankEmbed-Q8_0-GGUF"; post-refactor it writes the
// fully-qualified Provider.ID() of the form "ollama:<model>". Without
// this migration every legacy project would show a "stale model"
// badge forever because the bare string never matches the live
// "ollama:<model>" and a reindex would *still* write the new prefixed
// form — leaving every UN-reindexed project flagged falsely.
//
// Heuristic: rows that don't already start with a known provider-kind
// prefix predate the prefix convention. Prepend "ollama:" — safe
// because pre-refactor there was no other embedding backend; every
// legacy row was produced by the in-process llama-server sidecar.
// (Testing for the kind prefix rather than for the mere presence of a
// ":" matters: a legacy Ollama-style model name like
// "nomic-embed-text:latest" contains a colon but is NOT yet prefixed,
// so a presence-of-colon test would wrongly skip it and leave it
// flagged stale forever.)
//
// Idempotent: rows already starting with ollama:/openai:/voyage: are
// left alone, so re-running this migration (or running it against a DB
// that was already partially upgraded) is a no-op.
func migrateIndexedWithModelProviderPrefix(db *sql.DB) error {
	_, err := db.Exec(`
		UPDATE projects
		SET indexed_with_model = 'ollama:' || indexed_with_model
		WHERE indexed_with_model IS NOT NULL
		  AND indexed_with_model != ''
		  AND indexed_with_model NOT LIKE 'ollama:%'
		  AND indexed_with_model NOT LIKE 'openai:%'
		  AND indexed_with_model NOT LIKE 'voyage:%'
	`)
	if err != nil {
		return fmt.Errorf("backfill indexed_with_model prefix: %w", err)
	}
	return nil
}

// migrateUserLocalProjectDisabled adds users.local_project_disabled to
// pre-feature databases. The column gates local-project creation and
// indexing/reindexing for a user. DEFAULT 0 means every existing row (and
// every future INSERT that omits the column) is "allowed" — backward
// compatible by construction; an admin opts a user out by setting it to 1.
// Idempotent via columnExists.
func migrateUserLocalProjectDisabled(db *sql.DB) error {
	have, err := columnExists(db, "users", "local_project_disabled")
	if err != nil {
		return err
	}
	if have {
		return nil
	}
	if _, err := db.Exec(
		`ALTER TABLE users ADD COLUMN local_project_disabled INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("add users.local_project_disabled: %w", err)
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

// migrateWebhookMode adds workspace_repos.webhook_mode to pre-PR10
// databases and backfills it from the older auto_webhook bool so rows
// inserted before this migration keep their effective behaviour. Same
// PRAGMA-table_info / ALTER-only-if-absent pattern as the other helpers.
func migrateWebhookMode(db *sql.DB) error {
	// workspace_repos may not exist yet on databases that pre-date the
	// workspaces feature entirely — PRAGMA table_info returns no rows in
	// that case and we have nothing to migrate.
	rows, err := db.Query(`PRAGMA table_info(workspace_repos)`)
	if err != nil {
		return fmt.Errorf("table_info workspace_repos: %w", err)
	}
	have := false
	tableExists := false
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
		tableExists = true
		if name == "webhook_mode" {
			have = true
		}
	}
	rows.Close()
	if !tableExists || have {
		return nil
	}
	if _, err := db.Exec(
		`ALTER TABLE workspace_repos ADD COLUMN webhook_mode TEXT NOT NULL DEFAULT 'manual'`,
	); err != nil {
		return fmt.Errorf("add webhook_mode column: %w", err)
	}
	if _, err := db.Exec(
		`UPDATE workspace_repos SET webhook_mode = 'auto' WHERE auto_webhook = 1`,
	); err != nil {
		return fmt.Errorf("backfill webhook_mode: %w", err)
	}
	return nil
}

// migrateSplitWorkspaceRepos converts a legacy workspace_repos table
// into two new tables: git_repos (clone + webhook metadata, 1:1 with
// projects for external repos) and workspace_projects (workspace ↔
// project junction). After the table is consumed it is dropped, so
// re-running the migration on already-migrated DBs is a fast no-op.
//
// dataDir, when non-empty, points at the on-disk workspace data root.
// External (owned, non-linked) workspace_repos rows used to keep their
// clone in {dataDir}/repos/{workspace_repos.id}; we rename those dirs
// to {dataDir}/repos/{path_hash} so the new gitrepos service finds
// them.
//
// Crash-safety contract — FS renames run BEFORE the DB transaction:
// a kill -9 between commit and rename used to leave the DB split but
// the clone dirs stranded under their old UUID names (causing silent
// re-clones on next start). By running renames first and refusing to
// drop workspace_repos when any rename hard-fails, a retry on next
// start is fully idempotent (INSERT OR IGNORE on workspace_projects /
// git_repos, skip-target-exists on the rename loop). Missing source
// dirs (legacy clone job died before mkdir) and pre-existing targets
// (partial rename from a previous run) are non-fatal skips.
//
// Pre-conditions on the legacy table: the earlier migrateWebhookMode +
// migrateWorkspaceReposLinked passes brought it up to the richest
// shape (webhook_mode + is_linked columns present, no global UNIQUE
// on project_path).
func migrateSplitWorkspaceRepos(db *sql.DB, dataDir string) error {
	exists, err := tableExists(db, "workspace_repos")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	type rowSnapshot struct {
		id            string
		workspaceID   string
		githubURL     string
		branch        string
		projectPath   string
		tokenID       sql.NullString
		webhookSecret string
		webhookID     sql.NullInt64
		autoWebhook   int
		webhookMode   string
		lastSHA       sql.NullString
		lastError     sql.NullString
		isLinked      int
		createdAt     string
		updatedAt     string
	}

	rows, err := db.Query(`
		SELECT id, workspace_id, github_url, branch, project_path,
		       token_id, webhook_secret, webhook_id, auto_webhook,
		       webhook_mode, last_sha, last_error, is_linked,
		       created_at, updated_at
		  FROM workspace_repos`)
	if err != nil {
		return fmt.Errorf("select workspace_repos: %w", err)
	}
	var legacy []rowSnapshot
	for rows.Next() {
		var s rowSnapshot
		if err := rows.Scan(
			&s.id, &s.workspaceID, &s.githubURL, &s.branch, &s.projectPath,
			&s.tokenID, &s.webhookSecret, &s.webhookID, &s.autoWebhook,
			&s.webhookMode, &s.lastSHA, &s.lastError, &s.isLinked,
			&s.createdAt, &s.updatedAt,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan workspace_repos row: %w", err)
		}
		legacy = append(legacy, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate workspace_repos: %w", err)
	}

	// Build the rename plan from the legacy snapshot. Only owned
	// external rows (not linked, project_path is github.com/owner/repo@branch)
	// have a clone directory on disk; linked rows reuse the owner's
	// clone, and local projects have no on-disk artifact at all.
	type renamePair struct{ oldID, newHash string }
	var renames []renamePair
	for _, s := range legacy {
		if s.isLinked != 0 || !looksLikeGitHubProjectPath(s.projectPath) {
			continue
		}
		renames = append(renames, renamePair{
			oldID:   s.id,
			newHash: HashHostPath(s.projectPath),
		})
	}

	// Filesystem renames run BEFORE the SQL transaction. If a hard
	// failure happens (permissions, EROFS, …) we return an error and
	// leave workspace_repos intact, so the next process start retries
	// the migration end-to-end. The DB-side inserts are idempotent
	// via INSERT OR IGNORE, and the rename loop's skip-target-exists
	// branch keeps the FS retry idempotent too.
	if dataDir != "" && len(renames) > 0 {
		base := filepath.Join(dataDir, "repos")
		var renamed, skippedMissing, skippedExisting, failed int
		for _, rp := range renames {
			oldPath := filepath.Join(base, rp.oldID)
			newPath := filepath.Join(base, rp.newHash)
			if _, statErr := os.Stat(oldPath); statErr != nil {
				// Source missing — legacy clone job died before mkdir,
				// or a prior run already renamed away. Either way no
				// FS work needed and the next clone job will recreate.
				skippedMissing++
				continue
			}
			if _, statErr := os.Stat(newPath); statErr == nil {
				// Target already there — prior partial run completed
				// this rename. Safe to skip.
				skippedExisting++
				continue
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				failed++
				fmt.Fprintf(os.Stderr,
					"db: migrateSplitWorkspaceRepos: rename %s → %s failed: %v\n",
					oldPath, newPath, err)
				continue
			}
			renamed++
		}
		fmt.Fprintf(os.Stderr,
			"db: migrateSplitWorkspaceRepos: clone-dir renames "+
				"renamed=%d skipped_missing_source=%d skipped_target_exists=%d failed=%d\n",
			renamed, skippedMissing, skippedExisting, failed)
		if failed > 0 {
			return fmt.Errorf(
				"migrateSplitWorkspaceRepos: %d clone-dir rename(s) failed; "+
					"refusing to drop workspace_repos so migration retries on next start",
				failed,
			)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin split tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Pre-seed projects rows for any project_path that's referenced by
	// the legacy workspace_repos but doesn't yet exist in projects
	// (typical state for rows still in clone/index lifecycle when the
	// upgrade boots). Both workspace_projects and git_repos FK to
	// projects(host_path), so the membership + clone-metadata inserts
	// below would fail without this.
	for _, s := range legacy {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO projects (
				host_path, container_path, languages, settings, stats,
				status, created_at, updated_at, path_hash
			) VALUES (?, ?, '[]', '{}', '{}', 'pending', ?, ?, ?)`,
			s.projectPath, s.projectPath,
			s.createdAt, s.updatedAt, HashHostPath(s.projectPath),
		); err != nil {
			return fmt.Errorf("pre-seed projects row for %s: %w", s.projectPath, err)
		}
	}

	for _, s := range legacy {
		// Every legacy row becomes a workspace_projects membership.
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO workspace_projects
				(workspace_id, project_path, added_at)
			VALUES (?, ?, ?)`,
			s.workspaceID, s.projectPath, s.createdAt,
		); err != nil {
			return fmt.Errorf("insert workspace_projects: %w", err)
		}

		// Owned + external rows additionally seed a git_repos row.
		// Linked rows reuse the canonical owner's git_repos row, so we
		// skip them here. Local rows (project_path doesn't look like
		// github.com/owner/repo@branch) have no git_repos representation.
		if s.isLinked != 0 || !looksLikeGitHubProjectPath(s.projectPath) {
			continue
		}
		webhookMode := s.webhookMode
		if webhookMode == "" {
			webhookMode = "manual"
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO git_repos (
				project_path, github_url, branch,
				token_id, webhook_secret, webhook_id,
				webhook_mode, auto_webhook,
				last_sha, last_error,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.projectPath, s.githubURL, s.branch,
			nullableSQL(s.tokenID), s.webhookSecret, nullableSQLInt(s.webhookID),
			webhookMode, s.autoWebhook,
			nullableSQL(s.lastSHA), nullableSQL(s.lastError),
			s.createdAt, s.updatedAt,
		); err != nil {
			return fmt.Errorf("insert git_repos for %s: %w", s.projectPath, err)
		}
	}

	if _, err := tx.Exec(`DROP TABLE workspace_repos`); err != nil {
		return fmt.Errorf("drop workspace_repos: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit split tx: %w", err)
	}
	return nil
}

// looksLikeGitHubProjectPath decides whether a workspace_repos.project_path
// follows the canonical "github.com/owner/repo@branch" shape used for
// external repos. Local-path projects (absolute filesystem paths) fail this
// check and are handled as workspace-only memberships during the split.
func looksLikeGitHubProjectPath(projectPath string) bool {
	s := strings.TrimSpace(projectPath)
	if !strings.HasPrefix(s, "github.com/") {
		return false
	}
	return strings.LastIndex(s, "@") > 0
}

// tableExists returns whether a table with the given name is registered in
// sqlite_master. Used by migrations to short-circuit on already-migrated DBs.
func tableExists(db *sql.DB, name string) (bool, error) {
	row := db.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	var dummy int
	if err := row.Scan(&dummy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("sqlite_master lookup for %q: %w", name, err)
	}
	return true, nil
}

func nullableSQL(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func nullableSQLInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
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
