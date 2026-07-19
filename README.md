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
 ╚═════╝╚═╝╚═╝  ╚═╝  CodeIndeX
```

[![Release: Server](https://github.com/dvcdsys/code-index/actions/workflows/release-server.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/release-server.yml)
[![Release: CLI](https://github.com/dvcdsys/code-index/actions/workflows/release-cli.yml/badge.svg)](https://github.com/dvcdsys/code-index/actions/workflows/release-cli.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Hub](https://img.shields.io/docker/pulls/dvcdsys/code-index)](https://hub.docker.com/r/dvcdsys/code-index)

**cix — CodeIndeX.** Search your codebase by meaning, not just text. Self-hosted, embeddings-based, works with any agent or terminal — with a full web dashboard and multi-repo workspace search. Website: [codeindex.app](https://codeindex.app)

```bash
cix search "authentication middleware"
cix search "database retry logic" --in ./api --lang go
cix symbols "UserService" --kind class
```

Or open `http://localhost:21847/dashboard` in your browser.

> [!IMPORTANT]
> **Reindex after upgrading the server.** Until the parsing/chunking/embedding
> pipeline stabilizes, an upgrade can change how code is embedded. A reindex
> brings every project onto the new pipeline; within a version, search is
> consistent once reindexed.

---

## Why

Grep and fuzzy file search work fine for small projects. At scale they break down:

- You have to know what a thing is called to find it
- Results flood with noise from unrelated files
- Agents waste tokens scanning files that aren't relevant

`cix` indexes your code into a vector store using [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) — a model purpose-built for code retrieval. Search queries return ranked snippets with file paths and line numbers, not raw file lists.

---

## What you get

- **`cix-server`** — Go HTTP API with embedded llama.cpp sidecar for embeddings, SQLite for symbols + metadata, chromem-go for vectors, FTS5 BM25 mirror for hybrid ranking. Ships as a single distroless container.
- **Web dashboard** at `/dashboard` — projects, search, users + API keys, runtime sidecar control, drift indicator. Embedded in the server binary. See [`doc/DASHBOARD.md`](doc/DASHBOARD.md).
- **`cix` CLI** — `cix search`/`symbols`/`files`/`workspace …` for terminal + agent use. See [`doc/CLI_REFERENCE.md`](doc/CLI_REFERENCE.md).
- **File watcher** — `cix watch` keeps the index fresh as you edit.
- **Workspaces** — group multiple repos into one named corpus; cix clones them server-side, indexes them, and runs hybrid BM25 + dense search across the union. GitHub webhooks auto-reindex on `push`. See [`workspaces.md`](workspaces.md).
- **Pluggable embeddings** — local llama.cpp by default; Voyage AI or any OpenAI-compatible endpoint optional. See [Embedding providers](#embedding-providers).
- **Ownership + view-group sharing** — every project/workspace has an owner; admins share to named groups. Private by default. See [`doc/DASHBOARD.md`](doc/DASHBOARD.md#authorization-model).
- **Claude Code plugin** — install once and `cix` becomes the agent's default reflex for code search. See [Agent integration](#agent-integration).

---

## Architecture

```
                  ┌────────────────────────────────────┐
                  │  Browser  →  http://host:21847     │
                  │  • /dashboard  • /docs  • /openapi │
                  └────────────┬───────────────────────┘
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│  cix-server (Go, single distroless binary)                      │
│  HTTP/REST + cookie sessions + Bearer API keys                  │
│  ├── auth, admin, api-keys, projects, indexing, search          │
│  ├── workspaces, github-tokens, webhooks                        │
│  └── embedded React dashboard + Swagger UI                      │
│                                                                 │
│  Indexing pipeline                                              │
│  ├── tree-sitter/wasm (AST chunking, 30+ langs)  (wazero)       │
│  ├── embedding provider (local llama.cpp / Voyage / OpenAI)     │
│  ├── chromem-go (cosine similarity vector store)                │
│  └── SQLite FTS5 mirror (BM25) + metadata (modernc/sqlite)      │
└────────────┬─────────────────────────────────────┬──────────────┘
             │ HTTP                                │ Unix socket
             ▼                                     ▼
   cix CLI (Go)                       ┌──────────────────────────┐
   search · symbols · workspace       │  llama-server child proc │
                                      └──────────────────────────┘
```

Pure-Go static binary; CUDA-image variants add a CUDA runtime layer for GPU embeddings. Workspace clones live in `<data-dir>/repos/`.

---

## Quick Start

| Mode | Best for | GPU | Prerequisites |
|------|----------|-----|---------------|
| **Docker (CPU)** | any OS, dev / small repos | none | Docker |
| **Docker (CUDA)** | NVIDIA GPU servers | CUDA 12.x | Docker + NVIDIA Container Toolkit |
| **Native (macOS)** | Apple Silicon w/ full Metal | Metal | Go 1.25+, Xcode CLT |

### 1. Start the server

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
cp .env.example .env
# Edit .env — set CIX_API_KEY, CIX_BOOTSTRAP_ADMIN_EMAIL, CIX_BOOTSTRAP_ADMIN_PASSWORD
docker compose up -d                              # CPU
# docker compose -f docker-compose.cuda.yml up -d # NVIDIA GPU
curl http://localhost:21847/health                # → {"status":"ok"}
```

> [!IMPORTANT]
> On a fresh database the server **refuses to start** unless both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` are set. The admin is created with `must_change_password=true` — you change it on first login, then can drop the env vars.

For Apple Silicon with full Metal acceleration, run natively (Docker Desktop has no Metal access) — see [`doc/SETUP_MACOS_NATIVE.md`](doc/SETUP_MACOS_NATIVE.md). For shared/team deployment, see [`doc/TEAM_DEPLOYMENT.md`](doc/TEAM_DEPLOYMENT.md).

### 2. Log in and mint an API key

Open `http://localhost:21847/dashboard`, sign in with the bootstrap admin, change the password when prompted. ([What's on each page](doc/DASHBOARD.md).)

Then go to **API Keys → Create key**, name the key, and copy the revealed `cix_…` value — it is shown exactly once. The dialog also gives you a ready-to-paste `cix config` connect command for step 3.

### 3. Install + configure the CLI

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
# paste the connect command from the API-key dialog, or:
cix config set server.local.url http://localhost:21847
cix config set server.local.key cix_<key-from-step-2>
cix config set default_server local
```

(Legacy alternative: set `CIX_API_KEY=cix_…` in `.env` before first boot and the server imports it as an API key.)

From source: `cd cli && make build && make install`. Pre-release `develop` channel: [`doc/UPDATES.md`](doc/UPDATES.md#cli-install-channels).

### 4. Index a project and search

```bash
cd /path/to/your/project
cix init                            # registers + indexes + starts the file watcher
cix status                          # wait for: Status: ✓ Indexed

cix search "authentication middleware"
cix symbols "handleRequest" --kind function
cix summary
```

Full command surface: [`doc/CLI_REFERENCE.md`](doc/CLI_REFERENCE.md). Same five modes are on the dashboard's **Search** page.

---

## Embedding providers

cix is self-hosted first: out of the box it embeds with a **bundled
llama.cpp sidecar** and never sends your code anywhere. The backend is
pluggable — switched at runtime from **Dashboard → Server** (admin only).

| Provider | Kind | Default model | Where code goes | API key |
|---|---|---|---|---|
| **Local** *(default)* | `ollama` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | Stays on your machine — bundled `llama-server`, fully offline. GPU via CUDA/Metal. | none |
| **Voyage AI** | `voyage` | `voyage-code-3` | Sent to `api.voyageai.com`. Code-specialized, retrieval-tuned, Matryoshka dims 256–2048, `int8`. | `CIX_VOYAGE_API_KEY` |
| **OpenAI / compatible** | `openai` | `text-embedding-3-small` | Sent to the configured `base_url` (OpenAI or any compatible endpoint). | `CIX_OPENAI_API_KEY` |

Set the API-key env var on the **server**, then select the provider + model in
the dashboard. Switching providers (or a provider's model/dimensions) changes
the embedding space, so cix treats it as a new identity and a **full reindex**
is required — vectors aren't comparable across providers.

> **Choosing:** **Local** for air-gapped / privacy-sensitive repos and zero
> per-query cost. **Voyage AI** (`voyage-code-3`) for top-tier code retrieval
> without hosting a GPU. **OpenAI / compatible** to reuse an existing endpoint
> or internal gateway.

---

## Agent Integration

`cix` is designed to be called by AI agents (Claude, GPT, Cursor, custom agents) as a shell tool — they run `cix search` instead of Grep/Glob and get ranked snippets rather than raw file dumps.

**Claude Code (plugin, recommended).** Bundles the `cix` + `cix-workspace` skills, the `cix-workspace-investigator` sub-agent, CLI auto-install hooks, and a grep-nudge:

```
/plugin marketplace add dvcdsys/code-index
/plugin install cix@code-index
/reload-plugins
```

Then invoke the skill **paired with the actual task** (not a search query) — `/cix <fix / implement / investigate / refactor …>`. cix becomes the agent's IDE (goto-def, find-refs, "what calls this") while it works. Manual install: `cp -r skills/cix ~/.claude/skills/cix`. For multi-repo work: `/cix-workspace <task>`. Full hook list + configuration: [`plugins/cix/README.md`](plugins/cix/README.md).

**Claude Desktop & Cowork (MCP).** These don't load Claude Code plugins, so cix ships a built-in stdio MCP server exposing the same search as `cix_*` tools:

```
cix mcp install claude-desktop      # restart Claude Desktop; cix_* tools appear
/plugin install cix-cowork@code-index   # optional: Cowork skills
```

The model is server-centric and multi-server (no "current project" — the agent names projects/workspaces explicitly). Full guide: [`doc/COWORK_MCP.md`](doc/COWORK_MCP.md).

**Other agents.** Give the agent shell execution and describe the command:

```
Usage: cix search "what you're looking for" [--in ./subdir] [--lang python]
Returns: ranked code snippets with file paths and line numbers
```

---

## Configuration

Most common environment variables (full surface in [`doc/CONFIG_REFERENCE.md`](doc/CONFIG_REFERENCE.md); most are runtime-editable from **Dashboard → Server**):

| Variable | Default | Purpose |
|---|---|---|
| `CIX_API_KEY` | — | Bearer token for CLI/agents. **Required.** |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` | — | Required on a fresh DB; seeds the first admin. |
| `CIX_PORT` | `21847` | Listen port. |
| `CIX_EMBEDDING_MODEL` | `awhiteside/CodeRankEmbed-Q8_0-GGUF` | Local GGUF repo or absolute path. |
| `CIX_N_GPU_LAYERS` | `-1` macOS / `0` else / `99` Docker CUDA | `99` = full offload, `0` = CPU. |
| `CIX_EMBEDDINGS_ENABLED` | `true` | `false` boots without the llama sidecar. |
| `CIX_SECRET_KEY` / `_KEYFILE` | auto-generated keyfile | AES-256-GCM key for `github_tokens` encryption. **Back this up.** |
| `CIX_PUBLIC_URL` | — | Public origin for webhook URLs. Trumped by a live Managed Tunnel. |

The REST surface (auth, users, projects, indexing, search, workspaces, webhooks) is documented at `http://<host>:21847/docs` (Swagger UI) and in [`doc/openapi.yaml`](doc/openapi.yaml) — the single source of truth the Go interface and TypeScript types are generated from.

---

## Deploying & operating

- **Team / production deployment** — topology, volumes, UID/permissions, TLS, backups, upgrades: [`doc/TEAM_DEPLOYMENT.md`](doc/TEAM_DEPLOYMENT.md).
- **GPU (CUDA)** — host requirements, VRAM footprint, image base: [`doc/DOCKER_TAGS.md`](doc/DOCKER_TAGS.md), [`doc/vram-profiling.md`](doc/vram-profiling.md). Inference runs on GPU automatically with the `cu128` image.
- **Security hardening** — trusted-proxy posture, rate limits, body-size caps, what cix deliberately doesn't do: [`doc/SECURITY_DEPLOYMENT.md`](doc/SECURITY_DEPLOYMENT.md).
- **Releases** — tagging, CVE scans, Scout workflow, make targets: [`doc/RELEASES.md`](doc/RELEASES.md).
- **Troubleshooting** — common errors + search tuning: [`doc/TROUBLESHOOTING.md`](doc/TROUBLESHOOTING.md).

Pre-built images on Docker Hub:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | linux/amd64 + linux/arm64 | CPU |
| `dvcdsys/code-index:cu128` | linux/amd64 | NVIDIA GPU (CUDA 12.8) |
| `dvcdsys/code-index:<version>` / `<version>-cu128` | — | Version-pinned variants |

```bash
docker compose logs -f          # tail logs
docker compose down             # stop
docker compose down -v          # stop AND wipe data + models (destructive)
```

---

## Documentation map

| Doc | Purpose |
|---|---|
| [`doc/CLI_REFERENCE.md`](doc/CLI_REFERENCE.md) | Full CLI command surface + per-project config (`.cixignore`, `.cixconfig.yaml`) |
| [`doc/DASHBOARD.md`](doc/DASHBOARD.md) | Dashboard pages, authentication, authorization model, drift indicator |
| [`doc/TEAM_DEPLOYMENT.md`](doc/TEAM_DEPLOYMENT.md) | Self-hosting cix for a team — production / shared-infrastructure deployment for DevOps |
| [`doc/TROUBLESHOOTING.md`](doc/TROUBLESHOOTING.md) | Common issues + search-quality tuning (`--min-score`) |
| [`workspaces.md`](workspaces.md) | User-facing workspace guide (when to use, agent trust rules, query patterns) |
| [`doc/WORKSPACES.md`](doc/WORKSPACES.md) | Operator setup (encryption keys, Cloudflare tunnel, workers, REST API) |
| [`doc/SEARCH_ALGORITHM.md`](doc/SEARCH_ALGORITHM.md) | How per-project + hybrid workspace search rank results |
| [`doc/WEBHOOKS.md`](doc/WEBHOOKS.md) | GitHub webhook lifecycle, modes, HMAC validation |
| [`doc/COWORK_MCP.md`](doc/COWORK_MCP.md) | Using cix from Claude Desktop / Cowork over MCP (`cix mcp install`, multi-server) |
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
| [`plugins/cix-cowork/README.md`](plugins/cix-cowork/README.md) | Cowork skills plugin (MCP-based) reference |

---

## Acknowledgements

cix stands on a lot of excellent open-source work. Thank you to the
projects and teams that make it possible:

**Embeddings & models**
- [llama.cpp](https://github.com/ggml-org/llama.cpp) — the `llama-server`
  sidecar that runs embeddings locally on CPU/GPU.
- [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) by
  Nomic AI — the default code-retrieval embedding model — and
  [awhiteside/CodeRankEmbed-Q8_0-GGUF](https://huggingface.co/awhiteside/CodeRankEmbed-Q8_0-GGUF)
  for the GGUF quantization cix ships with.
- [Voyage AI](https://www.voyageai.com/) — `voyage-code-3` and the
  code-specialized embedding API, supported as a first-class provider.
- [OpenAI](https://platform.openai.com/docs/guides/embeddings) — the
  `text-embedding-3` family and the OpenAI-compatible provider shape.

**Indexing & storage**
- [tree-sitter](https://tree-sitter.github.io/tree-sitter/) — AST-aware
  chunking across 30+ languages, run via
  [wazero](https://github.com/tetratelabs/wazero) (pure-Go WASM runtime).
- [gotreesitter](https://github.com/odvcencio/gotreesitter) — the Go
  tree-sitter binding cix's AST chunking first grew from; thank you for the
  head start.
- [chromem-go](https://github.com/philippgille/chromem-go) — the
  embedded cosine-similarity vector store.
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — cgo-free
  SQLite for project metadata, symbols, and the FTS5/BM25 mirror.
- [go-git](https://github.com/go-git/go-git) — server-side repository
  cloning for workspaces.

**Server & API**
- [chi](https://github.com/go-chi/chi) — HTTP router.
- [kin-openapi](https://github.com/getkin/kin-openapi) +
  [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) —
  OpenAPI-as-source-of-truth codegen for the Go interface and TypeScript
  dashboard types.
- [brotli](https://github.com/andybalholm/brotli) and the
  [Go](https://go.dev/) standard library and `golang.org/x` ecosystem.

**CLI**
- [Cobra](https://github.com/spf13/cobra) — the command framework behind
  every `cix` subcommand.
- [Charm](https://charm.sh/) — [Bubble Tea](https://github.com/charmbracelet/bubbletea),
  [Bubbles](https://github.com/charmbracelet/bubbles), and
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) power the
  interactive `cix config` TUI.
- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) — the Model
  Context Protocol server that exposes cix to Claude Desktop & Cowork.
- [notify](https://github.com/rjeczalik/notify) — cross-platform filesystem
  watching for the index-on-change watcher.
- [koanf](https://github.com/knadh/koanf) — layered configuration
  (flags → env → `~/.cix/config.yaml`).

**Dashboard (web UI)**
- [React](https://react.dev/) + [Vite](https://vitejs.dev/) — the embedded
  dashboard served at `/dashboard`.
- [Radix UI](https://www.radix-ui.com/) + [Tailwind CSS](https://tailwindcss.com/)
  — accessible component primitives and styling (the shadcn/ui pattern).
- [TanStack Query](https://tanstack.com/query) — server-state and data
  fetching.
- [openapi-typescript](https://github.com/openapi-ts/openapi-typescript) —
  generates the dashboard's API types from the OpenAPI spec.
- [lucide](https://lucide.dev/) and [sonner](https://github.com/emilkowalski/sonner)
  — icons and toast notifications.

Full dependency lists with versions live in
[`server/go.mod`](server/go.mod), [`cli/go.mod`](cli/go.mod), and
[`server/dashboard/package.json`](server/dashboard/package.json).

---

## License

MIT
