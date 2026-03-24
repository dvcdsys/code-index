.PHONY: server-local-setup server-local-start server-local-stop server-local-restart \
        server-local-status server-local-logs \
        server-docker-start server-docker-stop server-docker-restart \
        server-docker-status server-docker-logs \
        server-cuda-start server-cuda-stop server-cuda-restart \
        server-cuda-status server-cuda-logs \
        docker-setup docker-push-arm64 docker-push-amd64 docker-push-all \
        docker-push-cuda docker-build-cuda \
        test test-server test-client test-setup help

PORT        ?= 21847
PYTHON      ?= $(shell test -f .venv/bin/python && echo .venv/bin/python || (command -v uv >/dev/null 2>&1 && echo "uv run --python 3.12 python" || echo python3))
DOCKER_USER ?= $(error DOCKER_USER is not set. Run: make docker-push-all DOCKER_USER=yourname)
IMAGE_NAME  ?= code-index
VERSION     ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo latest)
CUDA_TAG    ?= cu130
DATA_DIR    ?= $(HOME)/.cix/data

# ─── Server: Local (native, MPS on Mac) ─────────────────────────────

# First-time setup + start (installs uv, Python 3.12, deps, registers MCP)
server-local-setup:
	./setup-local.sh

# Start server from existing .venv
server-local-start:
	@if [ ! -f .venv/bin/uvicorn ]; then \
		echo "ERROR: Run 'make server-local-setup' first."; \
		exit 1; \
	fi
	@if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
		echo "Already running on port $(PORT)"; \
		exit 0; \
	fi
	@. .env && \
	mkdir -p "$(DATA_DIR)/chroma" "$(DATA_DIR)/sqlite" && \
	echo "Starting server on port $(PORT)..." && \
	cd api && \
	PYTHONPATH="$$(pwd)" \
	API_KEY="$$API_KEY" \
	CHROMA_PERSIST_DIR="$${CHROMA_PERSIST_DIR:-$(DATA_DIR)/chroma}" \
	SQLITE_PATH="$${SQLITE_PATH:-$(DATA_DIR)/sqlite/projects.db}" \
	EMBEDDING_MODEL="$${EMBEDDING_MODEL:-nomic-ai/CodeRankEmbed}" \
	MAX_FILE_SIZE="$${MAX_FILE_SIZE:-524288}" \
	EXCLUDED_DIRS="$${EXCLUDED_DIRS:-node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store}" \
	nohup ../.venv/bin/uvicorn app.main:app \
		--host 0.0.0.0 --port $(PORT) \
		> "$(DATA_DIR)/server.log" 2>&1 & \
	echo "$$!" > "$(DATA_DIR)/server.pid" && \
	echo "PID: $$(cat $(DATA_DIR)/server.pid)" && \
	cd .. && \
	for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
			echo "Healthy: http://localhost:$(PORT)"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "ERROR: Failed to start. Run: make server-local-logs"; exit 1

server-local-stop:
	@if [ -f "$(DATA_DIR)/server.pid" ]; then \
		PID=$$(cat "$(DATA_DIR)/server.pid"); \
		if kill -0 "$$PID" 2>/dev/null; then \
			echo "Stopping server (PID $$PID)..."; \
			kill "$$PID"; \
		fi; \
		rm -f "$(DATA_DIR)/server.pid"; \
	fi
	@PIDS=$$(lsof -ti :$(PORT) 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "Killing process(es) on port $(PORT): $$PIDS"; \
		echo "$$PIDS" | xargs kill 2>/dev/null || true; \
	fi
	@echo "Stopped"

server-local-restart: server-local-stop server-local-start

server-local-status:
	@if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
		echo "Running on port $(PORT)"; \
		curl -sf http://localhost:$(PORT)/health; echo; \
	else \
		echo "Not running"; \
	fi
	@if [ -f "$(DATA_DIR)/server.pid" ] && kill -0 $$(cat "$(DATA_DIR)/server.pid") 2>/dev/null; then \
		echo "PID: $$(cat $(DATA_DIR)/server.pid)"; \
	fi

server-local-logs:
	@if [ -f "$(DATA_DIR)/server.log" ]; then \
		tail -f "$(DATA_DIR)/server.log"; \
	else \
		echo "No log file at $(DATA_DIR)/server.log"; \
	fi

# ─── Server: Docker (CPU, multi-arch) ───────────────────────────────

server-docker-start:
	@if [ ! -f .env ]; then \
		echo "Generating .env..."; \
		API_KEY="cix_$$(openssl rand -hex 32)"; \
		printf "API_KEY=$$API_KEY\nPORT=$(PORT)\nEMBEDDING_MODEL=nomic-ai/CodeRankEmbed\nMAX_FILE_SIZE=524288\nEXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store\n" > .env; \
		echo "Created .env"; \
	fi
	@mkdir -p "$(DATA_DIR)/chroma" "$(DATA_DIR)/sqlite"
	docker compose up -d --build
	@echo "Waiting for health..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
			echo "Healthy: http://localhost:$(PORT)"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "ERROR: Failed to start. Run: make server-docker-logs"; exit 1

server-docker-stop:
	docker compose down

server-docker-restart: server-docker-stop server-docker-start

server-docker-status:
	@docker compose ps
	@if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
		curl -sf http://localhost:$(PORT)/health; echo; \
	fi

server-docker-logs:
	docker compose logs -f

# ─── Server: CUDA (NVIDIA GPU) ──────────────────────────────────────

server-cuda-start:
	@if [ ! -f .env ]; then \
		echo "Generating .env..."; \
		API_KEY="cix_$$(openssl rand -hex 32)"; \
		printf "API_KEY=$$API_KEY\nPORT=$(PORT)\nEMBEDDING_MODEL=nomic-ai/CodeRankEmbed\nMAX_FILE_SIZE=524288\nEXCLUDED_DIRS=node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store\n" > .env; \
		echo "Created .env"; \
	fi
	@mkdir -p "$(DATA_DIR)/chroma" "$(DATA_DIR)/sqlite"
	docker compose -f docker-compose.cuda.yml up -d --build
	@echo "Waiting for health (CUDA)..."
	@for i in $$(seq 1 45); do \
		if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
			echo "Healthy (CUDA): http://localhost:$(PORT)"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "ERROR: Failed to start. Run: make server-cuda-logs"; exit 1

server-cuda-stop:
	docker compose -f docker-compose.cuda.yml down

server-cuda-restart: server-cuda-stop server-cuda-start

server-cuda-status:
	@docker compose -f docker-compose.cuda.yml ps
	@if curl -sf http://localhost:$(PORT)/health > /dev/null 2>&1; then \
		curl -sf http://localhost:$(PORT)/health; echo; \
	fi

server-cuda-logs:
	docker compose -f docker-compose.cuda.yml logs -f

# ─── Build & Push ───────────────────────────────────────────────────

docker-setup:
	@if ! docker buildx inspect cix-builder > /dev/null 2>&1; then \
		echo "Creating buildx builder 'cix-builder'..."; \
		docker buildx create --name cix-builder --driver docker-container --bootstrap; \
	fi
	docker buildx use cix-builder
	@echo "Builder ready. Run: docker login"

docker-push-arm64:
	docker buildx build \
		--builder cix-builder \
		--platform linux/arm64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):arm64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):arm64-$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

docker-push-amd64:
	docker buildx build \
		--builder cix-builder \
		--platform linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):amd64-$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

docker-build-cuda:
	docker build \
		--platform linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(CUDA_TAG) \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(VERSION)-$(CUDA_TAG) \
		--file api/Dockerfile.cuda \
		.

docker-push-cuda:
	docker buildx build \
		--builder cix-builder \
		--platform linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(CUDA_TAG) \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(VERSION)-$(CUDA_TAG) \
		--file api/Dockerfile.cuda \
		--push \
		.

docker-push-all:
	docker buildx build \
		--builder cix-builder \
		--platform linux/arm64,linux/amd64 \
		--tag $(DOCKER_USER)/$(IMAGE_NAME):$(VERSION) \
		--file api/Dockerfile \
		--push \
		.

# ─── Tests ───────────────────────────────────────────────────────────

test-setup:
	$(PYTHON) -m pip install -r api/requirements-dev.txt

test: test-server test-client

test-server:
	$(PYTHON) -m pytest api/ -v; code=$$?; [ $$code -eq 5 ] && exit 0 || exit $$code

test-client:
	cd cli && go test -v ./...

# ─── Help ────────────────────────────────────────────────────────────

help:
	@echo "=== Claude Code Index ==="
	@echo ""
	@echo "Server — Local (native, MPS on Mac):"
	@echo "  server-local-setup    First-time setup (installs uv, Python, deps)"
	@echo "  server-local-start    Start server"
	@echo "  server-local-stop     Stop server"
	@echo "  server-local-restart  Restart server"
	@echo "  server-local-status   Check status"
	@echo "  server-local-logs     Tail logs"
	@echo ""
	@echo "Server — Docker (CPU):"
	@echo "  server-docker-start   Start server"
	@echo "  server-docker-stop    Stop server"
	@echo "  server-docker-restart Restart server"
	@echo "  server-docker-status  Check status"
	@echo "  server-docker-logs    Tail logs"
	@echo ""
	@echo "Server — CUDA (NVIDIA GPU):"
	@echo "  server-cuda-start     Start server"
	@echo "  server-cuda-stop      Stop server"
	@echo "  server-cuda-restart   Restart server"
	@echo "  server-cuda-status    Check status"
	@echo "  server-cuda-logs      Tail logs"
	@echo ""
	@echo "Build & Push:"
	@echo "  docker-setup          Create buildx builder (run once)"
	@echo "  docker-push-arm64     Build & push :arm64"
	@echo "  docker-push-amd64     Build & push :amd64"
	@echo "  docker-push-cuda      Build & push :$(CUDA_TAG) + :$(VERSION)-$(CUDA_TAG)"
	@echo "  docker-push-all       Build & push multi-arch :latest"
	@echo ""
	@echo "Tests:"
	@echo "  test                  Run all tests"
	@echo "  test-server           Python API tests"
	@echo "  test-client           Go CLI tests"