> **Work in progress.** This project was largely vibe-coded. Use at your own risk.

```
 ██████╗██╗██╗  ██╗
██╔════╝██║╚██╗██╔╝
██║     ██║ ╚███╔╝
██║     ██║ ██╔██╗
╚██████╗██║██╔╝ ██╗
 ╚═════╝╚═╝╚═╝  ╚═╝  Code IndeX
```

Search your codebase by meaning, not just text. Self-hosted, embeddings-based, works with any agent or terminal.

```bash
cix search "authentication middleware"
cix search "database retry logic" --in ./api --lang go
cix symbols "UserService" --kind class
```

---

## Why

Grep and fuzzy file search work fine for small projects. At scale they break down:

- You have to know what a thing is called to find it
- Results flood with noise from unrelated files
- Agents waste tokens scanning files that aren't relevant

`cix` indexes your code into a vector store using [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) — a model purpose-built for code retrieval. Search queries return ranked snippets with file paths and line numbers, not raw file lists.

---

## Architecture

```
cix CLI (Go)
├── init      → register project + index + start file watcher
├── search    → semantic search (embeddings)
├── symbols   → symbol lookup by name (SQLite)
├── files     → file path search
├── summary   → project overview
├── reindex   → manual reindex trigger
└── watch     → fsnotify daemon → auto reindex on changes

API Server (Python / FastAPI)
├── sentence-transformers  → embeddings (CodeRankEmbed, 768d)
├── ChromaDB               → vector store (cosine similarity)
├── tree-sitter            → AST chunking (functions, classes, methods)
└── SQLite                 → project metadata, symbols, file hashes
```

The API server does the heavy lifting (ML model, ~400MB RAM). The CLI is a thin Go binary that talks to it over HTTP.

---

## Quick Start

### 1. Start the API Server

**Docker (recommended)**

```bash
git clone <repo-url> && cd code-index
./setup.sh
```

This generates `.env` with a random API key, creates `~/.cix/data/` for persistent storage, builds the image, and starts the container.

```bash
curl http://localhost:21847/health   # → {"status": "ok"}
```

**Local (no Docker)**

```bash
git clone <repo-url> && cd code-index
./setup-local.sh
# Installs deps, downloads model (~274MB), starts server, writes .env
```

Or manually:

```bash
python3 -m venv .venv && source .venv/bin/activate
pip install -r api/requirements.txt

# Create .env (see Configuration section)

source .env && cd api
uvicorn app.main:app --host 0.0.0.0 --port 21847
```

### 2. Install the CLI

**Option A: one-line installer (macOS / Linux)**

```bash
curl -fsSL https://raw.githubusercontent.com/<owner>/cix/main/install.sh | bash
```

**Option B: from source**

```bash
cd cli
make build && make install   # → /usr/local/bin/cix
```

Or without Make:

```bash
cd cli && go build -o cix . && sudo mv cix /usr/local/bin/
```

### 3. Configure

```bash
# Point cix at your server (API key is in .env)
cix config set api.url http://localhost:21847
cix config set api.key $(grep API_KEY .env | cut -d= -f2)
```

### 4. Index a Project

```bash
cd /path/to/your/project
cix init          # registers, indexes, starts file watcher daemon
cix status        # wait until: Status: ✓ Indexed
```

### 5. Search

```bash
cix search "authentication middleware"
cix search "error handling" --in ./api
cix symbols "handleRequest" --kind function
cix files "config"
cix summary
```

---

## CLI Reference

### Project Management

| Command | Description |
|---------|-------------|
| `cix init [path]` | Register + index + start file watcher |
| `cix status` | Show indexing status and progress |
| `cix list` | List all indexed projects |
| `cix reindex [--full]` | Trigger manual reindex |
| `cix summary` | Project overview: languages, directories, symbols |

### Search

```bash
# Semantic search — natural language, finds by meaning
cix search <query> [flags]
  --in <path>          restrict to file or directory (repeatable)
  --lang <language>    filter by language (repeatable)
  --limit, -l <n>      max results (default: 10)
  --min-score <0-1>    minimum relevance score (default: 0.1)
  -p <path>            project path (default: cwd)

# Symbol search — fast lookup by name
cix symbols <name> [flags]
  --kind <type>        function | class | method | type (repeatable)
  --limit, -l <n>      max results (default: 20)

# File search
cix files <pattern> [--limit <n>]
```

### File Watcher

```bash
cix watch [path]             # start background daemon
cix watch --foreground       # run in terminal (Ctrl+C to stop)
cix watch stop               # stop daemon
cix watch status             # check if running
```

The watcher monitors the project with `fsnotify`, debounces events (5s), and triggers incremental reindexing automatically. Logs: `~/.cix/logs/watcher.log`.

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
| `api.key` | — | Bearer token for API auth (required) |
| `watcher.debounce_ms` | `5000` | Delay in ms before reindex is triggered after a file change |
| `indexing.batch_size` | `20` | Number of files sent to the server per indexing batch |

---

## Agent Integration

`cix` is designed to be called by AI agents (Claude, GPT, Cursor, custom agents) as a shell tool. Agents run `cix search` instead of Grep/Glob — getting ranked, relevant snippets rather than raw file dumps.

### Claude Code

Install the bundled skill so Claude knows to use `cix` automatically:

```bash
cp -r skills/cix ~/.claude/skills/cix
```

Then in any Claude Code session:

```
/cix
```

This loads search guidance into context. Claude will use `cix search` instead of Grep.

To activate in every session without typing `/cix`, add to `~/.claude/CLAUDE.md`:

```markdown
## Code search
Use `cix` for all code search instead of Grep/Glob:
- `cix search "query"` — semantic search by meaning
- `cix symbols "Name" --kind function` — find symbol definitions
- `cix files "pattern"` — find files by path
- `cix summary` — project overview
Run `cix init` on first use in a project.
```

### Other Agents

Same pattern — give the agent access to shell execution and describe the commands:

```
Tool: shell
Usage: cix search "what you're looking for" [--in ./subdir] [--lang python]
Returns: ranked code snippets with file paths and line numbers
```

### Typical Agent Workflow

```bash
# First time in a project
cix init /path/to/project

# Explore
cix summary
cix search "main entry point"

# Find specific code
cix search "JWT token validation"
cix symbols "ValidateToken" --kind function

# Navigate
cix search "who calls ValidateToken"
cix search "error handling in auth flow" --in ./api
```

---

## How Indexing Works

**Chunking** — tree-sitter parses code into semantic chunks (functions, classes, methods). Unsupported languages fall back to a sliding window (2000 chars, 256 char overlap).

Supported languages: Python, TypeScript, JavaScript, Go, Rust, Java (+ 40+ others via fallback).

**Embeddings** — each chunk is encoded with [nomic-ai/CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) (768d, 8192 token context, ~274MB). Queries get a `"Represent this query for searching relevant code: "` prefix for asymmetric retrieval.

**Incremental reindex** — uses SHA256 file hashes. Only new or changed files are re-embedded. Deleted files are removed from the index.

**Filtering** — respects `.gitignore`, skips common dirs (`node_modules`, `.git`, `.venv`, etc.), skips files >512KB and empty files.

---

## Configuration Reference

### Server Environment Variables (`.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `API_KEY` | auto-generated | Bearer token for API auth |
| `PORT` | `21847` | API server port |
| `EMBEDDING_MODEL` | `nomic-ai/CodeRankEmbed` | HuggingFace model name |
| `MAX_FILE_SIZE` | `524288` | Skip files larger than this (bytes) |
| `EXCLUDED_DIRS` | `node_modules,.git,.venv,...` | Comma-separated dirs to skip |
| `CHROMA_PERSIST_DIR` | `~/.cix/data/chroma` (local) | ChromaDB storage path — **local mode only**, ignored in Docker |
| `SQLITE_PATH` | `~/.cix/data/sqlite/projects.db` (local) | SQLite database path — **local mode only**, ignored in Docker |

In Docker mode, data is stored in `~/.cix/data/` on the host via bind mount — no extra configuration needed.

### Resource Usage

| | Docker | Local |
|--|--------|-------|
| Memory (idle) | 2–4GB | 2–4GB |
| Memory (indexing) | up to 4–6GB | up to 4–6GB |
| CPU | capped at 2 cores | no limit |
| Disk | `~/.cix/data/` (~50–200MB/project) | `~/.cix/data/` (~50–200MB/project) |
| Auto-restart | yes | no (use launchd/systemd) |

---

## Docker Management

```bash
docker compose up -d           # start
docker compose down            # stop
docker compose logs -f         # tail logs
docker compose restart         # restart
docker compose up -d --build   # rebuild after code changes
docker compose down -v         # stop + delete all indexed data
```

---

## Building and Publishing to Docker Hub

Use this when you want to push your own image to Docker Hub (e.g. to run on a server or share).

### 1. Login to Docker Hub

```bash
docker login
```

### 2. Create the buildx builder (once per machine)

```bash
make docker-setup
```

This creates a multi-platform `buildx` builder named `cix-builder`.

### 3. Build and push

Replace `yourname` with your Docker Hub username.

**arm64 only** (Mac M1/M2/M3, Orange Pi 5, Raspberry Pi 4+):

```bash
make docker-push-arm64 DOCKER_USER=yourname
```

**amd64 only** (x86-64 servers, VMs, most Linux):

```bash
make docker-push-amd64 DOCKER_USER=yourname
```

**Both architectures under one tag** (multi-arch manifest, recommended):

```bash
make docker-push-all DOCKER_USER=yourname
# or with a specific version tag:
make docker-push-all DOCKER_USER=yourname VERSION=v1.0.0
```

This pushes `yourname/code-index:latest` (or the specified version) to Docker Hub as a multi-arch image.

### 4. Use your image

Update `docker-compose.yml` to reference your image instead of building locally:

```yaml
services:
  api:
    image: yourname/code-index:latest
```

Then start as usual:

```bash
docker compose up -d
```

---

## REST API

All endpoints except `/health` require `Authorization: Bearer <api_key>`.

```bash
GET  /health                                    # liveness check
GET  /api/v1/status                             # service status

POST /api/v1/projects                           # create project
GET  /api/v1/projects                           # list projects
GET  /api/v1/projects/{id}                      # project details
DELETE /api/v1/projects/{id}                    # delete project + index

POST /api/v1/projects/{id}/index                # trigger indexing
GET  /api/v1/projects/{id}/index/status         # indexing progress
POST /api/v1/projects/{id}/index/cancel         # cancel indexing

POST /api/v1/projects/{id}/search               # semantic search
POST /api/v1/projects/{id}/search/symbols       # symbol search
POST /api/v1/projects/{id}/search/files         # file path search
GET  /api/v1/projects/{id}/summary              # project overview
```

---

## Troubleshooting

**`API key not set`**
```bash
cix config set api.key $(grep API_KEY /path/to/code-index/.env | cut -d= -f2)
```

**`connection refused`**
```bash
curl http://localhost:21847/health   # check if server is up
docker compose up -d                # Docker
./setup-local.sh                    # local
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
- Check project is indexed: `cix status`
- Lower the threshold: `cix search "query" --min-score 0.05`
- Docker mode: run `cix list` to verify the project is registered

---

## Releases

Cross-platform binaries are built with:

```bash
cd cli
make release VERSION=v0.1.0
```

This produces archives for macOS and Linux (amd64 + arm64) in `cli/dist/`, plus a `checksums.txt`. Upload them to a GitHub Release and the `install.sh` installer will pick up the latest version automatically.

Supported targets: `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`.

---

## License

MIT