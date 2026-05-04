# cix-server (Go)

The Go HTTP server backing cix's indexing + dashboard. Default port is
**21847** (was 8001 during the Python parallel-rollout era; the Python
backend was archived 2026-04 and the parity is no longer meaningful).

## Layout

```
cmd/cix-server/       main + graceful shutdown, version via -ldflags
internal/config/      CIX_* env loader (parity with api/app/config.py)
internal/db/          SQLite schema (1:1 with api/app/database.py) + opener
internal/httpapi/     chi router, middleware, /health and /api/v1/status
Dockerfile            CPU multi-stage, distroless runtime
```

## Build / run / test

```bash
cd server
go build ./...
go vet ./...
go test ./...

# Local run (binds :21847 by default)
CIX_SQLITE_PATH=/tmp/cix.db ./cix-server
# Or with version injected:
go build -ldflags "-X main.version=v0.5.1" -o cix-server ./cmd/cix-server
```

## Docker

```bash
docker build -t cix-server-go:dev --build-arg VERSION=v0.5.1 .
docker run --rm -p 21847:21847 \
  -e CIX_API_KEY=cix_<hex> \
  -e CIX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
  -e CIX_BOOTSTRAP_ADMIN_PASSWORD=<pw> \
  -v cix-data:/data \
  cix-server-go:dev
```

## Environment variables

All are optional; defaults match `api/app/config.py` except `CIX_PORT`.

| Var | Default | Notes |
|---|---|---|
| `CIX_API_KEY` | `""` | Warned at startup if empty; enforced from Phase 2 |
| `CIX_PORT` | `21847` | Listen port. Both Docker images bake this in. |
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | |
| `CIX_CHROMA_PERSIST_DIR` | `/data/chroma` | Name kept for compat; backend changes in Phase 4 |
| `CIX_SQLITE_PATH` | `/data/sqlite/projects.db` | Suffixed with model-safe name on open |
| `CIX_MAX_FILE_SIZE` | `524288` | |
| `CIX_EXCLUDED_DIRS` | see config.go | Comma-separated |
| `CIX_MAX_EMBEDDING_CONCURRENCY` | `5` | Embedding queue parallelism. Recommended for both CPU and single-GPU setups. |
| `CIX_EMBEDDING_QUEUE_TIMEOUT` | `300` | Seconds |
| `CIX_MAX_CHUNK_TOKENS` | `1500` | |

## Endpoints (Phase 1)

- `GET /health` — `{"status":"ok"}`
- `GET /api/v1/status` — includes `server_version`, `api_version`, `projects`, `active_indexing_jobs`

All other routes return `404`. Projects, indexing, search — Phase 2+.

## Phase 3 — embeddings + llama-server sidecar

cix-server supervises a sibling `llama-server` (llama.cpp) process and talks to
it over a unix socket. The llama-server binary + required dylibs ship alongside
`cix-server` in the release bundle — no `brew install`, no system packages.

### Environment variables (Phase 3)

| Var | Default | Notes |
|---|---|---|
| `CIX_GGUF_PATH` | *(auto-resolve)* | Absolute path to the GGUF model. Empty → cache lookup → HF download. |
| `CIX_GGUF_CACHE_DIR` | `~/Library/Caches/cix/models` (darwin) | Where HF downloads land. Respects `XDG_CACHE_HOME`. |
| `CIX_LLAMA_BIN_DIR` | `<exe_dir>/llama` | Where `llama-server` + dylibs live. |
| `CIX_LLAMA_SOCKET` | `${TMPDIR}/cix-llama-<pid>.sock` | Unix socket path. macOS `sun_path` limit = 104 bytes. |
| `CIX_LLAMA_TRANSPORT` | `unix` | `unix` or `tcp`. Auto-falls-back to tcp if the socket path is too long. |
| `CIX_LLAMA_CTX` | `CIX_MAX_CHUNK_TOKENS + 128` | `--ctx-size` passed to llama-server. |
| `CIX_N_GPU_LAYERS` | `-1` on darwin, `0` else | `-1` offloads all layers to Metal. |
| `CIX_LLAMA_STARTUP_TIMEOUT` | `60` | Seconds to wait for readiness probe. |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Set to `false` to boot without spawning llama-server (tests). |

### Build, bundle, gate

```bash
cd api-go-poc
make fetch-llama                   # downloads pinned llama.cpp release
make bundle OS=darwin ARCH=arm64   # builds cix-server + assembles dist/cix-darwin-arm64/
make test-gate                     # runs the Phase 3 parity gate (requires GGUF)
```

`make test-gate` is the Phase 3 exit criterion. It runs the build-tagged
`TestEmbeddingParity` suite, spawning a real llama-server child and asserting
cosine similarity against `bench/results/reference_embeddings.json`:

- mean cosine ≥ 0.999
- min cosine ≥ 0.995

If `CIX_GGUF_PATH` is unset and `bench/results/reference_gguf_path.txt` exists
on disk, config.Validate uses it as the GGUF source (zero-config on the dev
machine that produced the reference vectors).

Pooling is hardcoded to `cls` in the supervisor — this was empirically
matched against `llama-cpp-python` for CodeRankEmbed-Q8_0 during the Phase 3
gate (see comment in `internal/embeddings/supervisor.go`).

### Bundle layout

```
dist/cix-darwin-arm64/
  cix-server
  llama/
    llama-server
    libllama.dylib
    libllama-common.dylib
    libmtmd.dylib
    libggml.dylib
    libggml-base.dylib
    libggml-cpu.dylib
    libggml-metal.dylib
    libggml-blas.dylib
    libggml-rpc.dylib
    (versioned *.0.dylib, *.0.0.X.dylib aliases)
```

### macOS Gatekeeper

A freshly-downloaded `llama-server` can be quarantined by Gatekeeper on first
run, producing a silent kill with no log line. `scripts/fetch-llama.sh`
proactively strips the quarantine attribute; if you moved the bundle around
and hit a hang, clear it manually:

```bash
xattr -dr com.apple.quarantine dist/cix-darwin-arm64/
```

### Out of scope for Phase 3

- `POST /search/semantic` — Phase 4.
- Linux, Windows, darwin-amd64 bundles — later phases.
- Docker / CUDA variant — Phase 5.
- Code signing / notarization — tracked; xattr workaround for now.
