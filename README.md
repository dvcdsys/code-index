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

Search your codebase by meaning, not just text. Self-hosted, embeddings-based, works with any agent or terminal — and now with a full web dashboard.

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

- **`cix-server`** — Go HTTP API with embedded llama.cpp sidecar for embeddings, SQLite for symbols + project metadata, chromem-go for vectors. Ships as a single distroless container.
- **Web dashboard** at `/dashboard` — projects, semantic search, user + API-key management, runtime sidecar control, drift indicator. Embedded directly into the server binary.
- **`cix` CLI** — drop-in `cix search`/`cix symbols`/`cix files` commands for terminal + agent use.
- **File watcher** — `cix watch` keeps the index fresh as you edit, no manual reindex.
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
│  ├── embedded React dashboard (//go:embed all:dist)             │
│  └── embedded Swagger UI                                        │
│                                                                 │
│  Indexing pipeline                                              │
│  ├── gotreesitter (AST chunking, 200+ languages)                │
│  ├── llama-server sidecar (Unix socket → CodeRankEmbed Q8 GGUF) │
│  ├── chromem-go (cosine similarity vector store)                │
│  └── modernc.org/sqlite (projects, symbols, file hashes)        │
└────────────┬─────────────────────────────────────┬──────────────┘
             │ HTTP                                │ Unix socket
             ▼                                     ▼
   cix CLI (Go) — search,           ┌──────────────────────────┐
   symbols, files, init,            │  llama-server child proc │
   reindex, watch                   │  (llama.cpp embeddings)  │
                                    └──────────────────────────┘
```

The server is a pure-Go static binary; CUDA-image variants add a `libcublas` runtime layer for GPU embeddings.

---

## Quick Start

### 1. Start the server

Three deployment modes:

| Mode | Best for | GPU | Prerequisites |
|------|----------|-----|---------------|
| **Docker (CPU)** | any OS, dev / small repos | none | Docker |
| **Docker (CUDA)** | NVIDIA GPU servers | CUDA 12.x | Docker + NVIDIA Container Toolkit |
| **Native (macOS)** | Apple Silicon w/ full Metal | Metal | Go 1.25+, Xcode CLT |

#### Docker (CPU)

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
cp .env.example .env
# Edit .env — set CIX_API_KEY, CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
docker compose up -d
```

```bash
curl http://localhost:21847/health   # → {"status":"ok"}
```

> [!IMPORTANT]
> On a fresh database the server **refuses to start** unless both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` are set. The user is created with `must_change_password=true`, so the temporary password only works for the first login.

#### Docker (CUDA — NVIDIA GPU)

See [GPU Acceleration (CUDA)](#gpu-acceleration-cuda) below.

```bash
docker compose -f docker-compose.cuda.yml up -d
```

#### Native macOS (Apple Silicon — Metal GPU)

> **Why not Docker?** Docker Desktop on macOS runs containers inside a Linux VM — Metal GPU is **not accessible** from within a container. For full Metal acceleration you must run natively.

```bash
xcode-select --install                # if not installed
cd server && make bundle              # builds cix-server + downloads Metal-enabled llama-server
cp .env.example .env
# Set CIX_API_KEY, CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
# Set CIX_N_GPU_LAYERS=99 for full Metal offload
cd server && make run
```

Native env-var summary for Metal:

| Variable | Recommended | Notes |
|---|---|---|
| `CIX_N_GPU_LAYERS` | `99` | Offload all layers to Metal; `0` = CPU only |
| `CIX_LLAMA_BIN_DIR` | set by `make run` | Path to the `llama-server` bundle dir |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Default. Set `false` to skip the sidecar entirely |

> [!TIP]
> `make run` runs `make bundle` first (no-op if already built), so it's safe after any `git pull`.

**Auto-start with launchd** (optional — run as a background service on login):

```bash
cat > ~/Library/LaunchAgents/com.cix.server.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cix.server</string>
  <key>ProgramArguments</key>
  <array><string>/ABSOLUTE/PATH/TO/server/dist/cix-darwin-arm64/cix-server</string></array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CIX_API_KEY</key><string>YOUR_KEY</string>
    <key>CIX_BOOTSTRAP_ADMIN_EMAIL</key><string>admin@example.com</string>
    <key>CIX_BOOTSTRAP_ADMIN_PASSWORD</key><string>change-me-on-first-login</string>
    <key>CIX_LLAMA_BIN_DIR</key><string>/ABSOLUTE/PATH/TO/server/dist/cix-darwin-arm64/llama</string>
    <key>CIX_N_GPU_LAYERS</key><string>99</string>
    <key>CIX_PORT</key><string>21847</string>
    <key>CIX_SQLITE_PATH</key><string>/Users/YOUR_USER/.cix/data/sqlite/projects.db</string>
    <key>CIX_CHROMA_PERSIST_DIR</key><string>/Users/YOUR_USER/.cix/data/chroma</string>
    <key>CIX_GGUF_CACHE_DIR</key><string>/Users/YOUR_USER/.cix/data/models</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/cix-server.log</string>
  <key>StandardErrorPath</key><string>/tmp/cix-server.err</string>
</dict></plist>
EOF
launchctl load ~/Library/LaunchAgents/com.cix.server.plist
launchctl start com.cix.server
```

### 2. Log in to the dashboard

Open http://localhost:21847/dashboard in your browser.

1. Sign in with the email + password you set as `CIX_BOOTSTRAP_ADMIN_*` env vars.
2. You'll be **forced to change the password** on first login. Pick a real one.
3. Land on the home screen — see [Dashboard](#dashboard) for what's there.

### 3. Install the CLI

**Option A — one-line installer (macOS / Linux):**

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
```

**Option B — from source:**

```bash
cd cli && make build && make install   # → /usr/local/bin/cix
```

**Option C — without Make:**

```bash
cd cli && go build -o cix . && sudo mv cix /usr/local/bin/
```

### 4. Configure the CLI

```bash
cix config set api.url http://localhost:21847
cix config set api.key $(grep CIX_API_KEY .env | cut -d= -f2)
```

Or mint a fresh API key from the dashboard's **API Keys** page and paste that.

### 5. Index a project

```bash
cd /path/to/your/project
cix init           # registers + indexes + starts the file watcher
cix status         # wait for: Status: ✓ Indexed
```

### 6. Search

```bash
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
| **Home** | everyone | Live status strip (server version, current embedding model, sidecar Ready/Loading) + module shortcuts |
| **Projects** | everyone | List indexed projects, view stats (file count, languages, symbols, vector count, sqlite/chroma sizes), copy reindex commands. Cards turn **red with "Stale model"** badge when the runtime embedding model differs from the model the project was indexed with — see [Drift indicator](#drift-indicator). |
| **Search** | everyone | Five modes: semantic, symbols, references, definitions, files. Same engine the CLI uses. |
| **API Keys** | everyone | Mint long-lived `cix_*` keys (256-bit entropy, GitHub-class), copy them once, revoke at any time. |
| **Users** | admin | Invite teammates, set role (admin/viewer), reset password (forces change on next login), disable account. |
| **Settings** | everyone | Theme, default editor, change own password. |
| **Server** | admin | Runtime config — embedding model, `n_ctx`, `n_gpu_layers`, `n_threads`, batch size, queue concurrency. **Save & Restart** drains in-flight embeddings, restarts the sidecar, polls until ready. Source pill on each field shows whether the live value comes from the DB override, env bootstrap, or the recommended fallback. |

### Authentication

Two paths share the same identity model:

- **Cookie session** (browser) — `cix_session` HttpOnly cookie, 14-day rolling TTL, `sha256(token)` stored in DB. The raw token never leaves the browser.
- **Bearer API key** (CLI / agents / CI) — `Authorization: Bearer cix_<43-char-base64url>` header. 256 bits of entropy, hex-`sha256`-stored, scoped to the issuing user's role.

### Drift indicator

When you change the runtime embedding model (Server → Embedding model → Save & Restart), every project that was indexed with the previous model becomes stale — the vectors are no longer comparable to fresh queries. The dashboard surfaces this:

- **Projects list:** stale projects render with `border-destructive` + a `Stale model` badge.
- **Project detail page:** a banner "Indexed with `<old>`; current runtime model is `<new>`. Vectors may be incompatible." with a copy-to-clipboard `cix reindex /path` command.

After running the reindex, the drift signal clears automatically.

### Disabled-embeddings mode

Set `CIX_EMBEDDINGS_ENABLED=false` to bring the server up without the llama-server sidecar — auth, dashboard, project metadata, and symbol/file searches all keep working; only semantic search and indexing are disabled. The Server page renders a warning banner and disables the relevant inputs.

---

## CLI Reference

### Project Management

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

### File Watcher

```bash
cix watch [path]             # start background daemon
cix watch --foreground       # run in terminal (Ctrl+C to stop)
cix watch stop               # stop daemon
cix watch status             # check if running
```

The watcher monitors the project with `fsnotify`/`rjeczalik/notify`, debounces events (5 s default), and triggers incremental reindexing automatically. Logs: `~/.cix/logs/watcher.log`.

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

### Claude Code

Install the bundled skill so Claude knows to use `cix` automatically:

```bash
cp -r skills/cix ~/.claude/skills/cix
```

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

A bare `/cix` works (yields a generic "ready to search" reply), and a search-style prompt like `/cix find X` works (Claude does one search and stops). Neither captures the real value. Pairing the skill with a real task — fix, implement, investigate, refactor — is what makes the agent meaningfully more useful than grep + reading files.

To activate in every session without typing `/cix` (so cix becomes the default reflex for any code-search question), add to `~/.claude/CLAUDE.md`:

```markdown
## Code search
Use `cix` for all code search instead of Grep/Glob:
- `cix search "query"` — semantic search by meaning
- `cix symbols "Name" --kind function` — find symbol definitions
- `cix files "pattern"` — find files by path
- `cix summary` — project overview
Run `cix init` on first use in a project.
```

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

## How Indexing Works

**Chunking** — tree-sitter parses code into semantic chunks (functions, classes, methods). Unsupported languages fall back to a sliding window (2000 chars, 256 char overlap).

Supported languages: Python, TypeScript, JavaScript, Go, Rust, Java, C, C++, C#, Ruby, PHP, Swift, Kotlin, Scala, Bash, SQL, Markdown, HTML, CSS, and 30+ more.

**Embeddings** — each chunk is encoded with a GGUF build of CodeRankEmbed (default: [awhiteside/CodeRankEmbed-Q8_0-GGUF](https://huggingface.co/awhiteside/CodeRankEmbed-Q8_0-GGUF); 768d, 8192-token context, ~145 MB on disk) via the `llama-server` sidecar (llama.cpp). Queries get a `"Represent this query for searching relevant code: "` prefix for asymmetric retrieval.

**Path-aware preamble** — each chunk is embedded with its file path, language, and parent symbol prefixed. This makes "auth middleware" find `auth.go` even if the file content uses different vocabulary. Toggle with `CIX_EMBED_INCLUDE_PATH` (default `true`); changing it requires `cix reindex --full`.

**Incremental reindex** — uses SHA-256 file hashes. Only new or changed files are re-embedded. Deleted files are removed from the index.

**Filtering** — respects `.gitignore` and `.cixignore`, skips common dirs (`node_modules`, `.git`, `.venv`, etc.), skips files >`CIX_MAX_FILE_SIZE` (512 KiB default) and empty files. Per-project configuration via `.cixconfig.yaml` (see below).

---

## Tuning Search Quality

### `--min-score` threshold

`cix` defaults to `--min-score 0.4`. This is calibrated for **CodeRankEmbed-Q8_0** with the path-aware embedding format (`CIX_EMBED_INCLUDE_PATH=true`, default).

A typical score landscape on a real codebase:

| Match strength | Score range | Action |
|---|---|---|
| Exact symbol or filename match | 0.65 – 0.80 | rare; very high confidence |
| Strong path-aware concept match | 0.50 – 0.65 | typical "good" match for `cix search "cli watch daemon"` |
| Weaker concept / partial path overlap | 0.40 – 0.50 | typical for ambiguous or multi-token queries |
| Likely unrelated noise | < 0.40 | filtered out by default |

**When to lower the threshold**:

- The query returns `No results` but you know matching code exists — try `--min-score 0.25`
- Your query is intentionally vague (exploring an unfamiliar codebase) — `--min-score 0.2`
- Single-word identifier queries on rare names

**When to raise the threshold**:

- Agent context is filling up with weak matches — `--min-score 0.5`
- You only want clear top hits — `--min-score 0.6`

> [!NOTE]
> CodeRankEmbed is **asymmetric**: queries get a `"Represent this query for searching relevant code: "` prefix, which puts query and passage vectors into separate regions of the embedding space. Cosine similarities are systematically lower than for symmetric models — a "strong" match here is 0.55, not 0.80. Don't compare these numbers to thresholds quoted for OpenAI / Voyage / generic sentence-transformers.

> [!TIP]
> If you switched embedding models or toggled `CIX_EMBED_INCLUDE_PATH`, run `cix reindex --full` and recalibrate. Old vectors and new vectors live in the same store but score differently.

### `--exclude` for noisy directories

Repos with vendored code, fixtures, or legacy migrations can pull unrelated paths into top results because path tokens contribute to scoring. Two options:

```bash
# One-off exclude for a single search
cix search "main entry point" --exclude vendor --exclude bench/fixtures

# Permanent exclude — add to .cixignore (skips indexing entirely)
echo "vendor/" >> .cixignore
echo "bench/fixtures/" >> .cixignore
cix reindex --full
```

`.cixignore` is preferred for directories you never want in results — they don't take up index space. `--exclude` is a per-query escape hatch.

---

## Per-Project Configuration

### `.cixignore` — exclude files from indexing

Works exactly like `.gitignore` (same syntax, same nesting rules). Place it in the project root or any subdirectory. Patterns from `.cixignore` are merged with `.gitignore` — you don't need to duplicate rules.

Use `.cixignore` when you want to exclude files from the index that are **not** excluded by `.gitignore` (e.g., vendored code, generated files, large test fixtures).

```gitignore
# .cixignore
api/smart-contracts/
generated/
*.pb.go
testdata/fixtures/
```

Nested `.cixignore` files work like nested `.gitignore` — they apply to their directory and below, without affecting sibling directories. The file watcher automatically triggers a full reindex when `.cixignore` is created, modified, or deleted.

### `.cixconfig.yaml` — project-level settings

Place this in the project root. Currently supports automatic git submodule exclusion:

```yaml
ignore:
  submodules: true   # automatically exclude all git submodule paths
```

When `ignore.submodules` is `true`, cix reads `.gitmodules` and excludes all submodule paths from indexing. No git binary required — the file is parsed directly. Useful for Foundry/Forge dependencies, vendored submodules, or any repo where submodules contain thousands of files you don't want indexed.

The file watcher triggers a full reindex when `.cixconfig.yaml` changes.

---

## Configuration Reference

### Server environment variables

Complete list. See `.env.example` for the operator-facing template.

#### Auth + bootstrap

| Variable | Default | Description |
|---|---|---|
| `CIX_API_KEY` | — | Header API key for direct CLI / CI traffic. On first boot it's imported as the bootstrap admin's `env-bootstrap` key. |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` | — | **Required on a fresh DB.** Seeds the first admin user. Ignored once the users table is non-empty. |
| `CIX_BOOTSTRAP_ADMIN_PASSWORD` | — | **Required on a fresh DB.** The user is flagged `must_change_password=true`, so this only works for the first login. |
| `CIX_AUTH_DISABLED` | `false` | **Dev only.** Skips auth on every endpoint — every request behaves as admin. Never set in production. |

#### Networking + storage

| Variable | Default | Description |
|---|---|---|
| `CIX_PORT` | `21847` | Listen port (both Docker images bake this in). |
| `CIX_SQLITE_PATH` | `/data/sqlite/projects.db` | SQLite path. Suffixed with the model-safe name on open. |
| `CIX_CHROMA_PERSIST_DIR` | `/data/chroma` | Vector store directory. |
| `CIX_GGUF_CACHE_DIR` | `/data/models` | Where downloaded GGUF files live. |

#### Indexing

| Variable | Default | Description |
|---|---|---|
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | HuggingFace GGUF repo (or absolute path to a `.gguf`). |
| `CIX_MAX_FILE_SIZE` | `524288` | Skip files larger than this (bytes). |
| `CIX_EXCLUDED_DIRS` | `node_modules,.git,.venv,...` | Comma-separated dirs always skipped. |
| `CIX_LANGUAGES` | all | Comma-separated allow-list of chunker languages. Empty = all baked-in. |
| `CIX_EMBED_INCLUDE_PATH` | `true` | Path/language/symbol preamble before each chunk. Toggling requires `cix reindex --full`. |
| `CIX_MAX_CHUNK_TOKENS` | `1500` | Max chunk size before falling back to sliding window. Must stay ≤ `CIX_LLAMA_CTX`. |

#### llama-server sidecar

| Variable | Default | Description |
|---|---|---|
| `CIX_EMBEDDINGS_ENABLED` | `true` | Set `false` to boot without the sidecar (read-only mode). |
| `CIX_LLAMA_BIN_DIR` | `/app` (Docker) / `<exe>/llama` (native) | Directory containing `llama-server` + dylibs. |
| `CIX_LLAMA_TRANSPORT` | `unix` | `unix` or `tcp`. Auto-falls-back to TCP if the socket path is too long. |
| `CIX_LLAMA_SOCKET` | `${TMPDIR}/cix-llama-<pid>.sock` | Unix socket path. macOS `sun_path` cap = 104 bytes. |
| `CIX_LLAMA_CTX` | `2048` | `--ctx-size` passed to llama-server. |
| `CIX_N_GPU_LAYERS` | `-1` darwin / `0` else / `99` Docker CUDA | `99` offloads all layers; `0` forces CPU. |
| `CIX_LLAMA_STARTUP_TIMEOUT` | `60` | Seconds to wait for the sidecar's readiness probe. |
| `CIX_GGUF_PATH` | auto-resolve | Absolute path to a GGUF file. Empty → cache lookup → HF download. |
| `CIX_BOOTSTRAP_GGUF_PATH` | — | Optional. If set, cix imports this `.gguf` into `CIX_GGUF_CACHE_DIR` once (atomic `.partial → rename`) and ignores the env on subsequent boots. Useful for skipping the first-boot HF download in air-gapped or rate-limited environments. |

#### Tuning (also editable from `/dashboard/server`)

| Variable | Default | Description |
|---|---|---|
| `CIX_LLAMA_THREADS` | `0` (auto = `runtime.NumCPU()/2`) | CPU threads passed to llama-server. |
| `CIX_LLAMA_BATCH` | `0` (match `CIX_LLAMA_CTX`) | `-b` batch size. |
| `CIX_MAX_EMBEDDING_CONCURRENCY` | `5` | Embedding queue parallelism. Drop to `1` if the GPU contends. |
| `CIX_EMBEDDING_QUEUE_TIMEOUT` | `300` | Seconds before a queued embedding request is failed. |

> [!TIP]
> Anything in the **Tuning** group is overridable at runtime from the dashboard's **Server** page (admin only). The dashboard writes to a DB row and triggers a sidecar restart — the env-var values are the boot-time fallback.

### Resource Usage

| | Native (Apple Silicon) | Docker (CPU) | Docker (CUDA) |
|--|---|---|---|
| Image size | n/a | ~21 MB | ~1.0 GB |
| Memory (idle) | ~1 GB | ~1 GB | ~1 GB (system) + ~0.7 GB VRAM |
| Memory (indexing) | up to 2 GB | up to 2 GB | up to 2 GB system + ~0.7 GB VRAM |
| GPU | Metal | none | NVIDIA CUDA 12.x |
| Disk | `~/.cix/data/` (~50–200 MB/project) | same (mounted volume) | same |
| Auto-restart | use `launchd` | yes | yes |

### Switching embedding models

The server ships with `awhiteside/CodeRankEmbed-Q8_0-GGUF` — a Q8-quantized build of CodeRankEmbed (137M params, 768d, ~145 MB on disk, ~0.5–0.7 GB idle VRAM/RAM). Inference runs via the `llama-server` sidecar, so **only GGUF repositories are supported**. Plain PyTorch / `sentence-transformers` repos won't work.

You can switch in two places:

- **Dashboard → Server → Embedding model.** Pick from the on-disk cache (the dropdown lists `CIX_GGUF_CACHE_DIR`/*.gguf), or paste a HuggingFace repo or absolute path. **Save & Restart** drains, restarts the sidecar, and turns existing project cards red ("Stale model") until you reindex.
- **Env / `.env` file.** Set `CIX_EMBEDDING_MODEL=<repo-or-path>` and restart the container. The dashboard's runtime override (if any) wins; the env value becomes the bootstrap default.

> [!NOTE]
> ChromaDB and SQLite paths are suffixed by a sanitised form of the model name (e.g. `projects_awhiteside_coderankembed_q8_0_gguf.db`). This isolates vector spaces per model — switching back and forth keeps old indices intact and avoids dim-mismatch errors. Re-indexing under a model is **not free** (chunk count × embedding latency), but you don't lose state.

> [!TIP]
> **Apple Silicon:** Docker can't access Metal GPU — run natively. The bundled `llama-server` includes `libggml-metal.dylib`; set `CIX_N_GPU_LAYERS=99` for full Metal offload.
> **Linux NVIDIA:** use the CUDA image (`docker-compose.cuda.yml`). Force CPU with `CIX_N_GPU_LAYERS=0`.

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
make test-gate              # parity gate vs reference embeddings (requires GGUF)
make docker-build-cuda      # build + push CUDA image (uses cix-builder)
make docker-build-cuda-dev  # build + push :cu128-dev tag (smoke testing)
make scout-cuda             # safe pre-push CVE scan workflow
make promote-cuda SCOUT_TAG=scout-…  # retag without rebuild
```

---

## Building and publishing

CI handles releases — see [Releases](#releases). For local manual builds:

```bash
docker login
make docker-build-cuda                 # builds + pushes server/Dockerfile.cuda → :cu128
make docker-build-cuda-dev             # → :cu128-dev (operator iteration)
```

Pre-built images on Docker Hub:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | linux/amd64 + linux/arm64 | CPU |
| `dvcdsys/code-index:v0.5.1` | linux/amd64 + linux/arm64 | CPU, version-pinned |
| `dvcdsys/code-index:cu128` | linux/amd64 | NVIDIA GPU (CUDA 12.8) |
| `dvcdsys/code-index:v0.5.1-cu128` | linux/amd64 | NVIDIA, version-pinned |

See `doc/DOCKER_TAGS.md` for the full tag lifecycle policy.

---

## REST API

All endpoints except `/health`, `/api/v1/auth/login`, `/api/v1/auth/bootstrap-status`, `/dashboard/*`, `/docs`, and `/openapi.json` require authentication.

**Two auth methods accepted on every authenticated endpoint:**

- `Authorization: Bearer cix_<token>` — API key (CLI / agents / CI)
- `Cookie: cix_session=<raw-token>` — browser session (set by `/auth/login`)

### Probes + auth

```
GET    /health                                       liveness
GET    /api/v1/status                                live config snapshot

GET    /api/v1/auth/bootstrap-status                 anyone — needs_bootstrap?
POST   /api/v1/auth/login                            email + password → cookie
POST   /api/v1/auth/logout                           clears cookie + DB row
GET    /api/v1/auth/me                               current user
POST   /api/v1/auth/change-password                  forced or voluntary
GET    /api/v1/auth/sessions                         my active sessions
DELETE /api/v1/auth/sessions/{id}                    revoke a session
```

### API keys + admin (admin role)

```
GET    /api/v1/api-keys                              list keys (own; admin sees all)
POST   /api/v1/api-keys                              mint a new key
DELETE /api/v1/api-keys/{id}                         revoke

GET    /api/v1/admin/users                           list users + stats
POST   /api/v1/admin/users                           create user
PATCH  /api/v1/admin/users/{id}                      update role / disable / reset password
DELETE /api/v1/admin/users/{id}                      delete

GET    /api/v1/admin/runtime-config                  current snapshot + Source map
PUT    /api/v1/admin/runtime-config                  patch overrides (does NOT restart)
POST   /api/v1/admin/sidecar/restart                 drain + respawn llama-server
GET    /api/v1/admin/sidecar/status                  pid, uptime, model, ready
GET    /api/v1/admin/models                          list cached GGUF files in CIX_GGUF_CACHE_DIR
```

### Projects + indexing + search

```
GET    /api/v1/projects                              list
POST   /api/v1/projects                              register
GET    /api/v1/projects/{path}                       detail (sizes, drift, params)
PATCH  /api/v1/projects/{path}                       admin — settings
DELETE /api/v1/projects/{path}                       admin — drop project + index

POST   /api/v1/projects/{path}/index/begin           open run + return stored hashes
POST   /api/v1/projects/{path}/index/files           NDJSON streaming batch upload
POST   /api/v1/projects/{path}/index/finish          close run
POST   /api/v1/projects/{path}/index/cancel          any user — cancel active run
GET    /api/v1/projects/{path}/index/status          progress

POST   /api/v1/projects/{path}/search                semantic
POST   /api/v1/projects/{path}/search/symbols
POST   /api/v1/projects/{path}/search/definitions
POST   /api/v1/projects/{path}/search/references
POST   /api/v1/projects/{path}/search/files
GET    /api/v1/projects/{path}/summary
```

The full schema lives in `doc/openapi.yaml` and is browsable at `http://<host>:21847/docs` (Swagger UI).

---

## Troubleshooting

**Server refuses to start: `bootstrap auth: no users in database and the bootstrap admin env vars are not set`**
→ Set both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` in your `.env`, restart. Once you log in and change the password, you can drop the env vars (the user lives in the DB).

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

**`project not found`**
```bash
cix init /path/to/project
```

**Watcher not triggering reindex**
```bash
cix watch status
cat ~/.cix/logs/watcher.log
cix watch stop && cix watch /path/to/project
```

**Search returns no results**
- Check the project is indexed: `cix status`
- Lower the threshold: `cix search "query" --min-score 0.2` (default is `0.4`; see [Tuning Search Quality](#tuning-search-quality))
- `cix list` to verify the project is registered

**Dashboard shows "Stale model" on every project after upgrade**
→ The runtime model was changed (or its version stamp shifted). Either reindex affected projects (`cix reindex --full` per project) or revert the model change in **Server → Embedding model**.

**Forgot the admin password and there's no second admin**
→ Edit `users` table directly in `CIX_SQLITE_PATH`: clear `disabled_at` and reset `password_hash` (bcrypt cost 12). Better long-term: keep at least two admin accounts so this never recurs. See `doc/SECURITY_DEPLOYMENT.md`.

---

## Releases

CLI and server ship on independent tag streams:

| Component | Tag pattern | Workflow | Artifact |
|---|---|---|---|
| Server (`cix-server`) | `server/v*` (e.g. `server/v0.5.1`) | `release-server.yml` | Docker images on Docker Hub: `:latest`, `:<version>`, `:cu128`, `:<version>-cu128` |
| CLI (`cix`) | `cli/v*` (e.g. `cli/v0.5.0`) | `release-cli.yml` | `cix-{darwin,linux}-{amd64,arm64}.tar.gz` on a GitHub Release |

Bare `v*` tags are the historical pre-split CLI line — the installer still falls back to them when no `cli/v*` release exists, but no new bare-`v*` tags should be created.

### Cutting a CLI release

```bash
git tag cli/v0.6.0
git push origin cli/v0.6.0
```

CI builds binaries for macOS + Linux (amd64 + arm64), uploads them to a release named `cli/v0.6.0`, and the installer auto-picks them up on the next run.

### Cutting a server release

```bash
git tag server/v0.5.2
git push origin server/v0.5.2
```

CI builds CPU multi-arch + CUDA amd64 images with provenance + SBOM attestations, pushes to Docker Hub with both pinned (`:0.5.2`, `:0.5.2-cu128`) and floating (`:latest`, `:cu128`) tags, and creates a GitHub Release. Pre-tag CVE scan: `cd server && make scout-cuda`.

### Local cross-build (no release)

```bash
cd cli && make release VERSION=v0.6.0
```

Produces archives in `cli/dist/` plus `checksums.txt`. Useful for testing the artifact format before pushing a tag.

Supported targets: `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`.

---

## GPU Acceleration (CUDA)

A CUDA-enabled image is available for servers with NVIDIA GPUs. Inference runs on GPU automatically — no configuration needed.

### VRAM Usage (CodeRankEmbed Q8_0 GGUF, RTX 3090)

With the GGUF backend the footprint is near-constant: weights (~200–250 MB) plus the pre-allocated context (`n_ctx=8192`, ~200–400 MB) give a **~0.5–0.7 GB** idle draw. Embedding calls do not spike VRAM the way fp16 PyTorch attention used to — sequence length and batch size only change latency, not peak memory.

`CIX_MAX_CHUNK_TOKENS` still caps the length of each code chunk (1 token ≈ 4 chars) and must stay ≤ `CIX_LLAMA_CTX` (8192). `CIX_MAX_EMBEDDING_CONCURRENCY` defaults to `5` — the indexing queue ships chunks in parallel; the llama-server sidecar still serialises requests through one context, but pipelining host-side prep with device inference at this depth saturates the GPU without measurable latency cost. Drop to `1` only if you observe contention.

See [`doc/vram-profiling.md`](doc/vram-profiling.md) for methodology and numbers.

**Docker Hub:** [`dvcdsys/code-index:cu128`](https://hub.docker.com/r/dvcdsys/code-index/tags) (floating) and `:<version>-cu128` (pinned). Image size: ~1.66 GB (3-stage build: `nvidia/cuda:12.8.1-base` + libcublas + llama-server + Go binary).

See `doc/DOCKER_TAGS.md` for the full tag lifecycle.

**Host requirements:**

- NVIDIA GPU with driver **≥ 520** (CUDA 12.x compatible)
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html) installed on the host

**Docker Compose:**

```bash
docker compose -f docker-compose.cuda.yml up -d
```

**Portainer:** use `portainer-stack-cuda.yml` — deploy as a new stack with `API_KEY`, `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD` env variables set.

---

## Security

The server is designed for a trusted-network or behind-a-reverse-proxy deployment. See **[`doc/SECURITY_DEPLOYMENT.md`](doc/SECURITY_DEPLOYMENT.md)** for:

- Trusted-proxy posture for `X-Forwarded-For` (load-bearing for the per-IP login rate limiter)
- TLS / `Secure` cookie auto-detection
- Login brute-force resistance (5/(IP,email)/15min + 60/IP/min)
- Body-size caps (1 MiB default, 64 MiB on `/index/files`)
- Bootstrap admin lifecycle
- Password policy (server enforces only `len ≥ 8`)
- API key scoping (inherits owner's role)
- What the server explicitly does **not** do (CSRF tokens, CORS, multi-tenancy, self-service reset)

---

## License

MIT
