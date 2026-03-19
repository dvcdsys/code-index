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

Three deployment options — pick the one that fits your setup:

| Mode | Best for | GPU acceleration | Prerequisites |
|------|----------|-----------------|---------------|
| **Local** | macOS (Apple Silicon), development | MPS (Apple GPU) | none — `uv` installs Python automatically |
| **Docker** | any OS, isolation, servers | CPU only | Docker |
| **CUDA** | NVIDIA GPU servers | CUDA | Docker, NVIDIA Container Toolkit |

#### Local (recommended for Mac)

Native execution with automatic Apple MPS (Metal) GPU acceleration on Apple Silicon. No Python or Docker required — the setup script installs everything via [uv](https://docs.astral.sh/uv/).

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
./setup-local.sh    # or: make server-local-setup
```

This installs `uv` (if needed), downloads Python 3.12 automatically, installs dependencies, downloads the embedding model (~274MB), starts the server, and registers the MCP server in Claude Code.

```bash
curl http://localhost:21847/health   # → {"status": "ok"}
```

Daily usage after setup:

```bash
make server-local-start     # start server
make server-local-stop      # stop server
make server-local-restart   # restart server
make server-local-status    # check status
make server-local-logs      # tail logs
```

#### Docker (CPU)

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
./setup.sh    # or: make server-docker-start
```

This generates `.env` with a random API key, creates `~/.cix/data/` for persistent storage, pulls `dvcdsys/code-index:latest` from Docker Hub, and starts the container.

```bash
make server-docker-start    # start
make server-docker-stop     # stop
make server-docker-restart  # restart
make server-docker-status   # check status
make server-docker-logs     # tail logs
```

> **Note:** Docker Desktop on Mac runs a Linux VM — Apple Metal/MPS is not available inside containers. For GPU-accelerated inference on Mac, use the Local mode instead.

#### CUDA (NVIDIA GPU)

See [GPU Acceleration (CUDA)](#gpu-acceleration-cuda) section below.

```bash
make server-cuda-start      # start
make server-cuda-stop       # stop
make server-cuda-restart    # restart
make server-cuda-status     # check status
make server-cuda-logs       # tail logs
```

### 2. Install the CLI

**Option A: one-line installer (macOS / Linux)**

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
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

**Filtering** — respects `.gitignore` and `.cixignore`, skips common dirs (`node_modules`, `.git`, `.venv`, etc.), skips files >512KB and empty files. Per-project configuration via `.cixconfig.yaml` (see below).

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

Nested `.cixignore` files work like nested `.gitignore` — they apply to their directory and below, without affecting sibling directories.

The file watcher automatically triggers a full reindex when `.cixignore` is created, modified, or deleted.

### `.cixconfig.yaml` — project-level settings

Place this file in the project root. Currently supports automatic git submodule exclusion.

```yaml
# .cixconfig.yaml
ignore:
  submodules: true   # automatically exclude all git submodule paths
```

When `ignore.submodules` is `true`, cix reads `.gitmodules` and excludes all submodule paths from indexing. No git binary is required — the file is parsed directly.

This is useful for projects with Foundry/Forge dependencies, vendored submodules, or any repo where submodules contain thousands of files you don't want indexed.

**Example:** a project with 228 own files and 3,400+ files in nested submodules — after adding `ignore.submodules: true`, only the 228 project files are indexed.

The file watcher triggers a full reindex when `.cixconfig.yaml` changes.

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
| `CPUS` | `2.0` | Number of CPU cores available to the container |
| `OMP_NUM_THREADS` | all cores | OpenMP threads used by the embedding model (CPU inference) |
| `CHROMA_PERSIST_DIR` | `~/.cix/data/chroma` (local) | ChromaDB storage path — **local mode only**, ignored in Docker |
| `SQLITE_PATH` | `~/.cix/data/sqlite/projects.db` (local) | SQLite database path — **local mode only**, ignored in Docker |

In Docker mode, data is stored in `~/.cix/data/` on the host via bind mount — no extra configuration needed.

### Resource Usage

| | Local (native) | Docker (CPU) | CUDA |
|--|----------------|--------------|------|
| Memory (idle) | 2-4GB | 2-4GB | 2-4GB |
| Memory (indexing) | up to 4-6GB | up to 4-6GB | up to 4-6GB |
| CPU | no limit | `CPUS` env var (default: 2) | unlimited |
| GPU | MPS (Apple Silicon) | none | NVIDIA CUDA |
| Disk | `~/.cix/data/` (~50-200MB/project) | same | same |
| Auto-restart | no (use launchd/systemd) | yes | yes |

---

## Server Management

All commands follow the pattern `make server-{mode}-{action}`:

```bash
# Local (native, MPS on Apple Silicon)
make server-local-setup     # first-time setup (installs uv, Python, deps)
make server-local-start     # start server
make server-local-stop      # stop server
make server-local-restart   # restart server
make server-local-status    # check status
make server-local-logs      # tail logs

# Docker (CPU)
make server-docker-start    # start server
make server-docker-stop     # stop server
make server-docker-restart  # restart server
make server-docker-status   # check status
make server-docker-logs     # tail logs

# CUDA (NVIDIA GPU)
make server-cuda-start      # start server
make server-cuda-stop       # stop server
make server-cuda-restart    # restart server
make server-cuda-status     # check status
make server-cuda-logs       # tail logs
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

### Pre-built images

Ready-to-use images are available on Docker Hub:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | multi-arch | default, recommended |
| `dvcdsys/code-index:arm64` | arm64 | Mac M1/M2/M3, Orange Pi, Raspberry Pi |
| `dvcdsys/code-index:amd64` | amd64 | x86-64 servers, VMs |
| `dvcdsys/code-index:cuda` | amd64 | NVIDIA GPU servers |

### 4. Use your image

Update `docker-compose.yml` to reference your image instead of building locally:

```yaml
services:
  api:
    image: yourname/code-index:latest
```

Then start as usual:

```bash
make server-docker-start
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
make server-local-start             # local
make server-docker-start            # Docker
make server-cuda-start              # CUDA
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

## GPU Acceleration (CUDA)

A CUDA-enabled image is available for servers with NVIDIA GPUs. Inference runs on GPU automatically — no configuration needed.

**Docker Hub:** [`dvcdsys/code-index:cuda`](https://hub.docker.com/r/dvcdsys/code-index/tags)

**Host requirements:**

- NVIDIA GPU with driver **>= 525** (CUDA 12.6 compatible)
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html) installed on the host

**Docker Compose:**

```bash
make server-cuda-start
# or manually:
docker compose -f docker-compose.cuda.yml up -d
```

**Portainer:** use `portainer-stack-cuda.yml` — deploy as a new stack with `API_KEY` env variable set.

```bash
make server-cuda-stop       # stop
make server-cuda-restart    # restart
make server-cuda-status     # check status
make server-cuda-logs       # tail logs
```

---

## License

MIT