# Self-hosting cix for a team

A practical guide for **DevOps / platform engineers** standing up `cix` as
**shared infrastructure** — one server that a whole team's CLIs, IDEs, and
agents point at, rather than each developer running their own instance.

If you just want cix on your laptop, the [README Quick Start](../README.md#quick-start)
is enough. Read this when cix becomes a service other people depend on.

> **Scope.** This is the operational/integration guide: topology, env,
> volumes, networking, backups, upgrades. For the threat model and
> hardening checklist see [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md);
> for the full env-var surface see [`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md);
> for the authorization rules see the access model in
> [`../README.md`](../README.md#authorization-model) and `.claude/CLAUDE.md`.

---

## 1. What you are deploying

A single `cix-server` container exposes:

- `:21847` — REST API (Bearer API key) + cookie-session web dashboard at
  `/dashboard`, Swagger UI at `/docs`.
- An embedded indexing pipeline (tree-sitter chunking → embeddings →
  chromem-go vector store + SQLite FTS5/BM25 mirror).
- An embedding backend — by default a **bundled llama.cpp sidecar** (no
  external calls), optionally **Voyage AI** or an **OpenAI-compatible**
  endpoint (see §6).

Everyone on the team connects to *this one server*:

```
  developers' CLIs / IDEs / agents ─┐
  (cix config set api.url …)        │  HTTPS
  CI jobs (CIX_API_URL + key)       ├────────►  reverse proxy / TLS
  Claude Code plugin                ┘            │
                                                 ▼
                                       cix-server :21847  ──►  /data (sqlite+chroma)
                                                            └►  embedding provider
```

Two images, pick one (never merge them):

| Image | Base | Size | Runtime user | Use |
|---|---|---|---|---|
| `dvcdsys/code-index:latest` | distroless static | ~40 MB | `65532:65532` | CPU-only |
| `dvcdsys/code-index:cu128` | distroless cc + CUDA libs | ~1.0 GB | `1001:1001` | NVIDIA GPU |

See [`DOCKER_TAGS.md`](DOCKER_TAGS.md) for the full tag lifecycle.

---

## 2. Prerequisites

- Docker Engine 24+ with Compose v2.
- For the CUDA image: an NVIDIA GPU, recent driver, and the
  **NVIDIA Container Toolkit** installed on the host (`nvidia-ctk`).
- A persistent disk for `/data` (SQLite + chroma vectors grow with the
  number and size of indexed repos).
- DNS + TLS termination if the team reaches it over the network
  (reverse proxy — see §7).
- For server-side workspace cloning of private repos: a GitHub PAT
  (configured in-app, encrypted at rest — see [`WORKSPACES.md`](WORKSPACES.md)).

---

## 3. Bring-up

### 3a. CPU

```bash
git clone https://github.com/dvcdsys/code-index.git
cd code-index
cp .env.example .env      # then edit — see §4
docker compose pull       # `up -d` only pulls when the image is MISSING locally
docker compose up -d
docker compose logs -f code-index-api   # watch for "listening on :21847"
```

### 3b. GPU (CUDA)

```bash
docker compose -f docker-compose.cuda.yml pull
docker compose -f docker-compose.cuda.yml up -d
```

The CUDA compose sets `CIX_N_GPU_LAYERS=99` (full offload) and reserves one
NVIDIA device. **Verify the GPU is actually used** — a silent CPU fallback
is a real failure mode:

```bash
docker compose -f docker-compose.cuda.yml logs code-index-api | grep -i "offloaded\|CUDA\|GPU"
nvidia-smi   # cix-server / llama-server should show up while indexing
```

### 3c. First boot

cix-server **refuses to start with an empty users table unless** both
`CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` are set.
On first boot it creates that admin with `must_change_password=true`:

1. Open `http://<host>:21847/dashboard`, log in with the bootstrap creds.
2. Change the password immediately (you are forced to).
3. You can then drop the two bootstrap env vars — the user lives in the DB.

---

## 4. Minimum `.env`

| Variable | Required | Notes |
|---|---|---|
| `CIX_API_KEY` | **yes** | Bearer token CLIs/agents/CI present. Treat as a shared secret; rotate via the dashboard's API Keys page. |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` | first boot | First admin user. |
| `CIX_BOOTSTRAP_ADMIN_PASSWORD` | first boot | Forced change on first login. |
| `CIX_PORT` | no (`21847`) | Container listen port; keep host mapping in sync. |
| `CIX_EMBEDDING_MODEL` | no | Default local GGUF. Leave unless you ship your own model. |
| `CIX_PUBLIC_URL` | if webhooks | Public origin (`https://cix.example.com`) used to build GitHub webhook URLs. |
| `CIX_VOYAGE_API_KEY` / `CIX_OPENAI_API_KEY` | per provider | Only if you switch off the local provider (§6). |
| `CIX_SECRET_KEY` / `CIX_SECRET_KEYFILE` | recommended | AES key for encrypting stored GitHub PATs. Auto-generated if unset — **but then you must back up the generated keyfile** (§8). |

The full surface (sidecar tuning, polling, tunnels, runtime overrides) is in
[`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md). Most tunables are also editable
at runtime from **Dashboard → Server** without a restart.

---

## 5. Persistence, volumes & permissions

The compose files mount two things:

- `${HOME}/.cix/data:/data` — operator-managed bind holding **SQLite**
  (`/data/sqlite/projects.db`) and **chroma vectors** (`/data/chroma`). Back
  this up.
- `cix-models:/data/models` — Docker-managed named volume for the GGUF model
  cache. Downloaded once; survives `docker compose down` (not `down -v`).

**UID matters.** The container does not run as root:

- CPU image → `65532:65532`
- CUDA image → `1001:1001`

On Linux the bind directory must be writable by that uid, or the server can't
open the DB — it crash-loops on `unable to open database file`. Either:

```bash
# CPU image
sudo chown -R 65532:65532 ~/.cix/data
# CUDA image
sudo chown -R 1001:1001 ~/.cix/data
```

…or add `user: "0:0"` to the service to fall back to root (less safe). If you
are migrating from an old root-owned volume, `chown` it **before** switching
to the non-root image. See [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md).
`install-server.sh` performs this chown for you (it asks first) — the manual
command is only needed when you drive compose directly. macOS Docker Desktop
maps ownership transparently, so none of this applies there.

**The named volume follows the same uid.** Docker copies the image's
ownership onto `cix-models` only while the volume is still empty, so a cache
that an older *root*-running image already filled stays root-owned and the
next model download fails with `permission denied`. The same applies when one
host switches between the CPU (65532) and CUDA (1001) images. Fix it without
a shell in the image:

```bash
docker run --rm -v <project>_cix-models:/v busybox chown -R 1001:1001 /v
```

**Optional — seed the model offline.** Air-gapped hosts can skip the
first-boot HuggingFace download by binding a `.gguf` read-only at
`/bootstrap/model.gguf` and setting `CIX_BOOTSTRAP_GGUF_PATH=/bootstrap/model.gguf`.
cix copies it into the model cache once, then ignores both the env and the bind.

---

## 6. Choosing an embedding provider for a team

The active provider is selected by an **admin** at **Dashboard → Server →
Embedding provider**. It is server-wide (one provider per server).

| Provider | When it fits a team | Setup |
|---|---|---|
| **Local** (default) | Privacy-sensitive / air-gapped code, predictable cost, you can host CPU or a GPU. No code leaves your infra. | Nothing — it's the default. Add the CUDA image + GPU for throughput. |
| **Voyage AI** | Top-tier code retrieval (`voyage-code-3`) without operating a GPU. Code is sent to `api.voyageai.com`. | Export `CIX_VOYAGE_API_KEY` on the **server**, pick model/dimensions in the dashboard. |
| **OpenAI-compatible** | Reuse an existing OpenAI account or an internal gateway (vLLM, TEI, LocalAI). | Export `CIX_OPENAI_API_KEY`, set `base_url` + model in the dashboard. |

> API keys are **never persisted in the DB** — cix stores only the *name* of
> the env var to read, and resolves it on each embed call. That means the key
> lives only in your orchestrator's secret store / the container env.

> ⚠️ **Switching providers (or changing a provider's model/dimensions)
> changes the embedding space and forces a full reindex of every project.**
> Plan a provider decision *before* a team indexes hundreds of repos, and
> change it during a maintenance window.

---

## 7. Networking, TLS & webhooks

cix has **no built-in TLS** — terminate it at a reverse proxy
(nginx/Caddy/Traefik) and forward to `:21847`. Requirements:

- Forward WebSocket-less plain HTTP; pass through `Authorization` and cookies.
- Set generous timeouts — indexing requests and large workspace searches can
  run for minutes.
- Point developers at the public URL: `cix config set api.url https://cix.example.com`
  and `cix config set api.key <CIX_API_KEY>`.

**GitHub webhooks (workspace auto-reindex).** If the server is reachable from
GitHub, set `CIX_PUBLIC_URL` so webhook URLs are built correctly. If the
server is **behind NAT**, use **Managed Tunnels** (Cloudflare/ngrok,
configured from the dashboard) instead of port-forwarding — see
[`WEBHOOKS.md`](WEBHOOKS.md) and [`WORKSPACES.md`](WORKSPACES.md). Repos that
can't host a webhook can opt into **polling sync** (`CIX_DEFAULT_POLL_INTERVAL`).

---

## 8. Backups & disaster recovery

Back up, in order of importance:

1. **`/data/sqlite/projects.db`** — users, API keys, projects, symbols,
   workspaces, runtime config. Use SQLite online backup or stop-copy-start.
2. **`/data/chroma`** — vector store. Recoverable by reindex, but a backup
   avoids re-embedding everything.
3. **The secret key** (`CIX_SECRET_KEYFILE`, or the auto-generated keyfile
   under the SQLite parent dir). **Losing it invalidates every stored GitHub
   PAT** — they'd all have to be re-entered. Back it up *separately* from the
   DB (don't store the key next to the data it encrypts).

The `cix-models` named volume does **not** need backing up — it re-downloads.

---

## 9. Upgrades

```bash
docker compose pull
docker compose up -d
```

> **Reindex after a server upgrade.** Until the parsing/chunking/embedding
> pipeline stabilizes, an upgrade can change how code is embedded. Trigger a
> reindex (dashboard or `cix reindex`) so every project lands on the new
> pipeline. Within a version, search is consistent once reindexed.

Server and CLI are released on **independent tag streams** (`server/vX.Y.Z`
vs CLI tags) and are wire-compatible across a minor skew — you don't have to
upgrade developer CLIs in lockstep. See [`RELEASES.md`](RELEASES.md).

---

## 10. Sizing & resources

- **CPU image** ships with conservative compose limits (2 GB / 2 CPUs). Raise
  for large monorepos or many concurrent users. Indexing is the heavy phase;
  steady-state search is light.
- **CUDA image** reserves one GPU and a 10 GB memory limit. For VRAM figures
  (CodeRankEmbed Q8_0 on an RTX 3090) see
  [README → GPU Acceleration](../README.md#gpu-acceleration-cuda).
- Embedding concurrency is tunable: `CIX_MAX_EMBEDDING_CONCURRENCY` (default 5)
  and `CIX_EMBEDDING_QUEUE_TIMEOUT`. Drop concurrency to 1 if you see device
  contention.

---

## 11. Health & monitoring

- **Liveness:** the image's own check, `/cix-server -healthcheck` (already
  wired in both compose files — no `curl` needed), GETs `/health` and exits
  0/1. `start_period` is 120 s to allow the first model download.
- **Readiness probe (external):** `GET /health` on `:21847`.
- **Logs:** `docker compose logs -f code-index-api`. Set `CIX_LOG_LEVEL=debug`
  to diagnose indexing or provider issues.
- **Drift indicator** in the dashboard flags projects whose on-disk code has
  diverged from the index.

---

## 12. Portainer

The repo ships Portainer stack files — [`portainer-stack.yml`](../portainer-stack.yml)
(CPU) and [`portainer-stack-cuda.yml`](../portainer-stack-cuda.yml) (GPU).
Create a stack from the file, supply the same env as §4, and deploy. Note that
a CUDA image pull (~1 GB) can time out the Portainer client while the deploy
itself completes — verify via container state rather than blindly retrying.

---

## 13. Security checklist

- [ ] `CIX_API_KEY` is a strong random secret, distributed via your secret manager.
- [ ] Bootstrap admin password changed; bootstrap env vars removed afterward.
- [ ] TLS terminated at the proxy; `:21847` not exposed raw to the internet.
- [ ] `/data` bind owned by the correct non-root uid (65532 or 1001).
- [ ] Secret key backed up *separately* from the DB.
- [ ] Provider API keys (if any) injected from a secret store, never committed.
- [ ] Reviewed the ownership + view-group access model so projects aren't
      over-shared.

Full hardening guidance: [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md).

---

## 14. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `no users in database and the bootstrap admin env vars are not set` | Set both bootstrap vars, restart. |
| Server can't open the DB / read-only errors | `/data` bind not owned by the container uid — `chown` to 65532 (CPU) or 1001 (CUDA). |
| `resolve gguf: mkdir /data/models/…: permission denied` | The `cix-models` volume was initialised by a root-running image (or the other image's uid) — chown it, see §5. |
| GPU image runs but `nvidia-smi` shows no cix process | NVIDIA Container Toolkit missing, or silent CPU fallback — check logs for CUDA offload lines. |
| Webhooks never fire | `CIX_PUBLIC_URL` unset or server behind NAT — use Managed Tunnels or polling. |
| Search results changed/empty after upgrade | Reindex — the pipeline moved. |
| Stored GitHub PATs all rejected after a restore | Secret key wasn't restored alongside the DB. |

More: [README → Troubleshooting](../README.md#troubleshooting).
