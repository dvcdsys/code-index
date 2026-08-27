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
| `CIX_DATA_DIR` | `~/.cix/data` (`/tmp/cix-data` with no `$HOME`) | Base directory the other storage defaults are derived from. Ignored where a path is set explicitly — which the containers do, so it is a native-install variable in practice. |
| `CIX_BIND_ADDR` | — | Interface to listen on, as a bare address with no port. Empty means every interface, which is what a container needs; set `127.0.0.1` to make the server reachable only from the machine it runs on. The macOS app writes `127.0.0.1` at first run and exposes a menu toggle. |
| `CIX_SQLITE_PATH` | `/data/sqlite/projects.db` | System SQLite database — opened literally, with no model suffix appended. (A pre-0.x per-model filename is migrated to this path on first boot.) |
| `CIX_CHROMA_PERSIST_DIR` | `/data/chroma` | Legacy chromem-go store. Read on startup for the one-time import into the SQLite vector store, then left untouched as the rollback path. See [VECTORSTORE.md](VECTORSTORE.md). |
| `CIX_VECTORS_DIR` | sibling of `CIX_CHROMA_PERSIST_DIR` (`/data/vectors`) | Vector store directory: one SQLite database per embedding namespace. |
| `CIX_VECTOR_MMAP_SIZE` | `0` (off) | `PRAGMA mmap_size` for the vector store, in bytes. Roughly 40% lower search latency in exchange for resident memory — mapped database pages count in RSS. |
| `CIX_VECTOR_SCAN_QUANT` | `true` | Scan a compact int8 copy of each vector instead of the float32 original, rescoring the shortlist against the originals. Every score returned is the exact cosine; which documents reach the shortlist is an approximation, measured at recall 1.000 against exact search (see `doc/VECTORSTORE.md`). 3.4x fewer bytes read per query at 2048 dimensions, in exchange for roughly a quarter more disk. Set `false` if the volume cannot take it; existing copies are then ignored, and writes remove the copies of rows they touch so re-enabling rebuilds instead of trusting stale data. |
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
| `CIX_INDEX_EMBED_BATCH_CHUNKS` | `0` (built-in default) | Chunks per embedding batch during indexing. Also editable at runtime from the dashboard. |
| `CIX_CHUNK_MAX_CONCURRENT` | `0` (built-in default) | Files chunked in parallel. Also editable at runtime from the dashboard. |

## Search statistics

| Variable | Default | Description |
|---|---|---|
| `CIX_SEARCH_STATS_ENABLED` | `false` | Deploy-time starting position for per-project search counters. `true` starts a server collecting from its first boot — for provisioning a fleet. |

Off by default: an upgraded server does not quietly start collecting. An admin
can also switch it on from the statistics page, which takes effect immediately
and **outranks this variable from then on** — otherwise the next container start
carrying the old environment would undo their decision. See
`doc/SEARCH_STATISTICS.md`.

Counters live in `searchstats.db`, created next to `CIX_SQLITE_PATH` — so a
deployment that mounts a volume for the system database already covers this one.
There is no separate path variable, deliberately: the two files have to share a
volume or the counters vanish on the next container restart.

The file is separate from `projects.db` because search must keep serving while
the system database is frozen for a compaction, and a counter has no business
queueing behind the indexer's write lock. See `doc/SEARCH_STATISTICS.md`.

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
| `CIX_LLAMA_CACHE_RAM` | `0` (disabled) | llama-server's host prompt cache in MiB (`--cache-ram`). Embeddings get no prompt reuse from it, and upstream's 8192 default has OOM-killed this server; `-1` is unlimited. |
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

### Managed tunnels

A managed tunnel gives a NAT-ed server a public URL for webhook delivery.
The binaries are in both Docker images; on a native install they come from
`PATH` unless a path is given here.

| Variable | Default | Purpose |
|---|---|---|
| `CIX_TUNNEL_CLOUDFLARE_BIN_PATH` | `cloudflared` (from `PATH`; images set `/cloudflared`) | `cloudflared` executable. |
| `CIX_TUNNEL_CLOUDFLARE_METRICS_ADDR` | `127.0.0.1:21848` | Where `cloudflared` exposes its metrics endpoint, which is how readiness is detected. |
| `CIX_TUNNEL_CLOUDFLARE_STARTUP_TIMEOUT` | `30` | Seconds to wait for the tunnel to come up. |
| `CIX_TUNNEL_NGROK_BIN_PATH` | `ngrok` (from `PATH`) | `ngrok` executable. |
| `CIX_TUNNEL_NGROK_STARTUP_TIMEOUT` | `30` | Seconds to wait for the tunnel to come up. |
| `CIX_TUNNEL_BIN_MANAGED` | `false` | Let the server download and update the tunnel binary itself. |
| `CIX_TUNNEL_BIN_DIR` | `<SQLite dir>/tunnel-bin` | Where a managed binary is kept. |

## Database maintenance

Reclaim and compaction are normally driven from **Server → Resources →
Database**; these variables exist for deployments nobody opens a dashboard
for. A schedule saved in the dashboard overrides the environment, and an
invalid cron expression is refused at startup rather than at the first tick.

| Variable | Default | Purpose |
|---|---|---|
| `CIX_DB_MAINTENANCE_CRON` | — (no automatic run) | Default schedule for the database tasks, as a crontab expression. |
| `CIX_DB_MAINTENANCE_MIN_FREE_PERCENT` | `25` | How much of the file must be freelist waste before a scheduled run bothers. |
| `CIX_DB_MAINTENANCE_MIN_FREE_BYTES` | `256 MiB` | The same threshold in absolute bytes; both have to be met. |

Compaction takes the server **read-only and then restarts it** — read
[`DATABASE_MAINTENANCE.md`](DATABASE_MAINTENANCE.md) before scheduling one
on a server other people use.

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
| Image size (compressed pull) | n/a | ~80 MB | ~1.1 GB |
| Memory, `cix-server` idle | tens of MB | tens of MB | tens of MB |
| Memory, `llama-server` sidecar | ~0.5–0.7 GB | ~0.5–0.7 GB | ~0.2 GB system + ~0.7 GB VRAM |
| Memory (indexing) | up to ~1.5 GB total | up to ~1.5 GB total | same + ~0.7 GB VRAM |
| GPU | Metal | none | NVIDIA CUDA 12.x |
| Disk | `~/.cix/data/` (~50–200 MB/project) | same (mounted volume) | same |
| Auto-restart | `launchd` agent, installed by **cix.app** (see [`MACOS_APP.md`](MACOS_APP.md)) or by `install-server.sh` for a from-source build | yes | yes |

Since 0.13.0 the server's own resident memory no longer scales with the
index: vectors are read from SQLite per query instead of being loaded into
the heap at startup, and idle connections are closed after 30 seconds
([`VECTORSTORE.md`](VECTORSTORE.md) measures 19 MB idle on a
312k-document index that cost 2209 MB before). The one setting that
brings index-proportional memory back on purpose is
`CIX_VECTOR_MMAP_SIZE`. What is left at idle is the embedding sidecar.

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

Vector spaces are isolated per model by **directory**, not by filename:
each embedding namespace — provider kind, model slug, optional variant —
gets its own `<CIX_VECTORS_DIR>/<kind>/<model-slug>/vectors.db`
(`Config.VectorDirFor`). The system SQLite database is shared and not
model-specific. Switching back and forth therefore opens a different
`vectors.db`, keeps old indices intact and makes a dim-mismatch
impossible. Re-indexing under a model is not free (chunk count ×
embedding latency), but you don't lose state. See
[`VECTORSTORE.md`](VECTORSTORE.md).

## Related files

- `server/internal/config/config.go` — env-var loading + defaults
- `server/internal/runtimecfg/` — dashboard-editable overrides
- `.env.example` — copy-paste-ready template
- [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md) — production hardening
