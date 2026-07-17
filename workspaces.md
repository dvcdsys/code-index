# Workspaces

> [!NOTE]
> Workspaces are off by default. Set `CIX_WORKSPACES_ENABLED=true` and
> restart the server to enable them — see
> [§ Enabling workspaces](#enabling-workspaces). The defaults and search
> algorithm are calibrated on a 113-query eval
> (see [§ Search algorithm](#search-algorithm)).

A **workspace** is a named group of repositories that cix can search **as
one corpus**. Where `cix search` is for the project you're `cd`'d into, a
workspace is for tasks that span multiple repos — microservices that talk
to each other, a feature whose implementation crosses several services,
or any time the answer is "look in N repos, not one".

Workspaces clone GitHub repositories server-side and index them next to
your local projects, then expose a single hybrid (BM25 + dense)
cross-project search endpoint.

[← back to main README](README.md)

---

## Table of contents

- [What you get](#what-you-get)
- [Enabling workspaces](#enabling-workspaces)
- [Concepts](#concepts)
- [Quick start](#quick-start)
- [Adding repositories](#adding-repositories)
- [GitHub tokens](#github-tokens)
- [Searching a workspace](#searching-a-workspace)
- [Search algorithm](#search-algorithm)
- [Webhooks (auto-reindex on push)](#webhooks-auto-reindex-on-push)
- [Strengths and weaknesses](#strengths-and-weaknesses)
- [Configuration reference](#configuration-reference)
- [REST API reference](#rest-api-reference)
- [Troubleshooting](#troubleshooting)
- [Agent integration](#agent-integration)

---

## What you get

- **Cross-project semantic search.** One query, one ranked response across
  every indexed repo in the workspace. Returns `projects[]` (which repos
  look relevant) and `chunks[]` (round-robin interleaved snippets).
- **Server-side clones of GitHub repositories.** Add a repo by URL +
  branch; the server clones it under its data directory and indexes it
  the same way it indexes local projects.
- **GitHub PAT management.** Store a personal access token once, use it
  to clone private repos and (optionally) auto-register push webhooks.
  Tokens are AES-256-GCM encrypted at rest.
- **Auto-reindex on push.** With `auto` webhook mode the server
  registers a GitHub webhook on the repo and re-clones + reindexes on
  every push to the tracked branch.
- **Dashboard UI.** Browser-facing CRUD for workspaces, repos, tokens,
  and a two-stage search interface.
- **CLI integration.** `cix ws` from the terminal: list/describe
  workspaces and run cross-project search, plus manage them — create a
  workspace, link/unlink already-indexed projects, rename, and delete.
- **Agent skill.** A `cix-workspace` skill teaches AI agents how to use
  the workspace search responsibly, with a dedicated
  `cix-workspace-investigator` sub-agent for parallel per-repo
  investigation. See [`skills/cix-workspace/SKILL.md`](skills/cix-workspace/SKILL.md).

---

## Enabling workspaces

The feature is off by default. Set the flag in `.env` (or the equivalent
in your deployment):

```bash
CIX_WORKSPACES_ENABLED=true
```

…and restart the server. Without the flag every workspace endpoint
returns `503 Service Unavailable` with `workspaces feature is disabled
(set CIX_WORKSPACES_ENABLED=true and restart)`.

You may also want to set:

```bash
# Where workspace repo clones live on disk. Defaults to <data-dir>/repos.
CIX_WORKSPACES_DATA_DIR=/var/lib/cix/repos

# Public URL of this server — required if you want auto-registered
# GitHub webhooks. Without it, webhook mode falls back to `manual`.
CIX_PUBLIC_URL=https://cix.example.com

# Encryption key for GitHub tokens. If unset, the server auto-generates
# one at <data-dir>/.secret_key on first boot.
CIX_SECRET_KEY=$(openssl rand -hex 32)
```

---

## Concepts

### Workspace

A named group with an `id` (UUID), `name` (unique), and `description`.
A user creates a workspace, then attaches repositories to it. A workspace
has no built-in access control beyond what the server's auth layer already
provides — anyone authenticated can list and search workspaces today.

### Workspace project (membership)

A row in `workspace_projects` that ties one indexed project to a
workspace. Both the `projects` row and the workspace must already exist
— linking is the act of declaring "this project participates in this
workspace's cross-project search". Two underlying project kinds make it
into a workspace:

- **GitHub-cloned project** — backed by a row in the `git_repos` table.
  The server cloned the repo to disk and indexed it. `host_path` looks
  like `github.com/owner/repo@branch`. Its lifecycle (clone, index,
  reindex, webhook) lives in the `projects` row's `status` column.
- **Local-path project** — backed only by the `projects` row, no
  `git_repos` peer. Created with `cix init` against an absolute filesystem
  path, indexed by the local CLI / file watcher rather than the server's
  clone pipeline. Useful for including your primary repo in a workspace
  without duplicating data.

Both kinds are linked into a workspace identically — there is no
`is_linked` column anymore. The distinction is "does a `git_repos` row
exist for this project?".

### GitHub token

A personal access token (PAT) the server uses to clone private repos and
optionally register webhooks. Stored AES-256-GCM-encrypted; the plaintext
is returned to the client exactly once (on creation) and never again.

### Project path

Workspace repos use the canonical form `github.com/owner/repo@branch`
(e.g. `github.com/acme/api-server@main`) as their `project_path`. This
is the same identifier you'll see in the `projects[]` panel and in chunk
records when searching.

---

## Quick start

End-to-end walkthrough, assuming the server is up and you have a fresh
admin login.

### 1. Enable the feature

```bash
echo 'CIX_WORKSPACES_ENABLED=true' >> .env
docker compose restart   # or `make run` for native
```

### 2. Create a workspace

From the dashboard: open `/dashboard/workspaces` → **Create workspace**.

Or from the API:

```bash
curl -X POST http://localhost:21847/api/v1/workspaces \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"platform","description":"Platform services"}'
# → {"id":"4f2a785c-...","name":"platform",...}
```

### 3. Add a GitHub token (if any repo is private)

Dashboard: **API Keys** page → **GitHub tokens** tab → **Add token**.
Paste a PAT scoped at minimum to `repo` (for private repos) and
`admin:repo_hook` (for auto-registered webhooks).

Or:

```bash
curl -X POST http://localhost:21847/api/v1/github-tokens \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -d '{"name":"my-pat","token":"ghp_xxx..."}'
# → {"id":"abc-123","name":"my-pat","scopes":["repo","admin:repo_hook"]}
```

The token's plaintext is **never echoed back**. Lose the value and you
must rotate.

### 4. Attach a repo

Dashboard: open the workspace → **Add GitHub repository** → walk through
the staged dialog (token → account/org → repo → branch → webhook mode).

Or via the API — register the project + clone metadata, then link it
into the workspace:

```bash
# Step 1 — register the project + git_repos row (kicks off clone + index).
curl -X POST http://localhost:21847/api/v1/git-repos \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -d '{
    "github_url":"https://github.com/acme/api-server",
    "branch":"main",
    "token_id":"abc-123",
    "webhook_mode":"manual"
  }'
# → {"path_hash":"abc1234567890def","project_path":"github.com/acme/api-server@main","status":"created",...}

# Step 2 — link the new path_hash into the workspace.
curl -X POST http://localhost:21847/api/v1/workspaces/<workspace-id>/projects \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -d '{"path_hash":"abc1234567890def"}'
```

Status will transition through `created → indexing → indexed` over the
next minutes (depends on repo size + embedding throughput).

### 5. Watch the indexing progress

```bash
curl -H "Authorization: Bearer $CIX_API_KEY" \
  http://localhost:21847/api/v1/workspaces/<workspace-id>/projects
# Look for `project.status: "indexed"` per project.
```

### 6. Search

CLI:

```bash
cix ws platform search "authentication middleware"
```

Or directly:

```bash
curl -G -H "Authorization: Bearer $CIX_API_KEY" \
  --data-urlencode "q=authentication middleware" \
  http://localhost:21847/api/v1/workspaces/<workspace-id>/search
```

---

## Adding repositories

### GitHub-cloned vs local-path projects

| | GitHub-cloned project | Local-path project |
|---|---|---|
| Source | GitHub clone | Existing `cix init`'d local project |
| Clone path | `<data-dir>` → `repos` → `<path_hash>` | n/a (uses original) |
| Backing tables | `projects` + `git_repos` | `projects` only |
| Index lifecycle | Server-managed | Whatever the user runs locally |
| Indexed by | Server's index pipeline | `cix init` / `cix watch` |
| Webhooks | Supported | Not applicable |
| Created via | `POST /api/v1/git-repos` | `POST /api/v1/projects` (or `cix init` locally) |
| Linked into workspace via | `POST /api/v1/workspaces/{id}/projects` | `POST /api/v1/workspaces/{id}/projects` |
| Dashboard | **Add GitHub repository** button | **Link existing project** button |

Both kinds are linked into a workspace through the same membership
endpoint; the only difference is which table owns the project's
clone-and-webhook metadata. Use a local-path project when the primary
project you're working in should appear in the workspace search but you
don't want a second clone.

### From the dashboard

The **Add repository** dialog is staged:

1. **Pick a GitHub token** — required for private repos. Public repos
   can be added without a token (HTTPS anonymous clone).
2. **Pick an account** — your user or one of the orgs visible to the
   token's scopes.
3. **Pick a repo** — fetched from GitHub via the token (up to 500
   shown; search to narrow).
4. **Pick a branch** — defaults to the repo's default branch.
5. **Pick a webhook mode** — `manual` / `auto` / `disabled`. See
   [Webhooks](#webhooks-auto-reindex-on-push).

The dialog calls `POST /api/v1/git-repos` to register the project +
clone metadata, then `POST /api/v1/workspaces/{id}/projects` to link
the resulting `path_hash` into the workspace. The clone + index job
runs in the background.

### From the CLI

`cix ws` manages workspaces and their membership from the terminal — the
counterpart to the dashboard's **Link existing project** button. It links
projects that are **already indexed** (local `cix init` projects or
previously-cloned GitHub repos); it does **not** clone. To clone and index
a *new* GitHub repo, use the dashboard or `POST /git-repos` (below).

```bash
# Create a workspace
cix ws create "platform" --description "core platform repos"

# Link an already-indexed project — by absolute path, host_path, or path_hash
cix ws platform add /Users/me/svc-a
cix ws platform add github.com/owner/repo@main
cix ws platform add .                     # the current directory

# Unlink / rename / delete
cix ws platform remove github.com/owner/repo@main
cix ws platform rename "platform-core"
cix ws platform delete                     # prompts; -y to skip
```

Linking requires the project to be in `indexed` status; a still-cloning
project returns a "not yet indexed" error until the pipeline finishes. See
the [CLI reference](doc/CLI_REFERENCE.md#workspaces-cross-repo) for the full
verb list.

### From the API

Registering a GitHub-cloned project is a two-step flow: create the
project (with its `git_repos` peer) via `POST /git-repos`, then link
the resulting `path_hash` into the workspace via
`POST /workspaces/{id}/projects`.

```bash
# 1. Register the project + git_repos row (kicks off clone + index).
curl -X POST http://localhost:21847/api/v1/git-repos \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "github_url": "https://github.com/owner/repo",
    "branch": "main",
    "token_id": "<token-uuid-or-null>",
    "webhook_mode": "manual"
  }'
# → {"path_hash":"abc1234567890def","project_path":"github.com/owner/repo@main",...}

# 2. Link the path_hash into the workspace.
curl -X POST http://localhost:21847/api/v1/workspaces/<id>/projects \
  -H "Authorization: Bearer $CIX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"path_hash": "abc1234567890def"}'
```

Response fields worth knowing on the `POST /git-repos` response:

- `path_hash` — the 16-hex-char project identifier used by every
  per-project endpoint (`/projects/{hash}/reindex`,
  `/webhooks/github/{hash}`, etc.).
- `project_path` — `github.com/owner/repo@branch`, the search
  identifier the dashboard surfaces.
- `status` — lives on the `projects` row; starts at `created`,
  transitions through `indexing` to `indexed` once the pipeline
  finishes.
- `webhook_secret` — server-generated HMAC secret. Returned exactly
  once if you set `webhook_mode=manual`. Use it when you configure the
  webhook on GitHub manually.

### Cloning, indexing, and status transitions

`projects.status` tracks the per-project lifecycle. What happens after
`POST /git-repos`:

1. **`created`** — rows inserted in `projects` and `git_repos`. Clone
   job queued.
2. **`indexing`** — server fetches via `git clone` (or `git fetch +
   checkout` if the repo is already on disk) into
   `<CIX_WORKSPACES_DATA_DIR>/<path_hash>/`, then runs the indexer
   pipeline (tree-sitter chunking → embeddings → vector store + FTS5
   mirror). Private repos use the attached token.
3. **`indexed`** — `last_indexed_at` updated, project is searchable.
4. **`error`** — clone or index errored out. The dashboard surfaces
   the underlying error from the job. Common causes: invalid token,
   repo not found, branch doesn't exist, embedder unavailable.

Clone + index parallelism: `CIX_WORKER_CONCURRENCY` (default `2`).
Increase for fleet onboarding; lower if you saturate disk or GPU.

### Reindexing a single project

Per-project endpoint — the same call reindexes a GitHub-cloned project
or a local-path project, no workspace context needed.

```bash
curl -X POST http://localhost:21847/api/v1/projects/<path_hash>/reindex \
  -H "Authorization: Bearer $CIX_API_KEY"
```

Use this after a manual content update, after the embedding model
changes, or after the stale-FTS warning (see [Search algorithm](#search-algorithm)).

### Unlinking a project from a workspace

Removes the membership row but leaves the underlying project (and any
clone on disk) intact, so the same project can be re-linked or remain
linked to other workspaces.

```bash
curl -X DELETE http://localhost:21847/api/v1/workspaces/<wid>/projects/<path_hash> \
  -H "Authorization: Bearer $CIX_API_KEY"
```

From the CLI: `cix ws "<name>" remove <project>` (addressed by path,
host_path, or path_hash).

### Deleting a project entirely

Removes the `projects` row along with its `git_repos` peer (if any),
the on-disk clone, the vectors, and — via `ON DELETE CASCADE` — every
workspace membership referencing it.

```bash
curl -X DELETE http://localhost:21847/api/v1/projects/<host_path-or-hash> \
  -H "Authorization: Bearer $CIX_API_KEY"
```

---

## GitHub tokens

### Why store tokens

Three uses, all server-side:

1. **Clone private repositories** during the add-repo flow.
2. **List your accessible orgs and repos** so the dashboard's
   add-repo dialog can show a picker instead of asking for raw URLs.
3. **Auto-register push webhooks** on GitHub so the server can rebuild
   the index when the upstream changes.

### Storage and encryption

- Tokens are stored in the `github_tokens` table.
- The plaintext is **AES-256-GCM-encrypted** before insert via
  `internal/secrets/secrets.go`.
- Encryption key resolution order:
  1. `CIX_SECRET_KEY` (hex or base64 32 bytes)
  2. `CIX_SECRET_KEYFILE` (path to a 0o600+ file)
  3. Auto-generated at `<data-dir>/.secret_key` (mode 0600) on first
     boot
- The server **refuses to read tokens at startup** if encrypted rows
  exist but no key resolves. Losing the key means re-pasting every PAT.
- Plaintext is returned exactly once on `POST /github-tokens` and never
  again. The dashboard caches it in memory just long enough to show
  it to the user.

### Token lifecycle

| | |
|---|---|
| Create | `POST /api/v1/github-tokens` — validates scopes against GitHub `/user` endpoint, encrypts, stores. |
| List | `GET /api/v1/github-tokens` — metadata only (id, name, scopes, timestamps). |
| List accounts | `GET /api/v1/github-tokens/{id}/accounts` — PAT owner + visible orgs. |
| List repos | `GET /api/v1/github-tokens/{id}/repos?account=...` — up to 500. |
| Delete | `DELETE /api/v1/github-tokens/{id}` — revokes the metadata row. Does **not** revoke the PAT on GitHub itself. |

`last_used_at` updates on every successful decrypt (clone job, webhook
registration, repo listing).

### Recommended scopes

| Scope | Needed for |
|---|---|
| `repo` | Cloning private repos |
| `read:org` | Listing private org repos in the dashboard picker |
| `admin:repo_hook` | Auto-registering push webhooks (`webhook_mode=auto`) |

Use a separate PAT per token entry if you want easy revocation paths;
the server doesn't multiplex one PAT across users.

---

## Searching a workspace

Three surfaces, same backend.

### Dashboard

`/dashboard/workspaces/<id>` → **Search** button. Two-stage UI:

1. Type a query, hit Enter.
2. See the **projects panel** (which repos look relevant, with
   `project_score` + per-signal `bm25_score` and `dense_score`) plus
   the **chunks panel** (round-robin interleaved snippets, file:line +
   score).
3. Drill into a chunk to open the full file in the project's detail
   page.

### CLI

```bash
# List workspaces
cix ws
cix ws list --json

# Describe one workspace
cix ws platform
cix ws platform describe --json

# List repos in a workspace
cix ws platform list
cix ws platform repos --verbose

# Search
cix ws platform search "authentication middleware"
cix ws platform search "JWT validation" --top-projects 8 --top-chunks 30
cix ws platform search "feature flag rollout" --json
```

CLI flags:

- `--top-projects N` — surface up to N projects in the panel (1–50,
  default 10).
- `--top-chunks K` — return up to K chunks total (1–200, default 20).
  Round-robin interleaved across surviving projects.
- `--json` — raw response, machine-readable.
- `-v` / `--verbose` — extra columns in the human-readable output.

### REST API

```bash
curl -G -H "Authorization: Bearer $CIX_API_KEY" \
  --data-urlencode "q=cross-project query here" \
  --data-urlencode "top_projects=10" \
  --data-urlencode "top_chunks=20" \
  --data-urlencode "min_score=0.4" \
  http://localhost:21847/api/v1/workspaces/<wid>/search
```

Response shape (abbreviated):

```jsonc
{
  "status": "ok",                  // "ok" | "empty" | "partial_failure"
  "projects": [
    {
      "project_path": "github.com/acme/api-server@main",
      "label": "api-server@main",
      "project_score": 0.87,        // blended candidacy [0,1]
      "bm25_score": 6.42,           // per-signal aggregate (raw)
      "dense_score": 0.54,
      "num_hits": 5
    },
    ...
  ],
  "chunks": [
    {
      "project_path": "github.com/acme/api-server@main",
      "file_path": "internal/auth/middleware.go",
      "start_line": 42,
      "end_line": 58,
      "symbol_name": "RequireAuth",
      "language": "go",
      "score": 0.61,                // cosine; 0 for BM25-only matches
      "content": "..."
    },
    ...
  ],
  "pending_repos":   [...],         // projects still in created / indexing
  "failed_repos":    [...],         // projects in error status
  "stale_fts_repos": [...]          // pre-FTS-mirror repos — reindex
}
```

---

## Search algorithm

Workspace search is **hybrid BM25 + dense**, fanned out per-project and
fused across projects. The full implementation lives in
[`server/internal/httpapi/workspacesearch.go`](server/internal/httpapi/workspacesearch.go).

### Pipeline

```
query
  │
  ├──► EmbedQuery (llama.cpp sidecar)
  │
  ▼
For every indexed workspace repo, in parallel:
  │
  ├── dense path: chromem.Search(query_embedding) → top-50 by cosine
  ├── sparse path: SQLite FTS5 BM25 over chunks_fts → top-50 by BM25
  │
  ▼
  Per-project RRF fusion (k=60) → single ranked chunk list per project
  Per-project signal aggregates: mean of top-5 dense, mean of top-5 BM25
  │
  ▼
Across-project candidacy:
  - Per-signal min-max normalize over all projects
  - candidacy = α · bm25_norm + (1 − α) · dense_norm    (α = 0.5)
  - Drop projects below `0.4 × best_candidacy`           (project gate)
  │
  ▼
Build projects panel (top N by candidacy)
Round-robin interleave chunks across surviving projects (per-project cap = 5)
Return projects[] + chunks[]
```

### Tunable parameters

All defaults are calibrated on a 113-query eval (33 identifier + 33
conceptual + 22 mixed + 25 cross-project). Source:
`workspacesearch.go:43-52`.

| Constant | Value | Meaning |
|---|---|---|
| `workspaceSearchPerProjectLimit` | 50 | Per-side retrieval depth per project |
| `workspaceSearchBM25Limit` | 50 | Same for BM25 side |
| `workspaceSearchTopNPerProject` | 5 | Top-N hits feeding per-signal aggregate |
| `workspaceSearchTopProjects` | 10 | Default panel size (1–50 via param) |
| `workspaceSearchPerProjChunkCap` | 5 | Max chunks from one project in chunks[] |
| `workspaceSearchAlpha` | 0.5 | BM25 weight in candidacy blend |
| `workspaceSearchProjThreshold` | 0.4 | Relative gate: candidacy ≥ best × 0.4 |
| `rrfK` | 60 | Reciprocal Rank Fusion constant (Cormack 2009) |

Request-time:

| Param | Default | Range |
|---|---|---|
| `top_projects` | 10 | 1–50 |
| `top_chunks` | 20 | 1–200 |
| `min_score` | **0.4** | 0–1 |

### `min_score` semantics

- **Default `0.4`** — matches per-project search default. Filters
  weak-cosine chunks before they enter the per-signal aggregate.
  Calibrated on the eval: 91–99% of false positives are eliminated by
  this floor.
- **Pass `0` explicitly** for intentional cross-workspace sweeps where
  long-tail recall matters more than precision (broad queries like
  "authentication and authorization" that legitimately span many repos).
- **Higher values (0.5+)** when you want laser-focused results and can
  tolerate occasional recall misses.

### Why hybrid

The pre-hybrid algorithm (pure dense fan-out) had a known failure mode:
chromem always returns the nearest K vectors regardless of how far
"nearest" actually is. A workspace with repos that share zero
vocabulary with the query still surfaced 50 chunks per repo at
noise-level cosine. BM25 fixes this: a repo that scores 0 on the
literal token side gets caught by the relative project gate even if
dense is mildly positive.

The asymmetric blend (α = 0.5) was tuned on the eval to balance two
opposite failure modes: pure BM25 over-favors literal-token matches
(misses semantic similarity); pure dense over-favors common-domain
vocabulary (false friends across unrelated repos).

### Pre-FTS repos

If a workspace repo was indexed before the chunks_fts mirror existed,
BM25 will be permanently 0 for it. The response surfaces this via
`stale_fts_repos: [{project_path: "..."}]`. Run a reindex on each:

```bash
curl -X POST http://localhost:21847/api/v1/projects/<path_hash>/reindex \
  -H "Authorization: Bearer $CIX_API_KEY"
```

---

## Webhooks (auto-reindex on push)

Each workspace repo has a `webhook_mode`:

- **`disabled`** — server never reindexes automatically. Triggered
  reindex via the API only.
- **`manual`** — server generates a `webhook_secret` and exposes a
  delivery URL. Configure the webhook on GitHub yourself. Pushes to
  the tracked branch trigger reindex.
- **`auto`** — server calls the GitHub API to **create the webhook**
  on the repo using your stored PAT. Requires `CIX_PUBLIC_URL` set
  and a token with `admin:repo_hook` scope. Best-effort: failure to
  register doesn't block the add-repo flow but sets a warning.

### Delivery endpoint

```
POST /api/v1/webhooks/github/{path_hash}
```

The `{path_hash}` segment is the same 16-hex-char value returned by
`POST /git-repos` and surfaced on every per-project endpoint. GitHub's
payload is HMAC-SHA256-signed with the matching `git_repos.webhook_secret`;
the server verifies via the `X-Hub-Signature-256` header.

Event handling:

| Event | Action |
|---|---|
| `push` (ref matches tracked branch) | Enqueue clone+index (dedupes burst pushes) |
| `push` (other ref) | Ignored |
| `ping` | 200 with no-op |
| Anything else | Ignored |

### Manual webhook setup

When `webhook_mode=manual`, the add-repo response includes a
`webhook_secret` (returned once) and a `webhook_url` (always
returnable). Configure on GitHub:

- **Payload URL:** `<CIX_PUBLIC_URL>/api/v1/webhooks/github/<path_hash>`
- **Content type:** `application/json`
- **Secret:** the returned `webhook_secret`
- **Events:** Just `push` (the server ignores everything else)

---

## Strengths and weaknesses

Honest assessment from the calibration eval and 5 real engineering tasks.

### Strengths

- **Hybrid signal is robust.** Identifier-style queries hit ~91% top-1
  precision; conceptual queries ~70%; mixed (identifier + concept)
  ~96%. BM25 catches what dense misses and vice versa.
- **Project gate works.** The relative `0.4 × best` threshold filtered
  zero true positives across 88 single-target queries — projects with
  no shared vocabulary AND marginal semantic similarity fall out
  cleanly.
- **Round-robin + chunk cap prevents domination.** No single repo
  monopolizes the chunks panel even when its scores dwarf the rest.
- **Per-project drill-down is fast.** Once you know which repo to dig
  into, switching to a per-project search returns deeper, file-grouped
  results without the cross-project interleave overhead.
- **Index is incremental.** Webhook-driven re-indexes only re-embed
  changed files (SHA-256 file hashes).

### Weaknesses

- **Top-1 is ~70% on real tasks, not 91%.** The synthetic eval framed
  each query as single-target by construction. Real tasks often have a
  "right repo for the words" that's different from the "right repo for
  the change". When the task is action-oriented (modify / configure /
  deploy), the manifests / config / contracts repo is often the right
  target even when a code repo ranks higher.
- **Token-frequency bias.** A repo that mentions your query terms
  dozens of times (in comments, tests, fixtures) outranks a repo
  where the canonical implementation lives but uses different
  vocabulary. The path-aware preamble in indexing helps, but doesn't
  fully neutralize this.
- **`chunk.score=0` is misleading.** Chunks matched via BM25 only
  (no dense overlap) report `score=0` in the response. Agents and
  UIs that read the score field as "confidence" can wrongly discard
  valid literal-match hits. Inspect `projects[].bm25_score` to know
  whether the project survived on literal-token strength.
- **Disambiguator backfire.** Adding a third query word to discriminate
  between overloaded terms can rotate the ranking away from the right
  answer if the added word belongs to the wrong stack (e.g. naming a
  protocol your target repo doesn't use). Prefer meta-tokens
  (`endpoint`, `route`, `handler`, `manifest`) over guesses at the
  underlying technology.
- **Index gaps.** The indexer skips files outside its language
  allow-list and files larger than `CIX_MAX_FILE_SIZE`. If your
  workspace contains a language the indexer doesn't recognize, those
  files contribute zero to either BM25 or dense — search will look
  past that repo entirely. Check `cix summary` per repo if results
  look thin.
- **No multi-tenancy.** Anyone authenticated can read every workspace
  in the deployment. Don't share a single cix-server across teams who
  shouldn't see each other's code.
- **Stale-FTS repos.** Workspaces created before the FTS5 mirror
  existed don't have BM25 data. The response flags them in
  `stale_fts_repos`; you must reindex to repair.
- **Clone storage grows.** Each workspace repo is a full `git clone`.
  Plan for several hundred MB to several GB per repo depending on
  history size. `git gc` is not automated; the cleanest reset is
  remove + re-add.

### When to use vs when not to

Use a workspace when:

- The task plausibly spans 2+ repos and you need to know which.
- You want an agent to find cross-cutting code (e.g. event flows,
  API contracts mirrored across services) without N separate searches.
- You're onboarding to an unfamiliar codebase and need to see what's
  where before diving in.

Don't use a workspace when:

- The task is fully contained in one repo. `cix search` from inside
  that repo is faster and more precise.
- You're looking for an exact symbol or file path. `cix definitions
  <name>` or `cix files <pattern>` against the project directly skips
  the cross-project interleave.
- The repos truly share no vocabulary. The project gate will collapse
  the response to one repo anyway — search there directly.

---

## Configuration reference

### Server environment variables

| Variable | Default | Description |
|---|---|---|
| `CIX_WORKSPACES_ENABLED` | `false` | **Required** to enable the feature. Restart after change. |
| `CIX_WORKSPACES_DATA_DIR` | `<data-dir>/repos` | Where workspace repo clones live on disk. |
| `CIX_PUBLIC_URL` | — | Public origin used to build webhook delivery URLs. Required for `webhook_mode=auto`. |
| `CIX_SECRET_KEY` | — | 32-byte hex/base64 key for at-rest encryption of GitHub tokens. Falls back to a keyfile or auto-generated key. |
| `CIX_SECRET_KEYFILE` | — | Path to an alternative key source (file with mode ≤ 0600). |
| `CIX_WORKER_CONCURRENCY` | `2` | Parallelism for clone + index workers. |

### CLI configuration

`cix ws` reuses the standard `~/.cix/config.yaml` — no extra setup
needed beyond `api.url` and `api.key`.

---

## REST API reference

All endpoints require `Authorization: Bearer <cix_*>` or a valid cookie
session. All return `503` if `CIX_WORKSPACES_ENABLED=false`.

### Workspaces

```
GET    /api/v1/workspaces                       list
POST   /api/v1/workspaces                       create (body: {name, description})
GET    /api/v1/workspaces/{id}                  detail
PATCH  /api/v1/workspaces/{id}                  rename / update description
DELETE /api/v1/workspaces/{id}                  delete (removes the workspace + membership links only; projects, git_repos, and clones are untouched)
```

### Workspace project membership

```
GET    /api/v1/workspaces/{id}/projects                         list projects linked to this workspace
POST   /api/v1/workspaces/{id}/projects                         link an existing indexed project (body: {path_hash})
DELETE /api/v1/workspaces/{id}/projects/{hash}                  unlink (project + clone preserved)
```

### Projects (per-project, workspace-independent)

```
POST   /api/v1/git-repos                                        register a new GitHub-cloned project (clones + indexes)
GET    /api/v1/projects/{hash}/git-repo                         git_repos peer of a project (404 for local-path projects)
POST   /api/v1/projects/{hash}/reindex                          trigger a fresh index
GET    /api/v1/projects/{hash}/webhook-info                     dashboard helper — current webhook URL + secret
DELETE /api/v1/projects/{path}                                  delete project + clone + memberships (CASCADE)
```

### Workspace search

```
GET    /api/v1/workspaces/{id}/search?q=...&top_projects=10&top_chunks=20&min_score=0.4
```

See [§ Searching a workspace](#searching-a-workspace) for response shape.

### GitHub tokens

```
GET    /api/v1/github-tokens                                    list (metadata only)
POST   /api/v1/github-tokens                                    create (returns plaintext once)
GET    /api/v1/github-tokens/{id}/accounts                      PAT owner + orgs
GET    /api/v1/github-tokens/{id}/repos?account=...             repos visible to PAT
DELETE /api/v1/github-tokens/{id}                               revoke (server-side only)
```

### Webhooks

```
POST   /api/v1/webhooks/github/{hash}                           GitHub delivery endpoint (HMAC-verified)
```

Full OpenAPI: `doc/openapi.yaml` and `http://<host>:21847/docs`.

---

## Troubleshooting

**`503 workspaces feature is disabled`**
→ `CIX_WORKSPACES_ENABLED=true` is missing or the server hasn't been
restarted.

**`status: "error"` on a project, dashboard surfaces "authentication required"**
→ Private repo with no token, or token's scopes are insufficient.
Re-create the token with `repo` scope (and `admin:repo_hook` if you
want auto webhooks), then retry by deleting and re-adding the project.

**`status: "error"`, dashboard surfaces "branch not found"**
→ Typo or the branch was deleted upstream. Delete the project and
re-add it via `POST /git-repos` with the correct branch.

**Search returns `empty` for a query that should match**
→ Three likely causes:
1. Default `min_score=0.4` filtered everything. Retry with `min_score=0`.
2. Project is still indexing (`status: created|indexing`). Check
   `GET /workspaces/{id}/projects`.
3. The literal terms genuinely don't appear in any repo AND dense
   similarity is below threshold. Re-phrase with the term the code
   actually uses.

**`stale_fts_repos` populated on every search**
→ These repos were indexed pre-FTS5 mirror. Run
`POST /api/v1/projects/{hash}/reindex` on each.

**`status: "partial_failure"`**
→ At least one repo's dense search errored (corrupt chromem collection,
disk pressure). Other repos still returned. Check server logs; the
fastest fix is usually a reindex of the failed repo.

**Webhook isn't triggering reindex**
→ Verify:
1. GitHub's webhook deliveries page shows 200 OK.
2. Push was to the *tracked* branch (the one in `git_repos.branch`).
3. Server logs show signature verification succeeding.
4. `CIX_PUBLIC_URL` is set and reachable from GitHub (for `auto` mode).

**Token gone after a restart, all repos failing**
→ The encryption key resolved differently. Common cause: switched from
auto-generated `<data-dir>/.secret_key` to `CIX_SECRET_KEY` env var
without copying the original key value. Either restore the key or
re-create every token entry.

---

## Agent integration

The `cix-workspace` skill teaches AI agents how to use workspace search
responsibly — when to fan out, how to read the `projects[]` panel, how
to interpret `score=0` hits, how to spawn parallel per-repo investigators.
A bundled `cix-workspace-investigator` sub-agent handles the per-repo
deep dive in isolated context.

**This skill is manual-only by design.** It does **not** auto-trigger
on cross-cutting prompts — you invoke it deliberately when the request
genuinely spans multiple repos. The workspace flow is heavier than
single-repo `cix search` (multi-repo fan-out, sub-agent spawns) and
only pays off when you've made the call that cross-project research
is what you actually need. This policy may relax once the
"is-this-really-cross-project" heuristics are more reliable.

### Install — Claude Code plugin (recommended)

The skill and sub-agent ship with the `cix` Claude Code plugin (v0.2.0+).
Marketplace install:

```
/plugin marketplace add dvcdsys/code-index
/plugin install cix@code-index
/reload-plugins   # or restart Claude Code
```

### Install — manual cp-r (legacy)

```bash
cp -r skills/cix-workspace ~/.claude/skills/cix-workspace
mkdir -p ~/.claude/agents
cp skills/cix-workspace/agents/cix-workspace-investigator.md ~/.claude/agents/
```

### Invoke

In any Claude Code session — type `/cix-workspace` followed by the
task verbatim:

```
/cix-workspace add a new rate-limit middleware and wire it through
the gateway, the backend, and the deployment manifests
```

The skill loads the cross-project workflow, the agent runs workspace
search, identifies the relevant repos, and spawns
`cix-workspace-investigator` sub-agents in parallel — one per repo —
to do the deep dive without bloating the main session's context.

---

## Roadmap

Known direction:

- **`project_kind` enum in `projects[]`.** Surface whether each project
  is `code` / `manifests` / `contracts` / `docs` so agents can reason
  about the "words vs change location" mismatch noted above.
- **Auto-detect stale indexes.** Today reindex is manual; the server
  should detect when a repo's vectors are incompatible with the current
  embedding model and prompt automatically.
- **Broader language coverage in the indexer.** Expand the
  `CIX_LANGUAGES` allow-list to cover more domain-specific file types,
  and raise the file-size cap for prose-heavy docs.

Track open issues at [github.com/dvcdsys/code-index/issues](https://github.com/dvcdsys/code-index/issues).
