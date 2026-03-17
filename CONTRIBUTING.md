# Contributing

## Project structure

```
code-index/
├── api/              # Python API server (FastAPI + embeddings)
│   ├── app/
│   │   ├── routers/  # HTTP endpoints
│   │   ├── services/ # business logic (indexing, search, embeddings)
│   │   ├── schemas/  # Pydantic models
│   │   └── core/     # config, exceptions, language detection
│   └── Dockerfile
├── cli/              # Go CLI (cix binary)
│   ├── cmd/          # cobra commands
│   └── internal/     # client, config, daemon, indexer, watcher
├── mcp_server/       # MCP server wrapper
├── tests/            # Python integration tests
└── skills/           # Claude Code skill definitions
```

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.21+ | CLI |
| Python | 3.11+ | API server |
| uv | latest | Python package manager |
| Docker | 24+ | containerized server |
| make | any | build shortcuts |

## Local development setup

### API server

```bash
python3 -m venv .venv && source .venv/bin/activate
pip install -r api/requirements.txt

cp .env.example .env
# Edit .env — set API_KEY to anything for local dev

source .env
cd api && uvicorn app.main:app --host 0.0.0.0 --port 21847 --reload
```

### CLI

```bash
cd cli
go mod download
go build -o cix .

# Run directly without installing
./cix config set api.url http://localhost:21847
./cix config set api.key <your-api-key>
```

Or install globally:

```bash
make build && make install   # → /usr/local/bin/cix
```

## Running tests

```bash
# Python tests (requires running API server)
source .venv/bin/activate
pytest tests/ -v

# Go — no tests yet, just build check
cd cli && go build ./...
```

## Making changes

### API (Python)

- Endpoints go in `api/app/routers/`
- Business logic goes in `api/app/services/`
- Request/response models go in `api/app/schemas/`
- After changes: restart uvicorn (auto-reloads with `--reload`)

### CLI (Go)

- New commands go in `cli/cmd/` as a new `.go` file, registered in `root.go`
- HTTP client lives in `cli/internal/client/`
- After changes: `cd cli && go build -o cix .`

## Building the Docker image

```bash
# Local build (for testing)
docker compose up -d --build

# Push to Docker Hub (multi-arch)
make docker-setup                              # once per machine
make docker-push-all DOCKER_USER=yourname
```

See [README — Building and Publishing to Docker Hub](README.md#building-and-publishing-to-docker-hub) for details.

## Pull requests

- All changes to `main` must go through a pull request — direct pushes are not allowed
- At least **1 approval** from a contributor is required before merging
- Keep PRs focused — one feature or fix per PR
- Test against a running API server before submitting
- For CLI changes: make sure `go vet ./...` passes
- For API changes: make sure `pytest tests/` passes

## Reporting issues

Open an issue at https://github.com/dvcdsys/code-index/issues with:
- OS and architecture
- Docker or local mode
- `cix --version` output
- Relevant logs (`docker compose logs` or `~/.cix/logs/watcher.log`)
