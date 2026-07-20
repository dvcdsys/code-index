# Configuration Reference

Complete environment-variable surface for `cix-server`. The
operator-facing template lives in `.env.example`; the variables below
are the authoritative list. README's *Configuration* section keeps
only the must-know subset — for everything else, this is the doc.

Anything in the **Tuning** group is also overridable at runtime from
the dashboard's **Server** page (admin only). Dashboard writes go to
the SQLite `runtime_config` table and trigger a sidecar restart; the
env-var values become the boot-time fallback.

---

## Auth + bootstrap

| Variable | Default | Description |
|---|---|---|
| `CIX_API_KEY` | — | Header API key for direct CLI / CI traffic. On first boot it's imported as the bootstrap admin's `env-bootstrap` API key. |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` | — | **Required on a fresh DB.** Seeds the first admin user. Ignored once the `users` table is non-empty. |
| `CIX_BOOTSTRAP_ADMIN_PASSWORD` | — | **Required on a fresh DB.** The user is flagged `must_change_password=true`, so this only works for the first login. |
| `CIX_AUTH_DISABLED` | `false` | **Dev only.** Skips auth on every endpoint — every request behaves as admin. Never set in production. |

On a fresh DB the server **refuses to start** unless both
`CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` are
set. After first login, drop them from `.env` — the user lives in
the DB.

## Networking + storage

| Variable | Default | Description |
|---|---|---|
| `CIX_PORT` | `21847` | Listen port (both Docker images bake this in). |
| `CIX_SQLITE_PATH` | `/data/sqlite/projects.db` | SQLite path. Suffixed with the model-safe name on open. |
| `CIX_CHROMA_PERSIST_DIR` | `/data/chroma` | Vector store directory. |
| `CIX_GGUF_CACHE_DIR` | `/data/models` | Where downloaded GGUF files live. |
| `CIX_PUBLIC_URL` | — | Externally-reachable URL used to build GitHub webhook delivery URLs. Empty disables webhook URL display. |

## Indexing

| Variable | Default | Description |
|---|---|---|
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | HuggingFace GGUF repo (or absolute path to a `.gguf`). |
| `CIX_MAX_FILE_SIZE` | `524288` | Skip files larger than this (bytes). |
| `CIX_EXCLUDED_DIRS` | `node_modules,.git,.venv,...` | Comma-separated dirs always skipped. |
| `CIX_LANGUAGES` | all | Comma-separated allow-list of chunker languages. Empty = all baked-in. See [`LANGUAGES.md`](LANGUAGES.md). |
| `CIX_EMBED_INCLUDE_PATH` | `true` | Path/language/symbol preamble before each chunk. Toggling requires `cix reindex --full`. |
| `CIX_MAX_CHUNK_TOKENS` | `1500` | Max chunk size before falling back to sliding window. Must stay ≤ `CIX_LLAMA_CTX`. |

## llama-server sidecar

| Variable | Default | Description |
|---|---|---|
| `CIX_EMBEDDINGS_ENABLED` | `true` | Set `false` to boot without the sidecar (read-only mode; auth/dashboard/symbol search keep working). |
| `CIX_LLAMA_BIN_DIR` | `/app` (Docker) / `<exe>/llama` (native) | Directory containing `llama-server` + dylibs. |
| `CIX_LLAMA_TRANSPORT` | `unix` | `unix` or `tcp`. Auto-falls-back to TCP if the socket path is too long. |
| `CIX_LLAMA_SOCKET` | `${TMPDIR}/cix-llama-<pid>.sock` | Unix socket path. macOS `sun_path` cap = 104 bytes. |
| `CIX_LLAMA_CTX` | `2048` | `--ctx-size` passed to llama-server. |
| `CIX_N_GPU_LAYERS` | `-1` darwin / `0` else / `99` Docker CUDA | `99` offloads all layers; `0` forces CPU. |
| `CIX_LLAMA_STARTUP_TIMEOUT` | `60` | Seconds to wait for the sidecar's readiness probe. |
| `CIX_GGUF_PATH` | auto-resolve | Absolute path to a GGUF file. Empty → cache lookup → HF download. |
| `CIX_BOOTSTRAP_GGUF_PATH` | — | Optional. If set, cix imports this `.gguf` into `CIX_GGUF_CACHE_DIR` once (atomic `.partial → rename`) and ignores the env on subsequent boots. Useful for air-gapped or rate-limited environments. |

## Tuning (also editable from `/dashboard/server`)

| Variable | Default | Description |
|---|---|---|
| `CIX_LLAMA_THREADS` | `0` (auto = `runtime.NumCPU()/2`) | CPU threads passed to llama-server. |
| `CIX_LLAMA_BATCH` | `0` (match `CIX_LLAMA_CTX`) | `-b` batch size. |
| `CIX_MAX_EMBEDDING_CONCURRENCY` | `5` | Embedding queue parallelism. Drop to `1` if the GPU contends. |
| `CIX_EMBEDDING_QUEUE_TIMEOUT` | `300` | Seconds before a queued embedding request is failed. |

## Workspaces & GitHub repos

Both surfaces ship in every release — there is no opt-in flag. The
encryption key for `github_tokens` is required on first use; if neither
`CIX_SECRET_KEY` nor `CIX_SECRET_KEYFILE` is set the server auto-generates
one under `<CIX_SECRETS_DATA_DIR>/.secret_key`.

| Variable | Default | Purpose |
|---|---|---|
| `CIX_REPOS_DIR` | `<sqlite parent>/repos` | Base directory for cloned GitHub repos. Each clone lives at `<dir>/repos/<path_hash>/`. Point this at a dedicated volume — cloned repos can be large. |
| `CIX_WORKSPACES_DATA_DIR` | — | Legacy alias for `CIX_REPOS_DIR` (used when the latter is unset). Prefer `CIX_REPOS_DIR`. |
| `CIX_WORKER_CONCURRENCY` | `2` | Parallel clone/index workers. |
| `CIX_SECRET_KEY` | (auto-generate) | 32-byte AES key for GitHub token encryption. Hex or base64. |
| `CIX_SECRET_KEYFILE` | — | Alternative — path to a 0600-perm key file. |
| `CIX_SECRETS_DATA_DIR` | `dirname(CIX_SQLITE_PATH)` | Where the auto-generated keyfile lives. |
| `CIX_DEFAULT_POLL_INTERVAL` | `5m` | Default git-polling cadence for polling repos without a per-repo interval. Go duration string. |
| `CIX_MIN_POLL_INTERVAL` | `60s` | Floor applied to every effective poll interval. Go duration string. |
| `CIX_POLL_SCHEDULER_TICK` | `30s` | How often the shared poll scheduler scans for due repos. Go duration string. |

See [`WORKSPACES.md`](WORKSPACES.md) for the operator guide,
[`WEBHOOKS.md`](WEBHOOKS.md) for webhook lifecycle, and
[`POLLING.md`](POLLING.md) for the polling alternative.

## Version-check banner

| Variable | Default | Description |
|---|---|---|
| `CIX_VERSION_CHECK_ENABLED` | `true` | Set `false` to disable the outbound GitHub release poll. |
| `CIX_VERSION_CHECK_INTERVAL` | `6h` | Go duration string (`30m`, `12h`, …). |
| `CIX_VERSION_CHECK_REPO` | `dvcdsys/code-index` | Override only when running a fork with its own release stream. |

See [`UPDATES.md`](UPDATES.md) for how the banner works end-to-end.

## Resource usage

| | Native (Apple Silicon) | Docker (CPU) | Docker (CUDA) |
|--|---|---|---|
| Image size | n/a | ~21 MB | ~1.0 GB |
| Memory (idle) | ~1 GB | ~1 GB | ~1 GB (system) + ~0.7 GB VRAM |
| Memory (indexing) | up to 2 GB | up to 2 GB | up to 2 GB system + ~0.7 GB VRAM |
| GPU | Metal | none | NVIDIA CUDA 12.x |
| Disk | `~/.cix/data/` (~50–200 MB/project) | same (mounted volume) | same |
| Auto-restart | `launchd` agent, set up by `install-server.sh` (see [`SETUP_MACOS_NATIVE.md`](SETUP_MACOS_NATIVE.md)) | yes | yes |

## Switching embedding models

The server ships with `awhiteside/CodeRankEmbed-Q8_0-GGUF` — a
Q8-quantized build of CodeRankEmbed (137M params, 768d, ~145 MB on
disk, ~0.5–0.7 GB idle VRAM/RAM). Inference runs via the
`llama-server` sidecar, so **only GGUF repositories are supported**.
Plain PyTorch / `sentence-transformers` repos won't work.

You can switch in two places:

- **Dashboard → Server → Embedding model.** Pick from the on-disk
  cache (the dropdown lists `CIX_GGUF_CACHE_DIR`/*.gguf), or paste a
  HuggingFace repo or absolute path. **Save & Restart** drains,
  restarts the sidecar, and turns existing project cards red ("Stale
  model") until you reindex.
- **Env / `.env` file.** Set `CIX_EMBEDDING_MODEL=<repo-or-path>` and
  restart. The dashboard's runtime override (if any) wins; the env
  value becomes the bootstrap default.

ChromaDB and SQLite paths are suffixed by a sanitised form of the
model name (e.g. `projects_awhiteside_coderankembed_q8_0_gguf.db`).
This isolates vector spaces per model — switching back and forth
keeps old indices intact and avoids dim-mismatch errors.
Re-indexing under a model is not free (chunk count × embedding
latency), but you don't lose state.

## Related files

- `server/internal/config/config.go` — env-var loading + defaults
- `server/internal/runtimecfg/` — dashboard-editable overrides
- `.env.example` — copy-paste-ready template
- [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md) — production hardening
