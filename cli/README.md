# cix - Claude Code Index CLI

Thin client for semantic code search. Watches files, triggers reindexing, provides console commands for agents to search code and navigate projects.

## Architecture

```
cix (thin Go client)                     API Server (Docker or local)
├── watch  ─── fsnotify ─── debounce ──> POST /index (incremental)
├── init   ─── register + index + watch
├── search ─── semantic code search ───> POST /search
├── symbols ── symbol lookup ──────────> POST /search/symbols
├── files  ─── file path search ───────> POST /search/files
├── summary ── project overview ───────> GET  /summary
├── status ─── indexing progress ──────> GET  /index/status
├── list   ─── list projects ──────────> GET  /projects
├── reindex ── manual reindex ─────────> POST /index
└── config ─── manage ~/.cix/config.yaml

cli/
├── cmd/                 - Cobra commands (init, search, symbols, files, summary, watch, ...)
├── internal/
│   ├── client/          - HTTP client to FastAPI server
│   ├── config/          - YAML config (~/.cix/config.yaml)
│   ├── watcher/         - fsnotify file watcher with debounce
│   └── daemon/          - Background process management (PID file, start/stop)
└── main.go
```

## Installation

```bash
cd cli
make build        # builds to ./build/cix
make install      # copies to /usr/local/bin/cix
```

Or without make:

```bash
cd cli
go build -o cix .
sudo mv cix /usr/local/bin/
```

## Quick Start

```bash
# 1. Start the API server (pick one)
make server-docker   # Docker mode
make server-local    # Local mode (requires Python 3.11+)

# 2. Configure cix (API key is in .env)
cix config set api.key $(grep API_KEY ../.env | cut -d= -f2)

# 3. Initialize a project (registers + indexes + starts file watcher)
cd /path/to/your/code
cix init

# 4. Wait for indexing
cix status
# Status: ✓ Indexed
# Files: 1250 | Chunks: 5432 | Symbols: 892

# 5. Search
cix search "authentication middleware"
cix symbols handleRequest --kind function
cix files "config"
cix summary
```

## Commands

### Project Lifecycle

```bash
cix init [path]                  # Register project, index, start file watcher
cix init --watch=false [path]    # Register + index without watcher
cix list                         # List all indexed projects
cix status [-p path]             # Show project indexing status
cix summary [-p path]            # Project overview (languages, dirs, symbols)
cix reindex [--full] [-p path]   # Trigger manual reindex
```

### Search (for agents)

```bash
# Semantic code search (natural language)
cix search <query> [flags]
  --in <path>                    # Search within file or directory (repeatable)
  --limit, -l <n>                # Max results (default: 10)
  --lang <language>              # Filter by language (repeatable)
  --min-score <0.0-1.0>          # Minimum relevance score (default: 0.1)
  --project, -p <path>           # Project path (default: cwd)

# Examples
cix search "authentication middleware"
cix search "error handling" --in ./api
cix search "config" --in README.md
cix search "routes" --in ./api --in ./mcp_server
cix search "database" --lang python --limit 20

# Symbol search (by name, fast)
cix symbols <query> [flags]
  --kind <type>                  # function, class, method, type (repeatable)
  --limit, -l <n>                # Max results (default: 20)
  --project, -p <path>

# File path search
cix files <pattern> [flags]
  --limit, -l <n>                # Max results (default: 20)
  --project, -p <path>
```

### File Watching

```bash
cix watch [path]                 # Start as background daemon (default)
cix watch --foreground [path]    # Run in terminal (Ctrl+C to stop)
cix watch stop                   # Stop daemon
cix watch status                 # Check if daemon is running
```

The watcher uses `fsnotify` to monitor the project directory for changes. When files are modified, it debounces events (default 5s) and triggers incremental reindexing via the API.

Excluded from watching: `node_modules`, `.git`, `.venv`, `__pycache__`, `dist`, `build`, `.next`, `.cache`, binary files, images, archives.

### Configuration

```bash
cix config show                  # Show current config
cix config set <key> <value>     # Set value
cix config path                  # Show config file path

# Keys:
#   api.url              - API server URL (default: http://localhost:21847)
#   api.key              - API authentication key
#   watcher.debounce_ms  - Debounce delay in ms (default: 5000)
```

## Config File

`~/.cix/config.yaml`:

```yaml
api:
  url: http://localhost:21847
  key: cix_your_key_here

watcher:
  enabled: true
  debounce_ms: 5000
  exclude:
    - node_modules
    - .git
    - .venv

projects:
  - path: /Users/me/project1
    auto_watch: true
```

## Testing Indexing Manually

```bash
# 1. Start the server
make server-docker   # or make server-local

# 2. Check health
curl http://localhost:21847/health
# {"status":"ok"}

# 3. Init and index a project
cix init /path/to/your/project

# 4. Watch indexing progress
cix status -p /path/to/your/project
# repeat until Status: ✓ Indexed

# 5. Test semantic search
cix search "error handling" -p /path/to/your/project
cix search "database connection" --lang go -p /path/to/your/project

# 6. Test symbol search
cix symbols main -p /path/to/your/project
cix symbols "Handler" --kind function -p /path/to/your/project

# 7. Test file search
cix files "config" -p /path/to/your/project

# 8. Test watcher (in a separate terminal)
cix watch /path/to/your/project
# now edit a file in the project — watcher should trigger reindex

# 9. Test via raw API (without cix)
source .env
API=http://localhost:21847
AUTH="Authorization: Bearer $API_KEY"
PROJECT="/path/to/your/project"
ENCODED=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$PROJECT', safe=''))")

# Create project
curl -X POST "$API/api/v1/projects" -H "$AUTH" -H "Content-Type: application/json" \
  -d "{\"host_path\": \"$PROJECT\"}"

# Trigger indexing
curl -X POST "$API/api/v1/projects/$ENCODED/index" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"full": false}'

# Check progress
curl "$API/api/v1/projects/$ENCODED/index/status" -H "$AUTH"

# Semantic search
curl -X POST "$API/api/v1/projects/$ENCODED/search" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"query": "authentication", "limit": 5}'

# Symbol search
curl -X POST "$API/api/v1/projects/$ENCODED/search/symbols" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"query": "main", "limit": 10}'
```

## Troubleshooting

### "API key not set"

```bash
cix config set api.key $(grep API_KEY /path/to/code-index/.env | cut -d= -f2)
```

### "connection refused"

Server is not running. Start it:

```bash
cd /path/to/code-index
docker compose up -d          # Docker
# or
./setup-local.sh              # Local
```

### "project not found"

```bash
cix init /path/to/project
```

### Watcher not triggering

```bash
# Check if daemon is running
cix watch status

# Check logs
cat ~/.cix/logs/watcher.log

# Restart
cix watch stop
cix watch start /path/to/project
```

## License

MIT
