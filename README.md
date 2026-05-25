[![CI: Server](https://github.com/dvcdsys/code-index/actions/workflows/ci-server.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/ci-server.yml)
[![CI: CLI](https://github.com/dvcdsys/code-index/actions/workflows/ci-cli.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/ci-cli.yml)
[![CodeQL](https://github.com/dvcdsys/code-index/actions/workflows/codeql.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/codeql.yml)
[![Security](https://github.com/dvcdsys/code-index/actions/workflows/security.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/security.yml)

```
 ██████╗██╗██╗  ██╗
██╔════╝██║╚██╗██╔╝
██║     ██║ ╚███╔╝
██║     ██║ ██╔██╗
╚██████╗██║██╔╝ ██╗
 ╚═════╝╚═╝╚═╝  ╚═╝  Code IndeX
```

[![Release: Server](https://github.com/dvcdsys/code-index/actions/workflows/release-server.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/release-server.yml)
[![Release: CLI](https://github.com/dvcdsys/code-index/actions/workflows/release-cli.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/release-cli.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Hub](https://img.shields.io/docker/pulls/dvcdsys/code-index)](https://hub.docker.com/r/dvcdsys/code-index)

Search your codebase by meaning, not just text. Self-hosted, embeddings-based, works with any agent or terminal — with a full web dashboard and multi-repo workspace search.

```bash
cix search "authentication middleware"
cix search "database retry logic" --in ./api --lang go
cix symbols "UserService" --kind class
```

Or open `http://localhost:21847/dashboard` in your browser.

---

## Why

Grep and fuzzy file search work fine for small projects. At scale they break down:

- You have to know what a thing is called to find it
- Results flood with noise from unrelated files
- Agents waste tokens scanning files that aren't relevant

`cix` indexes your code into a vector store using [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) — a model purpose-built for code retrieval. Search queries return ranked snippets with file paths and line numbers, not raw file lists.

---

## What you get

- **`cix-server`** — Go HTTP API with embedded llama.cpp sidecar for embeddings, SQLite for symbols + project metadata, chromem-go for vectors, FTS5 BM25 mirror for keyword + hybrid ranking. Ships as a single distroless container.
- **Web dashboard** at `/dashboard` — projects, semantic search, user + API-key management, runtime sidecar control, drift indicator, release-update banner, dashboard-driven reindex. Embedded directly in the server binary.
- **`cix` CLI** — drop-in `cix search`/`cix symbols`/`cix files`/`cix workspace …` commands for terminal + agent use.
- **File watcher** — `cix watch` keeps the index fresh as you edit, no manual reindex.
- **Workspaces** — group multiple repositories into a named workspace; cix clones them server-side via a stored GitHub PAT, indexes them with the same pipeline, and runs hybrid BM25 + dense search across the union. GitHub webhooks auto-reindex on `push`. See [`workspaces.md`](workspaces.md) and [`doc/WORKSPACES.md`](doc/WORKSPACES.md).
- **Ownership + view-group sharing** — every project and workspace has an owner; admins manage *view-groups* and grant per-resource shares. Private by default; external (GitHub-cloned) projects are admin-administered. CLI / dashboard surfaces only what the caller is allowed to see.
- **Managed Tunnels** — server-orchestrated Cloudflare Tunnel or ngrok provides a public origin for GitHub webhook ingress from behind NAT. Configured + monitored from the dashboard's Managed Tunnels page; the agent binary auto-installs on demand.
- **Git polling sync** — repos where the user isn't an admin and can't install a webhook can opt into polling instead. Cadence is per-repo, measured from the end of the last index run.
- **Claude Code plugin** (v0.2.0+) — install once and `cix` becomes the agent's default reflex for code search. Bundles two skills (`cix`, `cix-workspace`) and a fan-out sub-agent. See [Agent integration](#agent-integration).
- **OpenAPI as source of truth** — Go server interface + TypeScript dashboard types are generated from `doc/openapi.yaml`. Swagger UI at `/docs`.

---

## Architecture

```
                  ┌────────────────────────────────────┐
                  │  Browser  →  http://host:21847     │
                  │           ─────────────────────────│
                  │  • /dashboard   React SPA          │
                  │  • /docs        Swagger UI         │
                  │  • /openapi.json                   │
                  └────────────┬───────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│  cix-server (Go, single distroless binary)                      │
├─────────────────────────────────────────────────────────────────┤
│  HTTP/REST + cookie sessions + Bearer API keys                  │
│  ├── auth, admin, api-keys, projects, indexing, search          │
│  ├── workspaces, github-tokens, webhooks                        │
│  ├── embedded React dashboard (//go:embed all:dist)             │
│  └── embedded Swagger UI                                        │
│                                                                 │
│  Indexing pipeline                                              │
│  ├── gotreesitter (AST chunking, 200+ languages)                │
│  ├── llama-server sidecar (Unix socket → CodeRankEmbed Q8 GGUF) │
│  ├── chromem-go (cosine similarity vector store)                │
│  ├── SQLite FTS5 chunk mirror (BM25 — powers hybrid workspace)  │
│  └── modernc.org/sqlite (projects, symbols, file hashes)        │
└────────────┬─────────────────────────────────────┬──────────────┘
             │ HTTP                                │ Unix socket
             ▼                                     ▼
   cix CLI (Go) — search,           ┌──────────────────────────┐
   symbols, files, init,            │  llama-server child proc │
   reindex, watch, workspace        │  (llama.cpp embeddings)  │
                                    └──────────────────────────┘
```

The server is a pure-Go static binary; CUDA-image variants add a CUDA runtime layer for GPU embeddings. Workspace clones live in `<data-dir>/repos/`.

---

## Quick Start

Three deployment modes:

| Mode | Best for | GPU | Prerequisites |
|------|----------|-----|---------------|
| **Docker (CPU)** | any OS, dev / small repos | none | Docker |
| **Docker (CUDA)** | NVIDIA GPU servers | CUDA 12.x | Docker + NVIDIA Container Toolkit |
| **Native (macOS)** | Apple Silicon w/ full Metal | Metal | Go 1.25+, Xcode CLT |

### 1. Start the server

**Docker (CPU):**

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
cp .env.example .env
# Edit .env — set CIX_API_KEY, CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
docker compose up -d
curl http://localhost:21847/health   # → {"status":"ok"}
```

> [!IMPORTANT]
> On a fresh database the server **refuses to start** unless both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` are set. The user is created with `must_change_password=true`, so the temporary password only works for the first login. After first login you can drop the env vars from `.env`.

**Docker (CUDA — NVIDIA GPU):**

```bash
docker compose -f docker-compose.cuda.yml up -d
```

See [GPU Acceleration (CUDA)](#gpu-acceleration-cuda) for host requirements.

**Native macOS (Apple Silicon — Metal GPU):**

Docker Desktop on macOS runs containers in a Linux VM with no Metal access — for full GPU acceleration on Apple Silicon, run natively.

```bash
xcode-select --install                # if not installed
cd server && make bundle              # builds cix-server + downloads Metal-enabled llama-server
cp .env.example .env
# Set CIX_API_KEY, CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
# Set CIX_N_GPU_LAYERS=99 for full Metal offload
cd server && make run
```

For a launchd auto-start setup and the full env-var checklist, see [`doc/SETUP_MACOS_NATIVE.md`](doc/SETUP_MACOS_NATIVE.md).

### 2. Log in to the dashboard

Open http://localhost:21847/dashboard and sign in with the bootstrap admin email + password. You'll be forced to change the password on first login. See [Dashboard](#dashboard) for what's on each page.

### 3. Install the CLI

**One-line installer (macOS / Linux):**

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
```

For a pre-release build from `develop`, use `install-develop.sh` instead — see [`doc/UPDATES.md`](doc/UPDATES.md#cli-install-channels). Not for production.

**From source:**

```bash
cd cli && make build && make install   # → /usr/local/bin/cix
```

### 4. Configure the CLI

```bash
cix config set api.url http://localhost:21847
cix config set api.key $(grep CIX_API_KEY .env | cut -d= -f2)
```

Or mint a fresh API key from the dashboard's **API Keys** page.

### 5. Index a project and search

```bash
cd /path/to/your/project
cix init                            # registers + indexes + starts the file watcher
cix status                          # wait for: Status: ✓ Indexed

cix search "authentication middleware"
cix search "error handling" --in ./api
cix symbols "handleRequest" --kind function
cix files "config"
cix summary
```

…or use the dashboard's **Search** page for the same five modes.

---

## Dashboard

The dashboard ships embedded in the server binary at `/dashboard`. No extra service to run, no nginx config, no separate static-files volume.

| Page | Audience | What it does |
|------|----------|--------------|
| **Home** | everyone | Live status strip (server version, current embedding model, sidecar Ready/Loading), update-available banner when a newer `server/v*` release is published on GitHub, module shortcuts. |
| **Projects** | everyone | List indexed projects with stats (file count, languages, symbols, vector count, sqlite/chroma sizes), per-project **Reindex** button + live indexing indicator, copy reindex commands. Cards turn red with a **Stale model** badge when the runtime embedding model differs from the model the project was indexed with (see [Drift indicator](#drift-indicator)). |
| **Workspaces** | everyone | Group multiple repositories into a named workspace and search them as one corpus. The in-dashboard add-repo flow streams clone + index progress live; pick the org/account first, then the repo. Status tracking: `pending` → `cloning` → `indexing` → `indexed` / `failed`. Hybrid BM25 + dense search across the whole group. See [`workspaces.md`](workspaces.md). |
| **Search** | everyone | Five modes: semantic, symbols, references, definitions, files. Same engine the CLI uses. |
| **API Keys** | everyone | Mint long-lived `cix_*` keys (256-bit entropy, GitHub-class), copy them once, revoke at any time. Keys inherit the issuing user's role. |
| **GitHub Tokens** | admin | Store personal access tokens used by external (cloned) projects + workspaces. Tokens are AES-256-GCM encrypted at rest; the plaintext is returned once on creation and never again. Scopes are **derived from GitHub** at storage time (not user-declared), so the dashboard shows the PAT's true capabilities. |
| **Users** | admin | Invite teammates, set role (admin / user), reset password (forces change on next login), disable account. |
| **Groups** | admin | Manage *view-groups* — named user sets used to share projects and workspaces with specific people. Add/remove members, grant shares from the project or workspace detail page. |
| **Managed Tunnels** | admin | Enable a Cloudflare Tunnel or ngrok tunnel to give the server a public origin for GitHub webhook ingress from behind NAT. Configure provider, mode (quick / named), and credentials; agent binary auto-installs on demand; live status + restart + round-trip test. |
| **Settings** | everyone | Theme, default editor, change own password. |
| **Server** | admin | Runtime config — embedding model, `n_ctx`, `n_gpu_layers`, `n_threads`, batch size, queue concurrency. **Save & Restart** drains in-flight embeddings, restarts the sidecar, polls until ready. Source pill on each field shows whether the live value comes from the DB override, env bootstrap, or the recommended fallback. |

### Authentication

Two paths share the same identity model:

- **Cookie session** (browser) — `cix_session` HttpOnly cookie, 14-day rolling TTL, `sha256(token)` stored in DB. The raw token never leaves the browser.
- **Bearer API key** (CLI / agents / CI) — `Authorization: Bearer cix_<43-char-base64url>` header. 256 bits of entropy, hex-`sha256`-stored, scoped to the issuing user's role.

### Authorization model

Two roles: **`admin`** and **`user`**. On top of roles, every resource has explicit visibility:

- **Local projects** (indexed via `cix init` on a developer's machine) belong to the user who ran the init and are private to that user. Project identity is per-machine — the same path on two different machines never collides.
- **External projects** (cloned by the server from GitHub) are *ownerless* and admin-administered. They become visible to others only through a **view-group share**.
- **Workspaces** are owned by their creator; sharing works the same way as projects.
- **View-groups** are admin-managed named user sets. Grant a group a share on a project or workspace from its detail page; every group member then sees it as if they owned it (read-only). Admins always see everything.

Every endpoint enforces this model server-side — the dashboard hides controls the caller isn't allowed to use, and the CLI surfaces a 404 (not a 403) when probing a resource the caller has no business knowing exists.

### Drift indicator

When you change the runtime embedding model (Server → Embedding model → Save & Restart), every project indexed with the previous model becomes stale — vectors are no longer comparable to fresh queries. The dashboard surfaces this with red borders + `Stale model` badges on project cards, and a banner on the project detail page with a copy-to-clipboard `cix reindex --full <path>` command. After running the reindex, the drift signal clears automatically.

### Disabled-embeddings mode

Set `CIX_EMBEDDINGS_ENABLED=false` to bring the server up without the llama-server sidecar — auth, dashboard, project metadata, and symbol / file searches all keep working; only semantic search and indexing are disabled. The Server page renders a warning banner and disables the relevant inputs.

### Workspaces

The **Workspaces** page groups repositories into one named workspace and searches them as a single corpus — useful for tasks that span microservices, infra-as-code, API specs, and the like. Unlike `cix init` (which indexes the project you're `cd`'d into), workspaces track repositories that the server itself clones (or local projects that you link in).

See [`workspaces.md`](workspaces.md) for the user-facing workflow and [`doc/WORKSPACES.md`](doc/WORKSPACES.md) for operator setup (encryption keys, Cloudflare/ngrok tunnel, webhook + polling sync modes, REST API). The hybrid-search algorithm lives in [`doc/SEARCH_ALGORITHM.md`](doc/SEARCH_ALGORITHM.md); the webhook lifecycle in [`doc/WEBHOOKS.md`](doc/WEBHOOKS.md).

---

## CLI Reference

### Project management

| Command | Description |
|---------|-------------|
| `cix init [path]` | Register + index + start file watcher |
| `cix status` | Show indexing status and progress |
| `cix list` | List all indexed projects |
| `cix reindex [--full]` | Trigger manual reindex |
| `cix cancel` | Cancel an in-flight indexing run |
| `cix summary` | Project overview: languages, directories, symbols |

### Search

```bash
# Semantic search — natural language, finds by meaning
cix search <query> [flags]
  --in <path>          restrict to file or directory (repeatable)
  --exclude <path>     exclude file or directory (repeatable)
  --lang <language>    filter by language (repeatable)
  --limit, -l <n>      max results (default: 10)
  --min-score <0-1>    minimum relevance score (default: 0.4)
  -p <path>            project path (default: cwd)

# Symbol search — fast lookup by name
cix symbols <name> [flags]
  --kind <type>        function | class | method | type (repeatable)
  --limit, -l <n>      max results (default: 20)

# Definition / reference navigation
cix definitions <symbol> [--kind <type>] [--file <path>] [--limit <n>]
cix references <symbol> [--file <path>] [--limit <n>]

# File search by path pattern
cix files <pattern> [--limit <n>]
```

### Workspaces (cross-repo)

```bash
cix workspace list                                          # all workspaces
cix workspace "<name>"                                      # describe (default verb)
cix workspace "<name>" describe                             # same, explicit
cix workspace "<name>" repos                                # list repos in the workspace
cix workspace "<name>" search "<query>" [--limit <n>]       # hybrid BM25 + dense
```

The CLI uses a name-first grammar so an agent doesn't need to juggle workspace ids. See [`workspaces.md`](workspaces.md) for the agent contract.

### File watcher

```bash
cix watch [path]             # start background daemon
cix watch --foreground       # run in terminal (Ctrl+C to stop)
cix watch stop               # stop daemon
cix watch status             # check if running
```

The watcher monitors the project with `fsnotify`, debounces events (5 s default), and triggers incremental reindexing automatically. Logs: `~/.cix/logs/watcher.log`.

### Configuration

```bash
cix config show              # print current config
cix config set <key> <val>   # set a value
cix config path              # show config file location
```

Config file: `~/.cix/config.yaml`

| Key | Default | Description |
|-----|---------|-------------|
| `api.url` | `http://localhost:21847` | API server URL |
| `api.key` | — | Bearer token (`cix_*`) — required |
| `watcher.debounce_ms` | `5000` | Delay before reindex triggers after a file change |
| `indexing.batch_size` | `20` | Files per `/index/files` batch |

---

## Agent Integration

`cix` is designed to be called by AI agents (Claude, GPT, Cursor, custom agents) as a shell tool. Agents run `cix search` instead of Grep/Glob — getting ranked, relevant snippets rather than raw file dumps.

### Claude Code (recommended: install the plugin)

The **`cix` Claude Code plugin** (v0.2.0+) bundles the `cix` and `cix-workspace` skills, the `cix-workspace-investigator` sub-agent (parallel per-repo fan-out for cross-project research), CLI auto-install hooks, and a grep-nudge that suggests `cix search` when the agent reaches for Grep on an indexed project. Install from the marketplace:

```
/plugin marketplace add dvcdsys/code-index
/plugin install cix@code-index
/reload-plugins
```

See [`plugins/cix/README.md`](plugins/cix/README.md) for the full hook list and configuration knobs.

If you prefer manual install or aren't using the plugin system: `cp -r skills/cix ~/.claude/skills/cix`.

Then in any Claude Code session, invoke the skill **paired with the actual engineering task** — not a search query. The pattern is `/cix <fix / implement / investigate / refactor …>`:

```
/cix fix the watcher hanging on files >10MB and add a regression test
/cix implement rate limiting on /api/v1/webhook with the same limiter
    pattern as /auth/login
/cix investigate why semantic search returns zero hits on the security
    package after the last reindex
/cix refactor the embedding queue to use a ring buffer instead of slice
    grow-and-truncate
```

The slash command primes Claude with cix usage guidance; the task that follows is what Claude actually executes. Throughout the work, Claude reaches for `cix search` / `cix definitions` / `cix references` to navigate the codebase **as a tool inside the task**, not as the task itself. This is the right mental model: cix is the agent's IDE — `goto-def`, `find-refs`, "what calls this" — that lets it understand unfamiliar code before changing it.

For multi-repo work, invoke the workspace skill explicitly: `/cix-workspace <task>`. It's manual-only by design — it doesn't auto-trigger on cross-cutting prompts. See [`workspaces.md`](workspaces.md#agent-integration) for the agent contract.

To activate `cix` as the default reflex without typing `/cix` every time, add to `~/.claude/CLAUDE.md`:

```markdown
## Code search
Use `cix` for all code search instead of Grep/Glob:
- `cix search "query"` — semantic search by meaning
- `cix symbols "Name" --kind function` — find symbol definitions
- `cix files "pattern"` — find files by path
- `cix summary` — project overview
Run `cix init` on first use in a project.
```

(The plugin's session hooks do most of this automatically once installed.)

### Other agents

Same pattern — give the agent shell execution and describe the commands:

```
Tool: shell
Usage: cix search "what you're looking for" [--in ./subdir] [--lang python]
Returns: ranked code snippets with file paths and line numbers
```

### Typical agent workflow

```bash
cix init /path/to/project           # first time

cix summary                         # explore
cix search "main entry point"

cix search "JWT token validation"   # find specific code
cix symbols "ValidateToken" --kind function

cix references ValidateToken        # navigate
cix search "error handling in auth flow" --in ./api
```

---

## How indexing works

**Chunking** — tree-sitter parses code into semantic chunks (functions, classes, methods). Unsupported languages fall back to a sliding window (2000 chars, 256 char overlap). 30+ languages have AST extraction — see [`doc/LANGUAGES.md`](doc/LANGUAGES.md) for the full list.

**Embeddings** — each chunk is encoded with a GGUF build of CodeRankEmbed (default: [awhiteside/CodeRankEmbed-Q8_0-GGUF](https://huggingface.co/awhiteside/CodeRankEmbed-Q8_0-GGUF); 768d, 8192-token context, ~145 MB on disk) via the `llama-server` sidecar (llama.cpp). Queries get a `"Represent this query for searching relevant code: "` prefix for asymmetric retrieval.

**Path-aware preamble** — each chunk is embedded with its file path, language, and parent symbol prefixed. This makes "auth middleware" find `auth.go` even if the file content uses different vocabulary. Toggle with `CIX_EMBED_INCLUDE_PATH` (default `true`); changing it requires `cix reindex --full`.

**FTS5 / BM25 mirror** — every chunk also lands in a SQLite FTS5 virtual table indexed by trigram over `(content, symbol_name, file_path)`. Single-project search stays pure-dense; the BM25 mirror powers hybrid workspace search (acronym precision + project-level relevance gating). See [`doc/SEARCH_ALGORITHM.md`](doc/SEARCH_ALGORITHM.md).

**Incremental reindex** — uses SHA-256 file hashes. Only new or changed files are re-embedded. Deleted files are removed from the index.

**Filtering** — respects `.gitignore` and `.cixignore`, skips common dirs (`node_modules`, `.git`, `.venv`, etc.), skips files >`CIX_MAX_FILE_SIZE` (512 KiB default) and empty files. Per-project configuration via `.cixconfig.yaml` (see below).

---

## Tuning search quality

`cix` defaults to `--min-score 0.4`, calibrated for **CodeRankEmbed-Q8_0** with the path-aware embedding format. Typical score landscape on a real codebase:

| Match strength | Score range | Action |
|---|---|---|
| Exact symbol or filename match | 0.65 – 0.80 | rare; very high confidence |
| Strong path-aware concept match | 0.50 – 0.65 | typical "good" match |
| Weaker concept / partial path overlap | 0.40 – 0.50 | typical for ambiguous queries |
| Likely unrelated noise | < 0.40 | filtered out by default |

**When to lower the threshold:** sparse queries returning no results — try `--min-score 0.25`. Exploring an unfamiliar codebase — `--min-score 0.2`. Rare single-word identifiers.

**When to raise it:** agent context filling up with weak matches — `--min-score 0.5` or `0.6`.

CodeRankEmbed is asymmetric: queries and passages live in different regions of the embedding space, so cosine similarities are systematically lower than for symmetric models. Don't compare these numbers to thresholds quoted for OpenAI / Voyage / generic sentence-transformers. Full details — including hybrid workspace scoring — in [`doc/SEARCH_ALGORITHM.md`](doc/SEARCH_ALGORITHM.md).

For noisy directories (vendored code, fixtures, legacy migrations), `--exclude vendor --exclude bench/fixtures` works per-query, or add entries to `.cixignore` to skip them at indexing time.

---

## Per-project configuration

### `.cixignore` — exclude files from indexing

Works exactly like `.gitignore` (same syntax, same nesting rules). Patterns are merged with `.gitignore` — you don't need to duplicate rules. Use this for files you want excluded from the *index* that aren't already excluded from git (vendored code, generated files, large test fixtures):

```gitignore
# .cixignore
api/generated/
vendor/
*.pb.go
testdata/fixtures/
```

Nested `.cixignore` files work like nested `.gitignore`. The file watcher automatically triggers a full reindex when `.cixignore` is created, modified, or deleted.

### `.cixconfig.yaml` — project-level settings

Place in the project root:

```yaml
ignore:
  submodules: true   # automatically exclude all git submodule paths
```

When `ignore.submodules` is `true`, cix reads `.gitmodules` and excludes all submodule paths from indexing. No git binary required — the file is parsed directly. Useful for Foundry/Forge dependencies, vendored submodules, or any repo where submodules contain thousands of files you don't want indexed. The watcher triggers a full reindex when this file changes.

---

## Configuration

The most common environment variables:

| Variable | Default | Purpose |
|---|---|---|
| `CIX_API_KEY` | — | Bearer token for CLI/agents. Required. |
| `CIX_PORT` | `21847` | Listen port. |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` | — | Required on a fresh DB; promotes a first admin and forces password change on first login. |
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | GGUF repo or absolute path. |
| `CIX_N_GPU_LAYERS` | `-1` macOS / `0` else / `99` Docker CUDA | `99` = full offload, `0` = CPU. |
| `CIX_EMBEDDINGS_ENABLED` | `true` | `false` boots without the llama sidecar. |
| `CIX_VERSION_CHECK_ENABLED` | `true` | `false` disables the GitHub release-poll banner. |
| `CIX_SECRET_KEY` / `_KEYFILE` | auto-generated keyfile | AES-256-GCM key for `github_tokens` encryption at rest. Auto-generated under the SQLite parent dir if neither is set; **back this up** (regenerating it invalidates every stored PAT). |
| `CIX_REPOS_DIR` | `<sqlite-dir>/repos` | Where workspace-cloned GitHub repos live. Legacy alias: `CIX_WORKSPACES_DATA_DIR`. |
| `CIX_PUBLIC_URL` | — | Public origin used to build webhook URLs (e.g. `https://cix.example.com`). Trumped by a live Managed Tunnel when one is up. |
| `CIX_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |
| `CIX_DEFAULT_POLL_INTERVAL` | `5m` | Cadence for repos opted into polling sync (Go duration string). Floor `CIX_MIN_POLL_INTERVAL=60s`. |
| `CIX_TUNNEL_BIN_MANAGED` | `false` | Allow the server to auto-install Cloudflare / ngrok agent binaries on demand. |

Workspaces are always available — no feature gate. The full env-var surface (auth, storage, sidecar tuning, secret-key resolution, tunnel + polling knobs, runtime overrides) lives in [`doc/CONFIG_REFERENCE.md`](doc/CONFIG_REFERENCE.md). Anything in the "Tuning" group is editable at runtime from **Dashboard → Server**.

---

## REST API

The HTTP surface covers auth + sessions, users + API keys (admin), projects + indexing + search, workspaces + GitHub tokens, runtime config, and webhook reception. All endpoints except `/health`, `/api/v1/auth/login`, `/api/v1/auth/bootstrap-status`, `/dashboard/*`, `/docs`, and `/openapi.json` require authentication (Bearer API key *or* session cookie).

The full schema with request/response shapes lives in [`doc/openapi.yaml`](doc/openapi.yaml) and is browsable at `http://<host>:21847/docs` (Swagger UI). The Go server interface and the TypeScript dashboard types are generated from that one file.

---

## Troubleshooting

**Server refuses to start: `bootstrap auth: no users in database and the bootstrap admin env vars are not set`** → Set both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` in your `.env`, restart. Once you log in and change the password, you can drop the env vars (the user lives in the DB).

**`API key not set` from CLI**
```bash
cix config set api.key $(grep CIX_API_KEY /path/to/code-index/.env | cut -d= -f2)
# or mint a fresh one in the dashboard's API Keys page
```

**`connection refused`**
```bash
curl http://localhost:21847/health                    # is the server up?
docker compose up -d                                  # start (CPU)
docker compose -f docker-compose.cuda.yml up -d       # start (CUDA)
```

**`project not found`** — run `cix init /path/to/project`.

**Watcher not triggering reindex**
```bash
cix watch status
cat ~/.cix/logs/watcher.log
cix watch stop && cix watch /path/to/project
```

**Search returns no results**
- Check the project is indexed: `cix status`
- Lower the threshold: `cix search "query" --min-score 0.2` (default `0.4`)
- `cix list` to verify the project is registered

**Dashboard shows "Stale model" on every project after upgrade** → The runtime model was changed (or its version stamp shifted). Either reindex affected projects (`cix reindex --full` per project) or revert the model change in **Server → Embedding model**.

**Dashboard banner says an update is available** → A newer `server/v*` release is on GitHub. Click through to the release notes; bump your Docker tag / native build at a convenient time. Disable the poll with `CIX_VERSION_CHECK_ENABLED=false` if you don't want it. See [`doc/UPDATES.md`](doc/UPDATES.md).

**Workspace repo stuck in `cloning` or `indexing`** → Check **Workspaces → Jobs** in the dashboard or `GET /api/v1/jobs?status=running`. Common causes: PAT missing `repo` scope on a private repo, network not reaching github.com, sidecar not ready. See [`doc/WORKSPACES.md`](doc/WORKSPACES.md#troubleshooting).

**Forgot the admin password and there's no second admin** → See `doc/SECURITY_DEPLOYMENT.md`. Better long-term: keep at least two admin accounts so this never recurs.

---

## GPU Acceleration (CUDA)

A CUDA-enabled image is available for servers with NVIDIA GPUs. Inference runs on GPU automatically — no configuration needed.

### VRAM Usage (CodeRankEmbed Q8_0 GGUF, RTX 3090)

With the GGUF backend the footprint is near-constant: weights (~200–250 MB) plus the pre-allocated context (`n_ctx=8192`, ~200–400 MB) give a **~0.5–0.7 GB** idle draw. Embedding calls do not spike VRAM the way fp16 PyTorch attention used to — sequence length and batch size only change latency, not peak memory.

`CIX_MAX_CHUNK_TOKENS` still caps the length of each code chunk (1 token ≈ 4 chars) and must stay ≤ `CIX_LLAMA_CTX` (8192). `CIX_MAX_EMBEDDING_CONCURRENCY` defaults to `5` — the indexing queue ships chunks in parallel; the llama-server sidecar still serialises requests through one context, but pipelining host-side prep with device inference at this depth saturates the GPU without measurable latency cost. Drop to `1` only if you observe contention.

See [`doc/vram-profiling.md`](doc/vram-profiling.md) for methodology and numbers, and [`doc/benchmarks.md`](doc/benchmarks.md) for the quantization comparison that picked Q8_0 as the default.

### Image base

The CUDA image is built on `gcr.io/distroless/cc-debian13:nonroot` (Debian 13 trixie, glibc 2.41, gcc 14) with CUDA shared libraries copied from `nvidia/cuda:12.8.1-base-ubuntu24.04`. No shell, apt, dpkg, or Ubuntu OS layer in the final image. See [`doc/DOCKER_TAGS.md`](doc/DOCKER_TAGS.md) for the full lifecycle and CVE delta.

**Host requirements:**

- NVIDIA GPU with driver **≥ 520** (CUDA 12.x compatible)
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html) installed on the host

**Docker Compose:**

```bash
docker compose -f docker-compose.cuda.yml up -d
```

**Portainer:** use `portainer-stack-cuda.yml` — deploy as a new stack with `API_KEY`, `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD` env variables set.

---

## Server Management

```bash
docker compose up -d                                # start (CPU)
docker compose -f docker-compose.cuda.yml up -d     # start (CUDA)
docker compose logs -f                              # tail logs
docker compose down                                 # stop
docker compose down -v                              # stop AND wipe data + models (destructive)
```

Developer builds (from source):

```bash
cd server
make build                  # compile cix-server binary
make bundle                 # build + fetch llama-server (macOS Metal)
make run                    # bundle + launch with .env (dev)
make test                   # go test ./...
```

Pre-built images on Docker Hub:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | linux/amd64 + linux/arm64 | CPU |
| `dvcdsys/code-index:cu128` | linux/amd64 | NVIDIA GPU (CUDA 12.8) |
| `dvcdsys/code-index:<version>` / `<version>-cu128` | — | Version-pinned variants |
| `dvcdsys/code-index:develop-cu128` | linux/amd64 | Pre-release CUDA — pairs with the develop CLI channel |

For the full release procedure (tagging, CVE scans, Scout workflow, make targets), see [`doc/RELEASES.md`](doc/RELEASES.md). For tag lifecycle policy, [`doc/DOCKER_TAGS.md`](doc/DOCKER_TAGS.md).

---

## Security

The server is designed for a trusted-network or behind-a-reverse-proxy deployment. See [`doc/SECURITY_DEPLOYMENT.md`](doc/SECURITY_DEPLOYMENT.md) for:

- Trusted-proxy posture for `X-Forwarded-For` (load-bearing for the per-IP login rate limiter)
- TLS / `Secure` cookie auto-detection
- Login brute-force resistance (5/(IP,email)/15min + 60/IP/min)
- Body-size caps (1 MiB default, 64 MiB on `/index/files`)
- Bootstrap admin lifecycle, password policy
- API key scoping (inherits owner's role)
- What the server explicitly does **not** do (CSRF tokens, CORS, multi-tenancy, self-service reset)

---

## Documentation map

| Doc | Purpose |
|---|---|
| [`workspaces.md`](workspaces.md) | User-facing workspace guide (when to use, agent trust rules, query patterns) |
| [`doc/WORKSPACES.md`](doc/WORKSPACES.md) | Operator setup (encryption keys, Cloudflare tunnel, workers, REST API) |
| [`doc/SEARCH_ALGORITHM.md`](doc/SEARCH_ALGORITHM.md) | How per-project + hybrid workspace search rank results |
| [`doc/WEBHOOKS.md`](doc/WEBHOOKS.md) | GitHub webhook lifecycle, modes, HMAC validation |
| [`doc/UPDATES.md`](doc/UPDATES.md) | Release-poll banner + stable vs develop install channels |
| [`doc/CONFIG_REFERENCE.md`](doc/CONFIG_REFERENCE.md) | Complete env-var reference |
| [`doc/RELEASES.md`](doc/RELEASES.md) | Cutting CLI + server releases, CVE scans, make targets |
| [`doc/SETUP_MACOS_NATIVE.md`](doc/SETUP_MACOS_NATIVE.md) | Native macOS Metal setup + launchd plist |
| [`doc/SECURITY_DEPLOYMENT.md`](doc/SECURITY_DEPLOYMENT.md) | Production hardening |
| [`doc/DOCKER_TAGS.md`](doc/DOCKER_TAGS.md) | Docker Hub tag lifecycle |
| [`doc/LANGUAGES.md`](doc/LANGUAGES.md) | Supported chunker languages |
| [`doc/MIGRATION_FROM_PYTHON.md`](doc/MIGRATION_FROM_PYTHON.md) | Python → Go server migration notes |
| [`doc/benchmarks.md`](doc/benchmarks.md) | Index of dated benchmark snapshots |
| [`doc/openapi.yaml`](doc/openapi.yaml) | REST API source of truth |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Contributor workflow |
| [`plugins/cix/README.md`](plugins/cix/README.md) | Claude Code plugin reference |

---

## License

MIT
