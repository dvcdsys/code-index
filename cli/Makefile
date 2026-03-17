.PHONY: build install clean test run server-docker server-local server-stop release

# Build variables
BINARY_NAME=cix
BUILD_DIR=build
DIST_DIR=dist
VERSION?=v0.1.0
ROOT_DIR=$(shell cd .. && pwd)

# ── Build ──────────────────────────────────────────────

build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="-X 'github.com/anthropics/code-index/cli/cmd.Version=$(VERSION)'" -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Built to $(BUILD_DIR)/$(BINARY_NAME)"

install: build
	@echo "Installing $(BINARY_NAME) to /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/
	@echo "Installed! Run 'cix --help' to get started"

clean:
	rm -rf $(BUILD_DIR) $(DIST_DIR)

# ── Release (cross-platform) ───────────────────────────
#
# Builds binaries for macOS and Linux (amd64 + arm64),
# packages each into a .tar.gz archive ready for GitHub Releases.
#
# Usage:
#   make release VERSION=v0.1.0
#
# Output:
#   dist/cix-darwin-arm64.tar.gz
#   dist/cix-darwin-amd64.tar.gz
#   dist/cix-linux-arm64.tar.gz
#   dist/cix-linux-amd64.tar.gz
#   dist/checksums.txt

release:
	@echo "Building release $(VERSION)..."
	@mkdir -p $(DIST_DIR)
	@for target in darwin-arm64 darwin-amd64 linux-arm64 linux-amd64; do \
		GOOS=$$(echo $$target | cut -d- -f1); \
		GOARCH=$$(echo $$target | cut -d- -f2); \
		BIN="$(DIST_DIR)/$(BINARY_NAME)-$$target"; \
		echo "  $$target..."; \
		GOOS=$$GOOS GOARCH=$$GOARCH go build \
			-ldflags="-X 'github.com/anthropics/code-index/cli/cmd.Version=$(VERSION)'" \
			-o "$$BIN" . ; \
		tar -czf "$(DIST_DIR)/$(BINARY_NAME)-$$target.tar.gz" \
			-C "$(DIST_DIR)" "$(BINARY_NAME)-$$target" ; \
		rm "$$BIN"; \
	done
	@cd $(DIST_DIR) && shasum -a 256 *.tar.gz > checksums.txt
	@echo ""
	@echo "Release artifacts in $(DIST_DIR)/:"
	@ls -lh $(DIST_DIR)/
	@echo ""
	@echo "Upload to GitHub Releases:"
	@echo "  gh release create $(VERSION) $(DIST_DIR)/*.tar.gz $(DIST_DIR)/checksums.txt"

# ── Server Management ──────────────────────────────────

# Start API server in Docker (builds image if needed)
server-docker:
	@echo "Starting API server in Docker..."
	@if [ ! -f "$(ROOT_DIR)/.env" ]; then \
		echo "Generating .env..."; \
		API_KEY="cix_$$(openssl rand -hex 32)"; \
		echo "API_KEY=$$API_KEY" > "$(ROOT_DIR)/.env"; \
		echo "PORT=21847" >> "$(ROOT_DIR)/.env"; \
		echo "EMBEDDING_MODEL=nomic-ai/CodeRankEmbed" >> "$(ROOT_DIR)/.env"; \
		echo "MAX_FILE_SIZE=524288" >> "$(ROOT_DIR)/.env"; \
		echo "EXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store" >> "$(ROOT_DIR)/.env"; \
		echo "Created $(ROOT_DIR)/.env (API_KEY=$$API_KEY)"; \
	fi
	cd "$(ROOT_DIR)" && docker compose up -d --build
	@echo "Waiting for server health..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:21847/health > /dev/null 2>&1; then \
			echo "Server is healthy: http://localhost:21847"; \
			echo "API Key: $$(grep API_KEY $(ROOT_DIR)/.env | cut -d= -f2)"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "ERROR: Server failed to start. Run: cd $(ROOT_DIR) && docker compose logs"; \
	exit 1

# Start API server locally (no Docker, requires Python 3.11+)
server-local:
	@echo "Starting API server locally..."
	cd "$(ROOT_DIR)" && ./setup-local.sh
	@echo ""
	@echo "Server running at http://localhost:21847"
	@echo "API Key: $$(grep API_KEY $(ROOT_DIR)/.env | cut -d= -f2)"

# Stop API server (both modes)
server-stop:
	@echo "Stopping API server..."
	@if cd "$(ROOT_DIR)" && docker compose ps -q 2>/dev/null | grep -q .; then \
		echo "Stopping Docker container..."; \
		cd "$(ROOT_DIR)" && docker compose down; \
	fi
	@if [ -f "$$HOME/.cix/data/server.pid" ]; then \
		PID=$$(cat "$$HOME/.cix/data/server.pid"); \
		if kill -0 "$$PID" 2>/dev/null; then \
			echo "Stopping local server (PID $$PID)..."; \
			kill "$$PID"; \
		fi; \
		rm -f "$$HOME/.cix/data/server.pid"; \
	fi
	@echo "Server stopped"

# Show server logs
server-logs:
	@if cd "$(ROOT_DIR)" && docker compose ps -q 2>/dev/null | grep -q .; then \
		cd "$(ROOT_DIR)" && docker compose logs -f; \
	elif [ -f "$$HOME/.cix/data/server.log" ]; then \
		tail -f "$$HOME/.cix/data/server.log"; \
	else \
		echo "No server running"; \
	fi

# ── Testing ────────────────────────────────────────────

test:
	go test -v ./...

# Quick smoke test: health check + index + search
test-manual:
	@echo "=== Manual Integration Test ==="
	@echo ""
	@if ! curl -sf http://localhost:21847/health > /dev/null 2>&1; then \
		echo "ERROR: Server not running. Start with: make server-docker"; \
		exit 1; \
	fi
	@echo "1. Health check"
	@curl -s http://localhost:21847/health | python3 -m json.tool
	@echo ""
	@API_KEY=$$(grep API_KEY $(ROOT_DIR)/.env | cut -d= -f2); \
	echo "2. Server status"; \
	curl -s -H "Authorization: Bearer $$API_KEY" http://localhost:21847/api/v1/status | python3 -m json.tool; \
	echo ""; \
	echo "3. Init current project"; \
	$(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" init "$(ROOT_DIR)" 2>&1 || true; \
	echo ""; \
	echo "4. Check indexing status (wait for it to finish)"; \
	for i in $$(seq 1 60); do \
		STATUS=$$($(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" status -p "$(ROOT_DIR)" 2>&1 | grep "Status:" | head -1); \
		echo "   $$STATUS"; \
		if echo "$$STATUS" | grep -q "Indexed"; then break; fi; \
		if echo "$$STATUS" | grep -q "Error"; then echo "   Indexing failed!"; break; fi; \
		sleep 3; \
	done; \
	echo ""; \
	echo "5. Semantic search: 'authentication'"; \
	$(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" search "authentication" -p "$(ROOT_DIR)" --limit 3 2>&1 || true; \
	echo ""; \
	echo "6. Symbol search: 'main'"; \
	$(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" symbols "main" -p "$(ROOT_DIR)" --limit 5 2>&1 || true; \
	echo ""; \
	echo "7. File search: 'config'"; \
	$(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" files "config" -p "$(ROOT_DIR)" 2>&1 || true; \
	echo ""; \
	echo "8. Project summary"; \
	$(BUILD_DIR)/$(BINARY_NAME) --api-key "$$API_KEY" summary -p "$(ROOT_DIR)" 2>&1 || true; \
	echo ""; \
	echo "=== Test Complete ==="

# ── Development ────────────────────────────────────────

run:
	go run . $(ARGS)

dev:
	go run . $(ARGS)

fmt:
	go fmt ./...

deps:
	go mod download
	go mod tidy

# ── Help ───────────────────────────────────────────────

help:
	@echo "=== cix Makefile ==="
	@echo ""
	@echo "Build:"
	@echo "  build                Build cix for current platform"
	@echo "  install              Build and install to /usr/local/bin"
	@echo "  release VERSION=v.. Build release archives for all platforms"
	@echo "  clean                Remove build and dist artifacts"
	@echo ""
	@echo "Server:"
	@echo "  server-docker        Start API server in Docker (builds image if needed)"
	@echo "  server-local         Start API server locally (Python 3.11+, no Docker)"
	@echo "  server-stop          Stop API server (both modes)"
	@echo "  server-logs          Tail server logs"
	@echo ""
	@echo "Testing:"
	@echo "  test                 Run Go unit tests"
	@echo "  test-manual          Run full integration test (requires running server)"
	@echo ""
	@echo "Development:"
	@echo "  run ARGS='...'       Quick run (e.g., make run ARGS='search auth')"
	@echo "  fmt                  Format code"
	@echo "  deps                 Download and tidy dependencies"
	@echo ""
	@echo "Quick start:"
	@echo "  make build && make server-docker && make test-manual"