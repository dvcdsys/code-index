#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="$PROJECT_DIR/.env"
DATA_DIR="$HOME/.cix/data"

echo "=== cix — Code IndeX Setup (Docker) ==="

# 1. Generate .env if not exists
if [ ! -f "$ENV_FILE" ]; then
    echo "Generating configuration..."
    API_KEY="cix_$(openssl rand -hex 32)"
    cat > "$ENV_FILE" <<EOF
API_KEY=$API_KEY
PORT=21847
EMBEDDING_MODEL=nomic-ai/CodeRankEmbed
MAX_FILE_SIZE=524288
EXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store
EOF
    echo "Created $ENV_FILE"
else
    echo "Config exists at $ENV_FILE"
fi

source "$ENV_FILE"

# 2. Create data directories (bind mount target must exist before docker starts)
echo "Creating data directories at $DATA_DIR..."
mkdir -p "$DATA_DIR/chroma" "$DATA_DIR/sqlite"

# 3. Build Docker image
echo "Building Docker image (first build downloads ~274MB model)..."
cd "$PROJECT_DIR"
docker compose build

# 4. Start container
echo "Starting service..."
docker compose up -d

# 5. Wait for health
echo "Waiting for service to be healthy..."
for i in $(seq 1 30); do
    if curl -sf "http://localhost:${PORT:-21847}/health" > /dev/null 2>&1; then
        echo "Service is healthy!"
        break
    fi
    [ "$i" -eq 30 ] && echo "ERROR: Service failed to start. Check logs: docker compose logs" && exit 1
    sleep 2
done

# 6. Configure cix CLI (if installed)
if command -v cix &>/dev/null; then
    echo "Configuring cix CLI..."
    cix config set api.url "http://localhost:${PORT:-21847}"
    cix config set api.key "$API_KEY"
    echo "✓ cix configured"
else
    echo "cix CLI not installed. Install it with: cd cli && make build && make install"
    echo "Then configure it:"
    echo "  cix config set api.url http://localhost:${PORT:-21847}"
    echo "  cix config set api.key $API_KEY"
fi

# 7. Register MCP server (Claude Code integration — optional)
if command -v claude &>/dev/null; then
    echo "Registering MCP server in Claude Code..."
    claude mcp remove code-index 2>/dev/null || true
    claude mcp add code-index \
        --scope user \
        -e CODE_INDEX_API_URL="http://localhost:${PORT:-21847}" \
        -e CODE_INDEX_API_KEY="$API_KEY" \
        -- uv run --directory "$PROJECT_DIR" python -m mcp_server
    echo "✓ MCP server registered"
fi

echo ""
echo "=== Setup Complete ==="
echo ""
echo "API:     http://localhost:${PORT:-21847}"
echo "API key: $API_KEY"
echo "Data:    $DATA_DIR"
echo ""
echo "Next steps:"
echo "  Install CLI:   cd cli && make build && make install"
echo "  Index project: cix init /path/to/your/project"
echo "  Search:        cix search \"authentication middleware\""
