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

	// Up-level pre-PR10 / pre-PR13 workspace_repos shapes to the richest
	// pre-split form (webhook_mode column present, is_linked column
	// present, no inline-UNIQUE on project_path). On already-current or
	// already-split DBs these are no-ops.
	if err := migrateWebhookMode(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate webhook_mode: %w", err)
	}
	if err := migrateWorkspaceReposLinked(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate workspace_repos is_linked: %w", err)
	}

	// Split the legacy workspace_repos table into git_repos +
	// workspace_projects. Idempotent — when the table is already gone
	// (post-split DBs) the migration returns immediately.
	if err := migrateSplitWorkspaceRepos(db, opts.DataDir); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate workspace_repos → git_repos: %w", err)
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
// them. Failures are logged-and-ignored — the next clone job will
// regenerate the directory from scratch.
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

	// Track rename targets so the filesystem step can run after the tx
	// commits — we don't want to half-rename then roll back the SQL.
	type renamePair struct{ oldID, newHash string }
	var renames []renamePair

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
		renames = append(renames, renamePair{
			oldID:   s.id,
			newHash: HashHostPath(s.projectPath),
		})
	}

	if _, err := tx.Exec(`DROP TABLE workspace_repos`); err != nil {
		return fmt.Errorf("drop workspace_repos: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit split tx: %w", err)
	}

	// Filesystem rename — best effort. Failure is non-fatal; the next
	// clone job will recreate the directory.
	if dataDir != "" {
		base := filepath.Join(dataDir, "repos")
		for _, rp := range renames {
			oldPath := filepath.Join(base, rp.oldID)
			newPath := filepath.Join(base, rp.newHash)
			if _, statErr := os.Stat(oldPath); statErr != nil {
				continue
			}
			if _, statErr := os.Stat(newPath); statErr == nil {
				continue
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				// Log via stderr — the db package has no logger
				// dependency, and a single warning per stuck dir is
				// enough for an operator to find and clean up.
				fmt.Fprintf(os.Stderr,
					"db: warning: could not rename clone dir %s → %s: %v\n",
					oldPath, newPath, err)
			}
		}
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
