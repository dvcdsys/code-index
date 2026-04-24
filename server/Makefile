# server/Makefile — Phase 3 build, fetch-llama, bundle, and gate targets.
#
# The llama.cpp upstream asset is pinned by LLAMA_VERSION so a fetch is
# reproducible across machines. Bumping the version is deliberately a separate
# commit — the new SHA256 must be appended to scripts/llama-checksums.txt and
# the parity gate re-run before shipping.

LLAMA_VERSION ?= b8914
LLAMA_REPO    ?= ggml-org/llama.cpp

# `make bundle OS=... ARCH=...` supports only darwin-arm64 in Phase 3.
OS   ?= darwin
ARCH ?= arm64

ROOT        := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
DIST_DIR    := $(ROOT)/dist
LLAMA_DIR   := $(DIST_DIR)/llama
BUNDLE_NAME := cix-$(OS)-$(ARCH)
BUNDLE_DIR  := $(DIST_DIR)/$(BUNDLE_NAME)

GO      ?= go
GOFLAGS ?=

IMAGE_REPO  ?= dvcdsys/code-index
IMAGE_TAG   ?= go-cu128
VERSION     ?= $(shell git describe --tags --always 2>/dev/null || echo "0.0.0-dev")

BUILDER     ?= cix-builder
SCOUT_TAG   ?= scout-$(shell date +%Y%m%d-%H%M)

.PHONY: help build test test-gate fetch-llama bundle run docker-build-cuda \
        scout-cuda scout-cpu promote-cuda clean

help:
	@echo "Targets:"
	@echo "  build              — go build cmd/cix-server into dist/$(BUNDLE_NAME)/cix-server"
	@echo "  test               — go test ./... (no llama-server required)"
	@echo "  fetch-llama        — download + SHA256-verify llama.cpp $(LLAMA_VERSION) for $(OS)-$(ARCH)"
	@echo "  bundle             — build + fetch-llama, assemble dist/$(BUNDLE_NAME)/ tree"
	@echo "  run                — bundle + launch cix-server (reads .env from repo root, sets CIX_LLAMA_BIN_DIR)"
	@echo "  test-gate          — run the Phase 3 parity gate (requires fetch-llama + GGUF)"
	@echo "  docker-build-cuda  — build + push linux/amd64 CUDA image $(IMAGE_REPO):$(IMAGE_TAG)"
	@echo "  scout-cuda         — build CUDA image via native x86 builder → push :<SCOUT_TAG> → docker scout cves"
	@echo "  scout-cpu          — build CPU image locally → docker scout cves (no push)"
	@echo "  promote-cuda       — retag SCOUT_TAG as go-cu128+cu128 without rebuild (imagetools)"
	@echo "  clean              — remove dist/"

build:
	mkdir -p $(BUNDLE_DIR)
	$(GO) build $(GOFLAGS) -o $(BUNDLE_DIR)/cix-server ./cmd/cix-server

test:
	$(GO) test ./...

fetch-llama:
	LLAMA_VERSION=$(LLAMA_VERSION) \
	LLAMA_REPO=$(LLAMA_REPO) \
	LLAMA_OS=$(OS) \
	LLAMA_ARCH=$(ARCH) \
	DEST_DIR=$(LLAMA_DIR) \
	CHECKSUMS_FILE=$(ROOT)/scripts/llama-checksums.txt \
	$(ROOT)/scripts/fetch-llama.sh

bundle: fetch-llama build
	@# Copy the llama/ directory next to cix-server so the runtime's
	@# filepath.Dir(os.Executable())/llama default resolves correctly.
	mkdir -p $(BUNDLE_DIR)/llama
	cp -R $(LLAMA_DIR)/. $(BUNDLE_DIR)/llama/
	@echo "Bundle ready: $(BUNDLE_DIR)"
	@echo "Optional:    tar czf $(DIST_DIR)/$(BUNDLE_NAME).tar.gz -C $(DIST_DIR) $(BUNDLE_NAME)"

# run — native macOS development target with Metal GPU acceleration.
# Reads env vars from <repo-root>/.env (copy from .env.example and fill in
# CIX_API_KEY), then launches cix-server with CIX_LLAMA_BIN_DIR pointing at
# the bundled Metal-enabled llama-server.
#
# Usage:
#   make run                  — start server on default port 21847
#   make run CIX_PORT=8080    — override port via extra env var
ENV_FILE := $(ROOT)/../.env
run: bundle
	@[ -f "$(ENV_FILE)" ] || { \
	    echo "ERROR: $(ENV_FILE) not found."; \
	    echo "       cp .env.example .env  and set CIX_API_KEY before running."; \
	    exit 1; \
	}
	env $$(grep -v '^[[:space:]]*[#;]' "$(ENV_FILE)" | grep -v '^[[:space:]]*$$' | xargs) \
	    CIX_LLAMA_BIN_DIR="$(BUNDLE_DIR)/llama" \
	    "$(BUNDLE_DIR)/cix-server"

test-gate:
	@# The gate depends on a local GGUF and a fetched llama-server. The dev
	@# fallback inside config.Validate reads bench/results/reference_gguf_path.txt
	@# when CIX_GGUF_PATH is unset, so this target Just Works on the dev
	@# machine that produced the reference vectors.
	CIX_LLAMA_BIN_DIR=$(LLAMA_DIR) \
	$(GO) test -tags=embed_gate -v -run TestEmbeddingParity ./internal/embeddings/...

docker-build-cuda:
	docker buildx build \
	  --builder $(BUILDER) \
	  --platform linux/amd64 \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION) \
	  -f $(ROOT)/Dockerfile.cuda \
	  -t $(IMAGE_REPO):$(IMAGE_TAG) \
	  --push \
	  $(ROOT)

# Scout workflow — iterate locally before touching production tags:
#
#   make scout-cuda                     # build on native x86 → :scout-YYYYMMDD-HHMM → scan
#   make promote-cuda SCOUT_TAG=scout-… # if clean: retag → :go-cu128 + :cu128 (no rebuild)
#
#   make scout-cpu                      # CPU image: build locally → scan (no push)

scout-cuda:
	@echo "→ Building CUDA image on native x86 builder ($(BUILDER)) → $(IMAGE_REPO):$(SCOUT_TAG)"
	docker buildx build \
	  --builder $(BUILDER) \
	  --platform linux/amd64 \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION) \
	  -f $(ROOT)/Dockerfile.cuda \
	  -t $(IMAGE_REPO):$(SCOUT_TAG) \
	  --push \
	  $(ROOT)
	@echo "→ Scanning $(IMAGE_REPO):$(SCOUT_TAG) — HIGH + CRITICAL only"
	docker scout cves $(IMAGE_REPO):$(SCOUT_TAG) --platform linux/amd64 --only-severity HIGH,CRITICAL
	@echo ""
	@echo "If clean: make promote-cuda SCOUT_TAG=$(SCOUT_TAG)"

promote-cuda:
	@[ -n "$(SCOUT_TAG)" ] || (echo "ERROR: set SCOUT_TAG=scout-YYYYMMDD-HHMM"; exit 1)
	@echo "→ Promoting $(IMAGE_REPO):$(SCOUT_TAG) → :go-cu128 and :cu128"
	docker buildx imagetools create \
	  -t $(IMAGE_REPO):go-cu128 \
	  -t $(IMAGE_REPO):cu128 \
	  $(IMAGE_REPO):$(SCOUT_TAG)

scout-cpu:
	@echo "→ Building CPU image locally"
	docker buildx build \
	  --load \
	  --build-arg VERSION=$(VERSION) \
	  -t $(IMAGE_REPO):scout-cpu-tmp \
	  -f $(ROOT)/Dockerfile \
	  $(ROOT)
	@echo "→ Scanning $(IMAGE_REPO):scout-cpu-tmp — HIGH + CRITICAL only"
	docker scout cves $(IMAGE_REPO):scout-cpu-tmp --only-severity HIGH,CRITICAL
	@docker rmi $(IMAGE_REPO):scout-cpu-tmp 2>/dev/null; true

clean:
	rm -rf $(DIST_DIR)
