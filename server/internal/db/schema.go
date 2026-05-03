package db

// Schema is the SQLite DDL. Ported 1:1 from api/app/database.py:8-75.
// Keep this file byte-aligned with Python if possible — divergence breaks
// parity guarantees between the two backends during parallel rollout.
const Schema = `
CREATE TABLE IF NOT EXISTS projects (
    host_path TEXT PRIMARY KEY,
    container_path TEXT NOT NULL,
    languages TEXT DEFAULT '[]',
    settings TEXT DEFAULT '{}',
    stats TEXT DEFAULT '{"total_files":0,"indexed_files":0,"total_chunks":0,"total_symbols":0}',
    status TEXT DEFAULT 'created',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_indexed_at TEXT,
    -- path_hash is the first 16 hex chars of SHA1(host_path). It replaces the
    -- O(n) GetByHash scan with an O(log n) index lookup. Computed in Go on
    -- insert; the column is nullable here so migrating databases can backfill
    -- lazily via Open's ALTER+UPDATE hook.
    path_hash TEXT
);

-- NOTE: CREATE INDEX on path_hash is intentionally NOT here. Pre-m7 databases
-- have a projects table without the path_hash column; creating the index
-- against a multi-statement Schema.Exec would fail before migratePathHash
-- has a chance to add the column. Index creation lives in migratePathHash
-- where the column is guaranteed to exist (either by fresh CREATE TABLE
-- above or by ALTER TABLE ADD COLUMN in the migration).

CREATE TABLE IF NOT EXISTS file_hashes (
    project_path TEXT NOT NULL,
    file_path TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    indexed_at TEXT NOT NULL,
    PRIMARY KEY (project_path, file_path),
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS symbols (
    id TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    file_path TEXT NOT NULL,
    line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    language TEXT NOT NULL,
    signature TEXT,
    parent_name TEXT,
    docstring TEXT,
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_symbols_project_name ON symbols(project_path, name);
CREATE INDEX IF NOT EXISTS idx_symbols_project_kind ON symbols(project_path, kind);
CREATE INDEX IF NOT EXISTS idx_symbols_project_file ON symbols(project_path, file_path);

CREATE TABLE IF NOT EXISTS refs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    line INTEGER NOT NULL,
    col INTEGER NOT NULL,
    language TEXT NOT NULL,
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_refs_project_name ON refs(project_path, name);
CREATE INDEX IF NOT EXISTS idx_refs_project_file ON refs(project_path, file_path);

CREATE TABLE IF NOT EXISTS index_runs (
    id TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    files_processed INTEGER DEFAULT 0,
    files_total INTEGER DEFAULT 0,
    chunks_created INTEGER DEFAULT 0,
    status TEXT DEFAULT 'running',
    error_message TEXT,
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
);

-- Dashboard auth: users/sessions/api_keys.
-- Added in the dashboard branch when the single-CIX_API_KEY model was
-- replaced with per-user accounts and named API keys. Old deployments are
-- still expected to come up cleanly: the bootstrap flow in main.go creates
-- the first admin from CIX_BOOTSTRAP_ADMIN_{EMAIL,PASSWORD} on a fresh DB.
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'viewer',
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    disabled_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    last_seen_ip TEXT,
    last_seen_ua TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    prefix TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    last_used_at TEXT,
    last_used_ip TEXT,
    last_used_ua TEXT,
    revoked_at TEXT,
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_apikeys_owner ON api_keys(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_apikeys_hash ON api_keys(hash);
`

// ExpectedTables lists the tables the schema creates. Used by db_test and by
// /api/v1/status consistency checks.
var ExpectedTables = []string{
	"projects",
	"file_hashes",
	"symbols",
	"refs",
	"index_runs",
	"users",
	"sessions",
	"api_keys",
}
