# Contributing

## Project structure

```
code-index/
├── server/           # Go API server (cix-server)
│   ├── cmd/          # main entrypoint
│   ├── internal/     # config, db, httpapi, embeddings, indexer, vectorstore, ...
│   ├── Dockerfile    # CPU multi-arch build
│   └── Dockerfile.cuda  # CUDA 3-stage build
├── cli/              # Go CLI (cix binary)
│   ├── cmd/          # cobra commands
│   └── internal/     # client, config, daemon, indexer, watcher
├── plugins/cix/      # Claude Code plugin (hooks, skills, slash commands, bats tests)
├── skills/           # Canonical sources for cross-cutting skills
│                     # (mirrored into plugins/cix/skills/ via sync-skills.sh)
└── doc/              # Tracked documentation
```

The `docs/` directory (with an `s`) is gitignored — it is for local notes
only. New tracked documentation goes under `doc/`.

## Branches and pull requests

The repo runs a two-branch model:

| Branch    | Purpose                                                       |
|-----------|---------------------------------------------------------------|
| `main`    | Release branch. Tags (`server/vX.Y.Z`, `cli/vX.Y.Z`) cut from here. |
| `develop` | Integration branch. All feature and fix work merges here first. |

**Open every PR against `develop`.** Promotion from `develop` to `main`
happens as part of the release workflow, not per-feature.

CI is wired in three workflows — `ci-cli.yml`, `ci-server.yml`,
`ci-plugin.yml` — all gated on the same branch set:

- Push to `main` or `develop` runs CI.
- Pull request targeting `main` or `develop` runs CI.
- Push to any other branch does **not** trigger CI directly — CI fires
  when you open the PR.

There is no required branch-name convention. You can name your feature
branch anything (`feat/foo`, `fix/bug-123`, `your-handle/sandbox`, …);
CI runs from the PR, not from the branch name.

Each workflow has a path filter, so CLI-only changes don't run server
tests and vice versa. Filters live at the top of each workflow file.

## Commit messages

The repo uses Conventional Commits:

```
<type>(<scope>): <imperative summary under ~70 chars>

<optional body explaining the why, wrapped at ~72 cols>
```

Types in active use: `feat`, `fix`, `chore`, `docs`, `ci`, `build`,
`refactor`. Scopes commonly seen: `cli`, `server`, `plugin`, `dashboard`,
`db`, `workspaces`, `skill`. Pick the smallest accurate scope; if a
change spans surfaces, the broader type without a scope is fine.

The body should explain **why**, not **what** — the diff already shows
what.

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.24+ | server + CLI |
| Docker | 24+ | containerized server |
| make | any | build shortcuts |

## Local development setup

### Server

```bash
cd server
go mod download

# Run unit tests
go test ./...

# Build binary
make build   # → server/dist/cix-darwin-arm64/cix-server (or linux-amd64)

# Build + fetch llama-server (for local E2E)
make bundle

# Run server locally (no embeddings)
CIX_PORT=21847 CIX_EMBEDDINGS_ENABLED=false \
  CIX_SQLITE_PATH=/tmp/cix-dev.db \
  CIX_CHROMA_PERSIST_DIR=/tmp/cix-chroma \
  ./dist/cix-darwin-arm64/cix-server
```

### CLI

```bash
cd cli
go mod download
go build -o cix .

./cix config set api.url http://localhost:21847
./cix config set api.key <your-api-key>
```

Or install globally:

```bash
cd cli && make build && make install   # → /usr/local/bin/cix
```

## Running tests

```bash
# Server unit tests
cd server && go test ./...

# Server parity gate (requires make bundle + a local GGUF)
cd server && make test-gate

# CLI tests + build check
cd cli && make test           # go test -v ./...
cd cli && go build ./...

# Plugin tests (bats + shellcheck + jq manifest validation)
bats plugins/cix/tests/*.bats
shellcheck --severity=warning plugins/cix/scripts/*.sh
jq . plugins/cix/hooks/hooks.json
```

`bats` is the test runner the plugin uses (`brew install bats-core` on
macOS, `apt-get install bats` on Linux). CI runs the same three checks
plus a symlink-integrity assertion for `plugins/cix/bin/cix`.

### End-to-end plugin reload

To smoke-test plugin changes against a real Claude Code installation:

```bash
make plugin-reload-local      # from repo root
```

This removes the cix plugin, purges the plugin cache, re-installs the
marketplace from the working tree, and re-installs the plugin. Restart
your Claude Code session to pick up the new bundle.

### Skill source sync

The `cix-workspace` skill has a canonical source under `skills/` and a
byte-identical mirror under `plugins/cix/skills/`. Both must stay in
sync. After editing the source:

```bash
plugins/cix/scripts/sync-skills.sh             # copy source → plugin
plugins/cix/scripts/sync-skills.sh --check     # drift check (also run in CI)
```

`--check` runs in `ci-plugin.yml` on every push/PR that touches `skills/`
or `plugins/`, so a divergence fails CI before merge. Do not edit the
plugin copy directly; the next sync overwrites it. The standalone
`skills/cix/SKILL.md` is **not** synced — the plugin version carries
extra frontmatter the standalone loader does not need.

## Making changes

### Server (Go)

- Endpoints: `server/internal/httpapi/`
- Business logic: `server/internal/indexer/`, `server/internal/embeddings/`
- Config: `server/internal/config/config.go`
- After changes: `go build ./...` + `go test ./...`
- **Do not touch `cli/`** — CLI is a separate module with its own scope.

### CLI (Go)

- New commands: `cli/cmd/` as a new `.go` file, registered in `root.go`
- HTTP client: `cli/internal/client/`
- After changes: `cd cli && go build -o cix .`

## Building the Docker image

```bash
# CPU multi-arch (linux/amd64 + linux/arm64)
# (run via GitHub Actions on server/v* tag — manual push rarely needed)

# CUDA amd64
make docker-build-cuda   # from repo root
```

See [README — Building and Publishing](README.md#building-and-publishing-to-docker-hub) for details.

## Pull requests

- Open every PR against `develop` (not `main` — see "Branches and pull
  requests" above).
- At least **1 approval** required before merging.
- Keep PRs focused — one feature or fix per PR.
- For server changes: `go test ./...` must pass in `server/`.
- For CLI changes: `go test ./...` must pass in `cli/`.
- For plugin changes: `bats plugins/cix/tests/*.bats` must pass (CI runs
  this on Linux and macOS).

## Reporting issues

Open an issue at https://github.com/dvcdsys/code-index/issues with:
- OS and architecture
- Docker image tag or binary version (`cix-server -v`)
- Relevant logs (`docker compose logs` or `~/.cix/logs/watcher.log`)
