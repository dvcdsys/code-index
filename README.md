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

cix-server (Go) — server/
├── llama-server (llama.cpp sidecar) → embeddings (CodeRankEmbed Q8_0 GGUF, 768d)
├── chromem-go                       → vector store (cosine similarity)
├── gotreesitter                     → AST chunking (200+ languages)
└── modernc.org/sqlite               → project metadata, symbols, file hashes
```

The server is a pure-Go static binary. The CLI is a thin Go binary that talks to it over HTTP.
The `llama-server` sidecar (from upstream [llama.cpp](https://github.com/ggml-org/llama.cpp)) handles embeddings — the Go process starts it as a child process and communicates via Unix socket.

---

## Quick Start

### 1. Start the API Server

Three deployment options:

| Mode | Best for | GPU acceleration | Prerequisites |
|------|----------|-----------------|---------------|
| **Docker (CPU)** | any OS, development | none | Docker |
| **Docker (CUDA)** | NVIDIA GPU servers | CUDA | Docker, NVIDIA Container Toolkit |
| **Native (macOS)** | Apple Silicon — full Metal GPU | Metal | Go 1.24+, Xcode CLT |

#### Docker (CPU)

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
cp .env.example .env
# Edit .env — set CIX_API_KEY to a random string
docker compose up -d
```

```bash
curl http://localhost:21847/health   # → {"status": "ok"}
```

#### Docker (CUDA — NVIDIA GPU)

See [GPU Acceleration (CUDA)](#gpu-acceleration-cuda) section below.

```bash
docker compose -f docker-compose.cuda.yml up -d
```

#### Native macOS (Apple Silicon — Metal GPU)

> **Why not Docker?** Docker Desktop on macOS runs containers inside a Linux VM — Metal GPU is **not accessible** from within a container. For full Apple Silicon GPU acceleration you must run the server natively.

**Prerequisites:** Go 1.24+, Xcode Command Line Tools

```bash
xcode-select --install   # if not already installed
```

**Step 1 — Build binary + download Metal-enabled llama-server (once)**

```bash
cd server
make bundle
# Outputs:
#   dist/cix-darwin-arm64/cix-server
#   dist/cix-darwin-arm64/llama/llama-server  (includes libggml-metal.dylib)
```

**Step 2 — Configure**

```bash
cp .env.example .env
# Edit .env — set at minimum:
#   CIX_API_KEY=cix_<your-random-key>
#   CIX_N_GPU_LAYERS=99      ← offload all layers to Metal
```

**Step 3 — Run**

```bash
cd server && make run
# Reads .env from repo root, sets CIX_LLAMA_BIN_DIR automatically.
```

```bash
curl http://localhost:21847/health   # → {"status": "ok"}
```

| Variable | Recommended | Notes |
|---|---|---|
| `CIX_N_GPU_LAYERS` | `99` | Offload all layers to Metal; `0` = CPU only |
| `CIX_LLAMA_BIN_DIR` | set by `make run` | Path to the `llama-server` binary dir |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Enable GPU embeddings (default) |

> [!TIP]
> `make run` always runs `make bundle` first (no-op if already built), so it's safe to use after any `git pull`.

**Auto-start with launchd** (optional — run server in the background on login):

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
# Replace /ABSOLUTE/PATH/TO and YOUR_USER/YOUR_KEY with real values, then:
launchctl load ~/Library/LaunchAgents/com.cix.server.plist
launchctl start com.cix.server
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
cix config set api.key $(grep CIX_API_KEY .env | cut -d= -f2)
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
  --exclude <path>     exclude file or directory (repeatable)
  --lang <language>    filter by language (repeatable)
  --limit, -l <n>      max results (default: 10)
  --min-score <0-1>    minimum relevance score (default: 0.4)
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

**Embeddings** — each chunk is encoded with a GGUF build of CodeRankEmbed (default: [awhiteside/CodeRankEmbed-Q8_0-GGUF](https://huggingface.co/awhiteside/CodeRankEmbed-Q8_0-GGUF); 768d, 8192 token context, ~145MB on disk) via the `llama-server` sidecar (llama.cpp). Queries get a `"Represent this query for searching relevant code: "` prefix for asymmetric retrieval.

**Incremental reindex** — uses SHA256 file hashes. Only new or changed files are re-embedded. Deleted files are removed from the index.

**Filtering** — respects `.gitignore` and `.cixignore`, skips common dirs (`node_modules`, `.git`, `.venv`, etc.), skips files >512KB and empty files. Per-project configuration via `.cixconfig.yaml` (see below).

---

## Tuning Search Quality

### `--min-score` threshold

`cix` defaults to `--min-score 0.4`. This is calibrated for **CodeRankEmbed-Q8_0** with the path-aware embedding format (`CIX_EMBED_INCLUDE_PATH=true`, default).

A typical score landscape on this codebase:

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
> If you switched embedding models or toggled `CIX_EMBED_INCLUDE_PATH`, rerun `cix reindex --full` and recalibrate. Old vectors and new vectors live in the same store but score differently.

### `--exclude` for noisy directories

Repos with vendored code, fixtures, or legacy migrations can pull unrelated paths into top results because path tokens contribute to scoring. Two options:

```bash
# One-off exclude for a single search
cix search "main entry point" --exclude legacy --exclude bench/fixtures

# Permanent exclude — add to .cixignore (skips indexing entirely)
echo "legacy/" >> .cixignore
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

See `.env.example` for a complete template.

| Variable | Default | Description |
|----------|---------|-------------|
| `CIX_API_KEY` | — | Bearer token for API auth |
| `CIX_PORT` | `21847` | API server port |
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | HuggingFace GGUF repo |
| `CIX_MAX_FILE_SIZE` | `524288` | Skip files larger than this (bytes) |
| `CIX_EXCLUDED_DIRS` | `node_modules,.git,.venv,...` | Comma-separated dirs to skip |
| `CIX_N_GPU_LAYERS` | auto | `99` offloads all layers to GPU; `0` forces CPU |
| `CIX_GGUF_CACHE_DIR` | `/data/models` | Where the GGUF file is cached |
| `CIX_LLAMA_BIN_DIR` | `/app` | Directory containing `llama-server` binary |
| `CIX_LLAMA_STARTUP_TIMEOUT` | `60` | Seconds to wait for llama-server ready |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Set to `false` to skip embeddings (CPU-only mode) |
| `CIX_CHROMA_PERSIST_DIR` | `/data/chroma` | Vector store path |
| `CIX_SQLITE_PATH` | `/data/sqlite/projects.db` | SQLite database path |

Data is stored in `/data` inside the container — mount a volume to persist it.

### Resource Usage

| | Local (native) | Docker (CPU) | CUDA |
|--|----------------|--------------|------|
| Memory (idle) | ~1GB | ~1GB | ~1GB |
| Memory (indexing) | up to 2GB | up to 2GB | up to 2GB |
| CPU | no limit | `CPUS` env var (default: 2) | unlimited |
| GPU | Metal (Apple Silicon) | none | NVIDIA CUDA |
| Disk | `~/.cix/data/` (~50-200MB/project) | same | same |
| Auto-restart | no (use launchd/systemd) | yes | yes |

### Switching Embedding Models

The server ships with `awhiteside/CodeRankEmbed-Q8_0-GGUF` — a Q8-quantized build of CodeRankEmbed (137M params, 768 dims, ~145MB on disk, ~650MB idle VRAM/RAM). Inference runs via the `llama-server` sidecar (llama.cpp), so **only GGUF repositories are supported**. Plain PyTorch/`sentence-transformers` repos will not work.

To switch models:
1. Stop the server (`make server-local-stop` or `make server-docker-stop`).
2. Set `EMBEDDING_MODEL` in `.env` to a Hugging Face repo that contains a `.gguf` file, for example:
   ```bash
   # code-specialised (default)
   EMBEDDING_MODEL=awhiteside/CodeRankEmbed-Q8_0-GGUF
   # smaller general-purpose alternative
   EMBEDDING_MODEL=nomic-ai/nomic-embed-text-v1.5-GGUF
   ```
3. *(Optional)* Pre-cache the new model into the Docker image:
   `docker compose build --build-arg EMBEDDING_MODEL=<repo>`.
4. Start the server and re-index your projects.

> [!NOTE]
> ChromaDB and SQLite paths are suffixed by a sanitised form of the model name (e.g. `projects.db_awhiteside_coderankembed_q8_0_gguf`). This isolates vector spaces per model, so switching back and forth keeps old indices intact and avoids dim-mismatch errors.

> [!TIP]
> **Apple Silicon:** Docker cannot access Metal GPU — run natively with `cd server && make run` (see [Native macOS (Apple Silicon — Metal GPU)](#native-macos-apple-silicon--metal-gpu) above). The bundled `llama-server` includes `libggml-metal.dylib`; set `CIX_N_GPU_LAYERS=99` for full Metal offload.
> **Linux NVIDIA:** use the CUDA image (`docker-compose.cuda.yml`). Force CPU with `CIX_N_GPU_LAYERS=0`.

---

## Server Management

```bash
docker compose up -d                           # start (CPU)
docker compose -f docker-compose.cuda.yml up -d  # start (CUDA)
docker compose logs -f                         # tail logs
docker compose down                            # stop
```

Developer builds (from source):

```bash
cd server && make build        # build cix-server binary
cd server && make bundle       # build + fetch llama-server
cd server && make test-gate    # parity gate (requires GGUF)
make docker-build-cuda         # build + push CUDA image
```

---

## Building and Publishing to Docker Hub

```bash
docker login
make docker-build-cuda   # builds + pushes server/Dockerfile.cuda → dvcdsys/code-index:go-cu128
```

Pre-built images on Docker Hub:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | linux/amd64 + linux/arm64 | CPU, `CIX_EMBEDDINGS_ENABLED=false` |
| `dvcdsys/code-index:cu128` | linux/amd64 | NVIDIA GPU (CUDA 12.8), full embeddings |
| `dvcdsys/code-index:0.2-python-legacy` | linux/amd64 | Frozen Python build, rollback only |

See `doc/DOCKER_TAGS.md` for the full tag lifecycle policy.

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
cix config set api.key $(grep CIX_API_KEY /path/to/code-index/.env | cut -d= -f2)
```

**`connection refused`**
```bash
curl http://localhost:21847/health              # check if server is up
docker compose up -d                           # start (CPU)
docker compose -f docker-compose.cuda.yml up -d  # start (CUDA)
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
- Lower the threshold: `cix search "query" --min-score 0.2` (default is `0.4`; see [Tuning Search Quality](#tuning-search-quality))
- Docker mode: run `cix list` to verify the project is registered

---

## Releases

CLI and server ship on independent tag streams:

| Component | Tag pattern | Workflow | Artifact |
|---|---|---|---|
| CLI (`cix`) | `cli/v*` (e.g. `cli/v0.4.0`) | `release-cli.yml` | `cix-{darwin,linux}-{amd64,arm64}.tar.gz` on a GitHub Release |
| Server (`cix-server`) | `server/v*` (e.g. `server/v0.3.0`) | `release-server.yml` | Docker images on Docker Hub (`:latest`, `:cu128`) |

Bare `v*` tags are the historical pre-split CLI line — the installer
still falls back to them when no `cli/v*` release exists, but no new
bare-`v*` tags should be created.

### Cutting a CLI release

```bash
git tag cli/v0.4.0
git push origin cli/v0.4.0
```

GitHub Actions builds binaries for macOS + Linux (amd64 + arm64),
uploads them to a release named `cli/v0.4.0`, and the installer
automatically picks them up on the next run.

### Cutting a server release

See `doc/DOCKER_TAGS.md` and the T9 step in `.claude/CLAUDE.md`.

### Local cross-build (no release)

```bash
cd cli
make release VERSION=v0.4.0
```

Produces archives in `cli/dist/` plus `checksums.txt`. Useful for
testing the artifact format before pushing a tag.

Supported targets: `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`.

---

## GPU Acceleration (CUDA)

A CUDA-enabled image is available for servers with NVIDIA GPUs. Inference runs on GPU automatically — no configuration needed.

### VRAM Usage (CodeRankEmbed Q8_0 GGUF, RTX 3090)

With the GGUF backend the footprint is near-constant: weights (~200-250 MB) plus
the pre-allocated context (`n_ctx=8192`, ~200-400 MB) give a **~0.5-0.7 GB**
idle draw. Embedding calls do not spike VRAM the way fp16 PyTorch attention
used to — sequence length and batch size only change latency, not peak memory.

`MAX_CHUNK_TOKENS` still caps the length of each code chunk (1 token ≈ 4 chars)
and must stay ≤ `n_ctx` (8192). `MAX_EMBEDDING_CONCURRENCY` should stay at `1`
for single-GPU setups — llama.cpp serialises through one context.

See [`doc/vram-profiling.md`](doc/vram-profiling.md) for methodology and numbers.

**Docker Hub:** [`dvcdsys/code-index:cu128`](https://hub.docker.com/r/dvcdsys/code-index/tags)

Tags: `cu128` (stable) and `v<version>-cu128` (pinned). Image size: ~1.66 GB
(3-stage build: nvidia/cuda:12.8.1-base + libcublas + llama-server binaries + Go binary).

See `doc/DOCKER_TAGS.md` for the full tag lifecycle.

**Host requirements:**

- NVIDIA GPU with driver **>= 520** (CUDA 12.x compatible)
- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html) installed on the host

**Docker Compose:**

```bash
docker compose -f docker-compose.cuda.yml up -d
```

**Portainer:** use `portainer-stack-cuda.yml` — deploy as a new stack with `API_KEY` env variable set.

---

## License

MIT