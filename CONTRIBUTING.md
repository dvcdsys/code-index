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
└── skills/           # Claude Code skill definitions
```

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

# CLI build check
cd cli && go build ./...
```

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

- All changes to `main` must go through a pull request
- At least **1 approval** required before merging
- Keep PRs focused — one feature or fix per PR
- For server changes: `go test ./...` must pass in `server/`
- For CLI changes: `go vet ./...` must pass in `cli/`

## Reporting issues

Open an issue at https://github.com/dvcdsys/code-index/issues with:
- OS and architecture
- Docker image tag or binary version (`cix-server -v`)
- Relevant logs (`docker compose logs` or `~/.cix/logs/watcher.log`)
