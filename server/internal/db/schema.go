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
    path_hash TEXT,
    -- indexed_with_model is the embedding model identifier active when the
    -- project was last indexed. NULL on legacy rows (pre-PR-E) until next
    -- reindex. Compared against the live runtime model to surface a "stale
    -- model" badge on the dashboard project list.
    indexed_with_model TEXT
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

-- runtime_settings holds the single dashboard-overridable runtime config row
-- (PR-E). NULL columns mean "fall through to env / recommended". The
-- CHECK(id=1) constraint enforces a single-row table; UPSERT uses
-- INSERT OR REPLACE on id=1.
CREATE TABLE IF NOT EXISTS runtime_settings (
    id INTEGER PRIMARY KEY CHECK(id=1),
    embedding_model TEXT,
    llama_ctx_size INTEGER,
    llama_n_gpu_layers INTEGER,
    llama_n_threads INTEGER,
    max_embedding_concurrency INTEGER,
    llama_batch_size INTEGER,
    updated_at TEXT NOT NULL,
    updated_by TEXT
);

-- Workspaces feature (PR1 — skeleton). Workspaces group GitHub repositories
-- for cross-project semantic search. Server-wide shared: every authenticated
-- user can see and modify any workspace (per the chosen visibility model).
-- The richer workspace_repos / call_edges / communities tables land in
-- subsequent PRs of the workspaces feature branch.
CREATE TABLE IF NOT EXISTS workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- github_tokens stores GitHub Personal Access Tokens encrypted at rest with
-- AES-256-GCM. Only the encrypted blob ever lives in SQLite — the plaintext
-- is returned exactly once on POST /api/v1/github-tokens and then forgotten.
-- The encryption key comes from CIX_SECRET_KEY / CIX_SECRET_KEYFILE / a
-- generated keyfile (see internal/secrets).
CREATE TABLE IF NOT EXISTS github_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    encrypted    BLOB NOT NULL,
    scopes       TEXT,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);

-- Workspaces feature PR2 — workspace_repos + jobs.
--
-- One workspace_repos row per (repo, branch). project_path is the canonical
-- "github.com/owner/repo@branch" string used as host_path in projects, so
-- existing per-project SQL stays uniform across local + remote sources.
-- webhook_secret is generated server-side at create time and shown exactly
-- once to the operator (or used by the auto-register flow added in PR3).
-- token_id stays nullable so public repos can be added without storing a PAT.
-- last_sha / last_indexed_at survive across reindexes so an incremental
-- fetch_repo job can short-circuit when HEAD hasn't moved.
CREATE TABLE IF NOT EXISTS workspace_repos (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL,
    github_url      TEXT NOT NULL,
    branch          TEXT NOT NULL,
    project_path    TEXT NOT NULL UNIQUE,
    token_id        TEXT,
    webhook_secret  TEXT NOT NULL,
    webhook_id      INTEGER,
    auto_webhook    INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    last_sha        TEXT,
    last_error      TEXT,
    last_indexed_at TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(workspace_id, github_url, branch),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (token_id) REFERENCES github_tokens(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_workspace_repos_workspace ON workspace_repos(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_repos_project ON workspace_repos(project_path);

-- jobs is the persistent worker queue. Survives process restarts; one
-- worker pool drains it. dedupe_key is the partial-unique mechanism that
-- collapses webhook bursts (e.g. 50 push deliveries for the same repo
-- become 1 pending fetch_repo job). status transitions:
--   pending → running → completed | failed
-- Failed jobs may be re-enqueued by the worker up to a per-type retry
-- budget (attempts column tracks the count).
CREATE TABLE IF NOT EXISTS jobs (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    dedupe_key    TEXT,
    payload       TEXT NOT NULL DEFAULT '{}',
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    last_error    TEXT,
    scheduled_at  TEXT NOT NULL,
    started_at    TEXT,
    completed_at  TEXT,
    created_at    TEXT NOT NULL
);
-- Partial unique index — at most one active job per dedupe_key. Insert
-- a second pending row for the same key and SQLite raises a UNIQUE
-- constraint error, which the jobs service translates to "already
-- enqueued, no-op".
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_dedupe_active ON jobs(dedupe_key)
    WHERE dedupe_key IS NOT NULL AND status IN ('pending','running');
CREATE INDEX IF NOT EXISTS idx_jobs_ready ON jobs(status, scheduled_at)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_jobs_type_status ON jobs(type, status);

-- Workspaces feature PR4 — call_edges + edge_provenance.
--
-- call_edges holds the approximate caller→callee edges that Louvain
-- consumes in PR5. It is a CO-OCCURRENCE graph, not an exact call graph:
-- we resolve callees by name lookup in the symbols table constrained
-- by the caller's enclosing scope, then weight each edge inversely with
-- the callee-name popcount (1 / N). That keeps Louvain robust to
-- name-collision noise (common in Python/JS) without needing per-language
-- type resolution.
--
-- One row per (caller, callee). Duplicate caller→callee pairs collapse
-- by accumulating weight at insert time (handler-side INSERT OR REPLACE
-- against the unique constraint below). source identifies which heuristic
-- produced the edge — useful for the eval harness and for swapping in
-- alternative graph builders without dropping the table.
CREATE TABLE IF NOT EXISTS call_edges (
    project_path  TEXT NOT NULL,
    caller_symbol TEXT NOT NULL,
    callee_symbol TEXT NOT NULL,
    weight        REAL NOT NULL,
    source        TEXT NOT NULL DEFAULT 'refs_heuristic',
    PRIMARY KEY (project_path, caller_symbol, callee_symbol),
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE,
    FOREIGN KEY (caller_symbol) REFERENCES symbols(id) ON DELETE CASCADE,
    FOREIGN KEY (callee_symbol) REFERENCES symbols(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_call_edges_project ON call_edges(project_path);
CREATE INDEX IF NOT EXISTS idx_call_edges_caller ON call_edges(caller_symbol);
CREATE INDEX IF NOT EXISTS idx_call_edges_callee ON call_edges(callee_symbol);

-- Workspaces feature PR5 — communities + community_members.
--
-- One community = one Louvain output cluster on the workspace's
-- combined call_edges graph. label is derived heuristically from the
-- top symbol names; parent_id is non-null for sub-communities produced
-- by the >50-chunks recursive-split rule.
--
-- compute_workspace_communities job DELETES + reinserts all rows for
-- a workspace_id on each rebuild, so the table reflects the latest
-- Louvain output without orphans. The downstream centroid Chroma
-- collection (workspace_{id}_centroids) is rebuilt in lock-step.
CREATE TABLE IF NOT EXISTS communities (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    label        TEXT,
    size         INTEGER NOT NULL DEFAULT 0,
    parent_id    TEXT,
    created_at   TEXT NOT NULL,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES communities(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_communities_workspace ON communities(workspace_id);

CREATE TABLE IF NOT EXISTS community_members (
    community_id TEXT NOT NULL,
    project_path TEXT NOT NULL,
    symbol_id    TEXT NOT NULL,
    PRIMARY KEY (community_id, project_path, symbol_id),
    FOREIGN KEY (community_id) REFERENCES communities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_community_members_symbol ON community_members(project_path, symbol_id);
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
	"runtime_settings",
	"workspaces",
	"github_tokens",
	"workspace_repos",
	"jobs",
	"call_edges",
	"communities",
	"community_members",
}
