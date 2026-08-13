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
    indexed_with_model TEXT,
    -- owner_user_id is the user who owns this (personal, locally indexed)
    -- project. NULL means ownerless: that is the canonical state for EXTERNAL
    -- projects (those with a git_repos row), which are admin-administered and
    -- reachable only via a view-group share. Personal projects are private to
    -- their owner; admins see everything. Set on CreateProject; NULL on
    -- AddGitRepo. FK SET NULL so deleting a user orphans (does not delete) the
    -- project for an admin to reassign.
    owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    -- host_path (PK) is the project IDENTITY, not necessarily the literal
    -- filesystem path. For LOCAL projects it is namespaced as
    -- "local:{machine_id}:{display_path}" so the same path on different
    -- machines (different users) never collides — the CLI computes path_hash
    -- from the same key. For EXTERNAL projects host_path == display_path ==
    -- the github.com/owner/repo@branch string. display_path is the
    -- human-readable path shown in the dashboard; machine_id is the per-home
    -- UUID the CLI sends (NULL for external); machine_label is os.Hostname()
    -- for display only.
    display_path TEXT,
    machine_id TEXT,
    machine_label TEXT,
    -- full_sync_required signals that this project's index is stale at the
    -- FORMAT level (not just content) and needs a complete rebuild — e.g. the
    -- chunker/embedding format changed under it (added in migration 18). It is
    -- purely INFORMATIONAL: it drives the dashboard "out of sync" badge but does
    -- NOT trigger a reindex. An admin starts the full resync manually; the flag
    -- is cleared on the next successful FULL run (FinishIndexing). 0 = in sync.
    full_sync_required INTEGER NOT NULL DEFAULT 0,
    -- full_sync_reason is the human-readable explanation shown next to the
    -- badge (added in migration 18). NULL when full_sync_required = 0.
    full_sync_reason TEXT
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
    role TEXT NOT NULL DEFAULT 'user',
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    disabled_at TEXT,
    -- When 1, the user may not create local projects or index/reindex anything
    -- (search of already-indexed projects + workspace creation stay allowed).
    -- Default 0 = allowed, so existing and new users keep full access; an admin
    -- flips it to 1 to restrict a specific user. Admins are always exempt.
    local_project_disabled INTEGER NOT NULL DEFAULT 0
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
    updated_by TEXT,
    -- Pluggable embedding provider (added in migration 12).
    -- embedding_provider selects the active backend kind; if NULL the
    -- server falls back to the env/recommended ollama defaults so old
    -- installs stay on llama-server until the admin picks otherwise.
    embedding_provider TEXT,
    -- embedding_provider_config holds the provider-specific config as
    -- a JSON blob (shape varies by provider). API keys are NEVER stored
    -- here — providers read them live from env vars named in this blob.
    embedding_provider_config TEXT,
    -- Cross-file embed-batch size for repo indexing (added in migration 15).
    -- NULL → fall through to env / recommended.
    index_embed_batch_chunks INTEGER,
    -- Chunker (tree-sitter wasm) instance-concurrency cap (added in migration 16).
    -- NULL → fall through to env / recommended (3).
    chunk_max_concurrent INTEGER,
    -- llama-server host prompt-cache cap in MiB, --cache-ram (added in
    -- migration 17). NULL → fall through to env / recommended (0 = disabled:
    -- embeddings never reuse prompts, and llama's own 8 GiB default
    -- OOM-killed the prod container). -1 = unlimited.
    llama_cache_ram_mib INTEGER
);

-- Workspaces group indexed projects (rows in the projects table,
-- optionally with their git_repos peer) for cross-project semantic
-- search. Membership lives in workspace_projects; clone + webhook
-- metadata lives in git_repos.
--
-- owner_user_id is the user who created the workspace; visible to the owner,
-- to members of any view-group it is shared to (workspace_group_shares), and
-- to admins. A workspace is decoupled from its projects: access is always
-- evaluated per project, so a project a viewer cannot see is simply hidden
-- from the workspace listing / search.
CREATE TABLE IF NOT EXISTS workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL
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

-- git_repos holds clone + webhook metadata for projects that come from a
-- git remote (currently GitHub-only). Exactly 1:1 with the corresponding
-- projects row — keyed by project_path = projects.host_path. Local
-- projects (indexed via the CLI) have no git_repos row at all, which
-- is how the server tells them apart from cloneable repos.
--
-- webhook_secret is generated server-side at create time and shown
-- exactly once to the operator (or consumed by the auto-register flow).
-- token_id stays nullable so public repos can be cloned without a PAT.
-- last_sha lets an incremental fetch short-circuit when HEAD hasn't
-- moved; status lives on the projects row (single source of truth).
CREATE TABLE IF NOT EXISTS git_repos (
    project_path    TEXT PRIMARY KEY,
    github_url      TEXT NOT NULL,
    branch          TEXT NOT NULL,
    token_id        TEXT,
    webhook_secret  TEXT NOT NULL,
    webhook_id      INTEGER,
    -- webhook_mode = 'auto' | 'manual' | 'disabled'. See workspaces docs.
    webhook_mode    TEXT NOT NULL DEFAULT 'manual',
    auto_webhook    INTEGER NOT NULL DEFAULT 0,
    last_sha        TEXT,
    -- indexed_sha is the SHA of the commit whose tree is currently
    -- reflected in the index. Distinct from last_sha (which is "what's
    -- on disk after fetch+reset"). Set atomically after a successful
    -- FinishIndexing; NULL when the repo has never been indexed yet.
    -- The diff between indexed_sha (old) and last_sha (new) drives the
    -- incremental reindex path — git tree.Diff returns the exact set
    -- of changed/added/deleted files without any disk I/O on unchanged
    -- ones. NULL → forces a full reindex on the next clone_repo job.
    indexed_sha     TEXT,
    last_error      TEXT,
    -- Polling sync (alternative to webhooks, for repos where the user is
    -- not an admin and cannot install a hook). Gated on webhook_mode =
    -- 'disabled' — a repo uses webhook OR polling, never both. When
    -- polling_enabled = 1 the shared poll scheduler enqueues a clone_repo
    -- job whenever next_poll_at is due. poll_interval_seconds NULL → use
    -- the server default (CIX_DEFAULT_POLL_INTERVAL). next_poll_at NULL →
    -- not scheduled (polling off). Cadence is measured from the END of the
    -- last index run, written by the clone/index completion handlers.
    polling_enabled       INTEGER NOT NULL DEFAULT 0,
    poll_interval_seconds INTEGER,
    next_poll_at          TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (github_url, branch),
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE,
    FOREIGN KEY (token_id) REFERENCES github_tokens(id) ON DELETE SET NULL
);
-- NOTE: CREATE INDEX idx_git_repos_due is intentionally NOT here. Pre-m9
-- databases lack the polling columns when Schema.Exec runs (migrations run
-- after), so the index is created by migration 9 instead — same pattern as
-- idx_projects_path_hash (see the path_hash note above).

-- workspace_projects is the many-to-many junction between workspaces
-- and projects. A workspace is just a labelled collection — adding a
-- project = INSERT here, removing = DELETE here. The project itself
-- is untouched. The same project can live in any number of workspaces.
-- ON DELETE CASCADE on both FKs keeps memberships consistent: deleting
-- a workspace or a project automatically clears the rows that name it.
CREATE TABLE IF NOT EXISTS workspace_projects (
    workspace_id  TEXT NOT NULL,
    project_path  TEXT NOT NULL,
    added_at      TEXT NOT NULL,
    PRIMARY KEY (workspace_id, project_path),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_workspace_projects_project
    ON workspace_projects(project_path);

-- view_groups + sharing (auth model). A view-group is an admin-managed set of
-- users; external projects and workspaces are shared TO a group, granting its
-- members read/search access. Group CRUD and membership are admin-only;
-- workspace owners may share their own workspaces to groups they belong to.
CREATE TABLE IF NOT EXISTS view_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS view_group_members (
    group_id  TEXT NOT NULL,
    user_id   TEXT NOT NULL,
    added_at  TEXT NOT NULL,
    PRIMARY KEY (group_id, user_id),
    FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_view_group_members_user ON view_group_members(user_id);

-- project_group_shares: an EXTERNAL project (git_repos peer, owner_user_id NULL)
-- shared to a view-group. Members of group_id get read/search on project_path.
CREATE TABLE IF NOT EXISTS project_group_shares (
    project_path TEXT NOT NULL,
    group_id     TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (project_path, group_id),
    FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_project_shares_group ON project_group_shares(group_id);

-- workspace_group_shares: a workspace shared to a view-group. Visibility only;
-- the per-project access check still gates which projects a member actually sees.
CREATE TABLE IF NOT EXISTS workspace_group_shares (
    workspace_id TEXT NOT NULL,
    group_id     TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (workspace_id, group_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES view_groups(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_workspace_shares_group ON workspace_group_shares(group_id);

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

-- PR14 dropped the workspaces communities/community_members tables.
-- Workspace search is now a weighted fan-out across per-project chromem
-- collections (no Louvain, no centroid index). migrateDropCommunities
-- DROPs the tables on upgrade for installs that ran any of PR5..PR12.

-- chunks_meta is the row-level shadow for chunks_fts: it stores the
-- non-content metadata we need to retrieve when a BM25 query matches.
-- chunks_fts (FTS5 virtual table) can only filter efficiently by rowid,
-- so we keep a regular indexed table here for (project_path, file_path)
-- lookups and join by rowid on retrieval/deletion.
CREATE TABLE IF NOT EXISTS chunks_meta (
    rowid        INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT    NOT NULL,
    file_path    TEXT    NOT NULL,
    start_line   INTEGER NOT NULL,
    end_line     INTEGER NOT NULL,
    chunk_type   TEXT,
    symbol_name  TEXT,
    language     TEXT
);
CREATE INDEX IF NOT EXISTS idx_chunks_meta_project_file
    ON chunks_meta(project_path, file_path);
CREATE INDEX IF NOT EXISTS idx_chunks_meta_project
    ON chunks_meta(project_path);

-- chunks_fts is the BM25-searchable side, parallel to chromem-go's dense
-- vector store. Workspace search runs both in parallel per project then
-- fuses by RRF; project-relevance gating uses BM25 signal to drop repos
-- that share no token with the query (the dense-only fan-out leaks
-- semantically-distant repos as false positives).
--
-- tokenize='trigram': substring matching on identifiers (CamelCase /
-- snake_case / dotted paths are not tokenized to word boundaries
-- predictably enough for code). Short acronyms like "ABC" become a
-- single trigram; lookups are exact-substring within a word.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    content,
    symbol_name,
    file_path,
    tokenize = 'trigram'
);

-- tunnel_config holds the single dashboard-managed Managed Tunnel row.
-- The feature has no env enable flag — enable/mode/hostname/token live
-- here and are edited from the dashboard. encrypted_token is the
-- AES-GCM-encrypted Cloudflare named-tunnel connector token (NULL for
-- quick mode). CHECK(id=1) enforces a single row; UPSERT on id=1.
CREATE TABLE IF NOT EXISTS tunnel_config (
    id              INTEGER PRIMARY KEY CHECK(id=1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    provider        TEXT    NOT NULL DEFAULT 'cloudflare',
    mode            TEXT    NOT NULL DEFAULT 'quick',
    hostname        TEXT    NOT NULL DEFAULT '',
    encrypted_token BLOB,
    updated_at      TEXT    NOT NULL,
    updated_by      TEXT
);

-- scheduled_tasks is when each recurring task runs (migration 20). One row per
-- task, keyed by the stable name its owner registers under.
--
-- The schedule is a crontab expression rather than an interval, because an
-- interval is measured from the last run and therefore drifts: one manual run
-- at 18:00 moves every subsequent nightly run to 18:00. cron is anchored to
-- the clock, which is what an operator means by "every night at midnight".
--
-- next_run_at is derived, never authoritative — it is recomputed from cron and
-- the current time whenever it is missing or was derived from a different
-- expression (cron_used). That is what makes a missed slot impossible to
-- lose: even an empty or nonsensical row re-arms itself on the next tick.
--
-- last_run_at is stamped *before* the handler runs, not after. One of these
-- tasks re-executes the whole process as its final step, so a slot still
-- marked due when the new process starts would fire it again, and again.
--
-- The configured column separates the two kinds of write this table takes. The
-- scheduler itself writes only the derived columns — cron_used, next_run_at,
-- and the outcome of the last run — and must never touch cron or enabled,
-- because arming a task is not the same as somebody choosing to enable it.
-- Only an admin saving a schedule sets configured, and only a configured row
-- overrides the built-in default.
CREATE TABLE IF NOT EXISTS scheduled_tasks (
    name         TEXT PRIMARY KEY,
    configured   INTEGER NOT NULL DEFAULT 0,
    cron         TEXT,
    enabled      INTEGER,
    cron_used    TEXT,
    next_run_at  TEXT,
    last_run_at  TEXT,
    last_status  TEXT,
    last_error   TEXT,
    last_millis  INTEGER,
    updated_at   TEXT NOT NULL,
    updated_by   TEXT
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
	"users",
	"sessions",
	"api_keys",
	"runtime_settings",
	"workspaces",
	"github_tokens",
	"git_repos",
	"workspace_projects",
	"view_groups",
	"view_group_members",
	"project_group_shares",
	"workspace_group_shares",
	"jobs",
	"call_edges",
	"chunks_meta",
	"chunks_fts",
	"tunnel_config",
	"scheduled_tasks",
	"schema_migrations",
}
