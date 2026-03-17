#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$PROJECT_DIR/.env"
DATA_DIR="$HOME/.cix/data"

echo "=== Claude Code Index — Local Setup ==="

# 1. Check Python
if ! command -v python3 &>/dev/null; then
    echo "ERROR: python3 is required. Install Python 3.11+ first."
    exit 1
fi

PYTHON_VERSION=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
echo "Python version: $PYTHON_VERSION"

# 2. Create virtual environment
if [ ! -d "$PROJECT_DIR/.venv" ]; then
    echo "Creating virtual environment..."
    python3 -m venv "$PROJECT_DIR/.venv"
fi
source "$PROJECT_DIR/.venv/bin/activate"

# 3. Install API dependencies
echo "Installing dependencies (first time downloads ~274MB embedding model)..."
pip install -q -r "$PROJECT_DIR/api/requirements.txt"

# 4. Create data directories
mkdir -p "$DATA_DIR/chroma" "$DATA_DIR/sqlite"

# 5. Generate .env if not exists
if [ ! -f "$ENV_FILE" ]; then
    echo "Generating configuration..."
    API_KEY="cix_$(openssl rand -hex 32)"
    cat > "$ENV_FILE" <<EOF
API_KEY=$API_KEY
PORT=21847
EMBEDDING_MODEL=nomic-ai/CodeRankEmbed
MAX_FILE_SIZE=524288
EXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store
CHROMA_PERSIST_DIR=$DATA_DIR/chroma
SQLITE_PATH=$DATA_DIR/sqlite/projects.db
EOF
    echo "Created $ENV_FILE"
else
    echo "Config exists at $ENV_FILE"
fi

source "$ENV_FILE"

# 6. Pre-download embedding model
echo "Ensuring embedding model is downloaded..."
python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('${EMBEDDING_MODEL:-nomic-ai/CodeRankEmbed}', trust_remote_code=True)" 2>/dev/null

# 7. Start API server in background
echo "Starting API server on port ${PORT:-21847}..."
cd "$PROJECT_DIR/api"
PYTHONPATH="$PROJECT_DIR/api" \
API_KEY="$API_KEY" \
CHROMA_PERSIST_DIR="${CHROMA_PERSIST_DIR:-$DATA_DIR/chroma}" \
SQLITE_PATH="${SQLITE_PATH:-$DATA_DIR/sqlite/projects.db}" \
EMBEDDING_MODEL="${EMBEDDING_MODEL:-nomic-ai/CodeRankEmbed}" \
MAX_FILE_SIZE="${MAX_FILE_SIZE:-524288}" \
EXCLUDED_DIRS="${EXCLUDED_DIRS:-node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store}" \
nohup "$PROJECT_DIR/.venv/bin/uvicorn" app.main:app \
    --host 0.0.0.0 --port "${PORT:-21847}" \
    > "$DATA_DIR/server.log" 2>&1 &

SERVER_PID=$!
echo "$SERVER_PID" > "$DATA_DIR/server.pid"
echo "Server PID: $SERVER_PID (saved to $DATA_DIR/server.pid)"

cd "$PROJECT_DIR"

# 8. Wait for health
echo "Waiting for service to be healthy..."
for i in $(seq 1 30); do
    if curl -sf "http://localhost:${PORT:-21847}/health" > /dev/null 2>&1; then
        echo "Service is healthy!"
        break
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "ERROR: Server process died. Check logs: cat $DATA_DIR/server.log"
        exit 1
    fi
    [ "$i" -eq 30 ] && echo "ERROR: Service failed to start. Check logs: cat $DATA_DIR/server.log" && exit 1
    sleep 2
done

# 9. Register MCP server
echo "Registering MCP server in Claude Code..."
claude mcp remove code-index 2>/dev/null || true
claude mcp add \
    --scope user \
    -e CODE_INDEX_API_URL="http://localhost:${PORT:-21847}" \
    -e CODE_INDEX_API_KEY="$API_KEY" \
    code-index \
    -- uv run --directory "$PROJECT_DIR" python -m mcp_server

# 10. Add instructions to global CLAUDE.md
CLAUDE_DIR="$HOME/.claude"
CLAUDE_MD="$CLAUDE_DIR/CLAUDE.md"
MARKER="<!-- code-index-instructions -->"

if [ ! -f "$CLAUDE_MD" ] || ! grep -q "$MARKER" "$CLAUDE_MD" 2>/dev/null; then
    echo "Adding code-index instructions to $CLAUDE_MD..."
    mkdir -p "$CLAUDE_DIR"
    cat >> "$CLAUDE_MD" <<'INSTRUCTIONS'

<!-- code-index-instructions -->
## Code Index (`cix`)

This environment has a semantic code index. Use the `cix` CLI to search code and navigate the project.

**IMPORTANT — search priority:**
1. ALWAYS use `cix search` or `cix symbols` FIRST when looking for code
2. Only fall back to Grep/Glob if the index returns no results or `cix` is not available
3. The index understands natural language — ask it like you would ask a developer

**Commands (run via Bash tool):**
- `cix search "authentication middleware"` — semantic code search
- `cix search "error handling" --in ./api` — search within a directory
- `cix search "config" --in README.md` — search within a specific file
- `cix symbols "handleRequest" --kind function` — find symbols by name
- `cix files "config"` — search files by path pattern
- `cix summary` — project overview (languages, directories, symbols)
- `cix status` — check indexing status
- `cix reindex` — trigger incremental reindex after changes

**First time setup:**
If the project is not yet indexed, run: `cix init`
This registers the project, starts indexing, and launches a file watcher daemon.
The watcher auto-reindexes when files change — no manual reindex needed.

**Tips:**
- Use `--in` flag to narrow search to a specific file or directory
- Use `--lang go` to filter by language
- Use `--limit 20` to get more results
- If `cix` is not installed, fall back to MCP tools: search_code, find_symbols
<!-- /code-index-instructions -->
INSTRUCTIONS
    echo "Added code-index instructions to $CLAUDE_MD"
else
    echo "Code-index instructions already in $CLAUDE_MD"
fi

echo ""
echo "=== Local Setup Complete ==="
echo "API server running on http://localhost:${PORT:-21847} (PID: $SERVER_PID)"
echo "MCP server 'code-index' registered globally."
echo "Instructions added to $CLAUDE_MD."
echo ""
echo "Useful commands:"
echo "  Stop server:    kill \$(cat $DATA_DIR/server.pid)"
echo "  View logs:      tail -f $DATA_DIR/server.log"
echo "  Restart server: kill \$(cat $DATA_DIR/server.pid) && ./setup-local.sh"
echo ""
echo "Restart Claude Code to use the new tools."
