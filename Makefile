.PHONY: server-stop server-docker server-local server-logs server-status \
        docker-setup docker-push-arm64 docker-push-amd64 docker-push-all

PORT       ?= 21847
DOCKER_USER ?= $(error DOCKER_USER is not set. Run: make docker-push-all DOCKER_USER=yourname)
IMAGE_NAME  ?= code-index
VERSION     ?= latest

# Stop the API server regardless of how it was started (Docker or local)
server-stop:
	@echo "Stopping API server..."
	@# 1. Docker
	@if docker compose ps -q 2>/dev/null | grep -q .; then \
		echo "  Stopping Docker container..."; \
		docker compose down; \
	fi
	@# 2. Local via setup-local.sh PID file
	@if [ -f "$$HOME/.cix/data/server.pid" ]; then \
		PID=$$(cat "$$HOME/.cix/data/server.pid"); \
		if kill -0 "$$PID" 2>/dev/null; then \
			echo "  Stopping local server (PID $$PID)..."; \
			kill "$$PID"; \
		fi; \
		rm -f "$$HOME/.cix/data/server.pid"; \
	fi
	@# 3. Any remaining uvicorn on the port
	@PIDS=$$(lsof -ti :$(PORT) 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "  Killing process(es) on port $(PORT): $$PIDS"; \
		echo "$$PIDS" | xargs kill 2>/dev/null || true; \
	fi
	@echo "Server stopped"

# Start API server in Docker
server-docker:
	@if [ ! -f .env ]; then \
		echo "Generating .env..."; \
		API_KEY="cix_$$(openssl rand -hex 32)"; \
		printf "API_KEY=$$API_KEY\nPORT=$(PORT)\nEMBEDDING_MODEL=nomic-ai/CodeRankEmbed\nMAX_FILE_SIZE=524288\nEXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store\n" > .env; \
		echo "Created .env"; \
	fi
	@mkdir -p "$$HOME/.cix/data/chroma" "$$HOME/.cix/data/sqlite"
	docker compose up -d --build
	@echo "Waiting for health..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
			echo "Server healthy: http://localhost:$(PORT)"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "ERROR: Server failed to start. Run: docker compose logs"; exit 1

# Start API server locally
server-local:
	./setup-local.sh

# Tail server logs (Docker or local)
server-logs:
	@if docker compose ps -q 2>/dev/null | grep -q .; then \
		docker compose logs -f; \
	elif [ -f "$$HOME/.cix/data/server.log" ]; then \
		tail -f "$$HOME/.cix/data/server.log"; \
	else \
		echo "No server running"; \
	fi

# Check server status
server-status:
	@if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
		echo "Server: running on port $(PORT)"; \
		curl -sf http://localhost:$(PORT)/health; echo; \
	else \
		echo "Server: not running"; \
	fi
	@# Docker
	@if docker compose ps -q 2>/dev/null | grep -q .; then \
		echo "Mode: Docker"; \
		docker compose ps; \
	elif [ -f "$$HOME/.cix/data/server.pid" ] && kill -0 $$(cat "$$HOME/.cix/data/server.pid") 2>/dev/null; then \
		echo "Mode: local (PID $$(cat $$HOME/.cix/data/server.pid))"; \
	elif lsof -ti :$(PORT) > /dev/null 2>&1; then \
		echo "Mode: unknown (PID $$(lsof -ti :$(PORT)))"; \
	fi

# Create (or reuse) a buildx builder that supports linux/arm64
docker-setup:
	@if ! docker buildx inspect cix-builder > /dev/null 2>&1; then \
		echo "Creating buildx builder 'cix-builder'..."; \
		docker buildx create --name cix-builder --driver docker-container --bootstrap; \
	fi
	docker buildx use cix-builder
	@echo "Builder ready. Run: docker login"

# Build and push image for linux/arm64 (Mac M3, Orange Pi 5, etc.)
docker-push-arm64:
	docker buildx build \
		--builder cix-builder \
		--platform linux/arm64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):arm64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):arm64-$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

# Build and push image for linux/amd64 (x86-64 servers, VMs)
docker-push-amd64:
	docker buildx build \
		--builder cix-builder \
		--platform linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):amd64-$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

# Build multi-arch manifest (arm64 + amd64) under :latest
docker-push-all:
	docker buildx build \
		--builder cix-builder \
		--platform linux/arm64,linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

help:
	@echo "=== Claude Code Index ==="
	@echo ""
	@echo "  server-docker      Start API server in Docker"
	@echo "  server-local       Start API server locally (Python 3.11+)"
	@echo "  server-stop        Stop API server (any mode)"
	@echo "  server-status      Check if server is running"
	@echo "  server-logs        Tail server logs"
	@echo ""
	@echo "  docker-setup        Create buildx builder (run once)"
	@echo "  docker-push-arm64   Build & push :arm64  (Mac M3, Orange Pi 5)"
	@echo "  docker-push-amd64   Build & push :amd64  (x86-64 servers)"
	@echo "  docker-push-all     Build & push multi-arch manifest :latest"
	@echo ""
	@echo "  Usage: make docker-push-arm64 DOCKER_USER=yourdockerhubname"
