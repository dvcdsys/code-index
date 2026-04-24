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
`

// ExpectedTables lists the tables the schema creates. Used by db_test and by
// /api/v1/status consistency checks.
var ExpectedTables = []string{
	"projects",
	"file_hashes",
	"symbols",
	"refs",
	"index_runs",
}
