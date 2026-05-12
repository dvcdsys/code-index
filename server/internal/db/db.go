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

	// PR10 — extend workspace_repos with webhook_mode so the dashboard
	// can distinguish manual/auto/disabled intents. Older databases get
	// the column with a sensible default; rows where auto_webhook=1 are
	// retro-fitted to 'auto' so they keep the same effective behaviour.
	if err := migrateWebhookMode(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate webhook_mode: %w", err)
	}

	// PR13 — workspace_repos.is_linked + drop the legacy global UNIQUE
	// on project_path. The rebuild path is taken only when the old
	// constraint is still present; freshly-created DBs hit the new
	// CREATE TABLE shape via Schema and the rebuild becomes a no-op.
	if err := migrateWorkspaceReposLinked(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate workspace_repos is_linked: %w", err)
	}

	// PR14 — workspace search switched from the Louvain-centroid two-
	// stage pipeline to a weighted fan-out. The communities +
	// community_members tables stop being written; drop them on
	// upgrade so the schema reflects what's actually used.
	if err := migrateDropCommunities(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate drop communities: %w", err)
	}

	return db, nil
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
