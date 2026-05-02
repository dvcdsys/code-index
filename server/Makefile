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

# Floating dev tag — overwritten on every `docker-build-cuda-dev` push. Used
# by the operator to deploy "current local work" onto the RTX 3090 box for
# manual smoke testing before merging a PR.
DEV_TAG     ?= cu128-dev
GIT_SHA     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "nogit")

BUILDER     ?= cix-builder
SCOUT_TAG   ?= scout-$(shell date +%Y%m%d-%H%M)

.PHONY: help build test test-gate fetch-llama bundle run docker-build-cuda \
        docker-build-cuda-dev scout-cuda scout-cpu promote-cuda clean \
        openapi-gen openapi-check swagger-ui-fetch

# Pin the Swagger UI bundle version. Bumping this is a deliberate commit:
# update the version, run `make swagger-ui-fetch`, eyeball the diff, ship.
SWAGGER_UI_VERSION ?= 5.18.2

help:
	@echo "Targets:"
	@echo "  build              — go build cmd/cix-server into dist/$(BUNDLE_NAME)/cix-server"
	@echo "  test               — go test ./... (no llama-server required)"
	@echo "  fetch-llama        — download + SHA256-verify llama.cpp $(LLAMA_VERSION) for $(OS)-$(ARCH)"
	@echo "  bundle             — build + fetch-llama, assemble dist/$(BUNDLE_NAME)/ tree"
	@echo "  run                — bundle + launch cix-server (reads .env from repo root, sets CIX_LLAMA_BIN_DIR)"
	@echo "  test-gate          — run the Phase 3 parity gate (requires fetch-llama + GGUF)"
	@echo "  docker-build-cuda  — build + push linux/amd64 CUDA image $(IMAGE_REPO):$(IMAGE_TAG)"
	@echo "  docker-build-cuda-dev — build + push CUDA dev image $(IMAGE_REPO):$(DEV_TAG)"
	@echo "  scout-cuda         — build CUDA image via native x86 builder → push :<SCOUT_TAG> → docker scout cves"
	@echo "  scout-cpu          — build CPU image locally → docker scout cves (no push)"
	@echo "  promote-cuda       — retag SCOUT_TAG as go-cu128+cu128 without rebuild (imagetools)"
	@echo "  openapi-gen        — regenerate internal/httpapi/openapi/openapi.gen.go from doc/openapi.yaml"
	@echo "  openapi-check      — fail if openapi.gen.go is out of sync with doc/openapi.yaml (CI)"
	@echo "  swagger-ui-fetch   — refresh internal/httpapi/docs/swagger-ui/ from jsdelivr (SWAGGER_UI_VERSION=$(SWAGGER_UI_VERSION))"
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

# Local dev workflow for the RTX 3090 box (the GPU prod server reachable via
# Portainer MCP). Builds the CUDA image with a "dev" tag and pushes to Docker
# Hub from the dev machine. Operator then bumps the Portainer stack image
# tag to :cu128-dev (or pulls fresh) and smoke-tests on real hardware before
# opening a PR / cutting a versioned release.
#
# Skips Docker Scout — this image is for the operator's eyes, not for
# downstream users; run `make scout-cuda` before promoting to :cu128/:go-cu128.
docker-build-cuda-dev:
	@echo "→ Building CUDA dev image on $(BUILDER) → $(IMAGE_REPO):$(DEV_TAG)"
	docker buildx build \
	  --builder $(BUILDER) \
	  --platform linux/amd64 \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION)-dev-$(GIT_SHA) \
	  -f $(ROOT)/Dockerfile.cuda \
	  -t $(IMAGE_REPO):$(DEV_TAG) \
	  --push \
	  $(ROOT)
	@echo ""
	@echo "✓ Pushed $(IMAGE_REPO):$(DEV_TAG)"
	@echo ""
	@echo "Next: bump the Portainer stack image to :$(DEV_TAG) and redeploy,"
	@echo "      then smoke-test on the RTX 3090 box."

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

# openapi-gen — regenerate the chi server interface + types from doc/openapi.yaml.
# The spec is the source of truth; generated code is committed to git so consumers
# can `go install` without needing the oapi-codegen tool installed.
openapi-gen:
	$(GO) generate ./internal/httpapi/openapi/...

# openapi-check — used in CI to ensure the committed .gen.go is in sync with
# doc/openapi.yaml. Regenerates and fails if `git diff` is non-empty.
openapi-check: openapi-gen
	@if ! git diff --quiet internal/httpapi/openapi/openapi.gen.go; then \
	    echo "ERROR: internal/httpapi/openapi/openapi.gen.go is out of sync with doc/openapi.yaml"; \
	    echo "       Run 'make openapi-gen' and commit the result."; \
	    git --no-pager diff internal/httpapi/openapi/openapi.gen.go | head -40; \
	    exit 1; \
	fi
	@echo "openapi.gen.go is in sync with doc/openapi.yaml"

# swagger-ui-fetch — refresh the embedded Swagger UI bundle from jsdelivr.
# Run when bumping SWAGGER_UI_VERSION; commit the diff in the same PR as the
# version bump so reviewers can see what changed in the bundle.
SWAGGER_UI_DIR := $(ROOT)/internal/httpapi/docs/swagger-ui
SWAGGER_UI_BASE = https://cdn.jsdelivr.net/npm/swagger-ui-dist@$(SWAGGER_UI_VERSION)
swagger-ui-fetch:
	@echo "→ Fetching Swagger UI $(SWAGGER_UI_VERSION) into $(SWAGGER_UI_DIR)"
	@for f in swagger-ui.css swagger-ui-bundle.js swagger-ui-standalone-preset.js favicon-32x32.png favicon-16x16.png; do \
	    echo "   $$f"; \
	    curl -fsSL -o "$(SWAGGER_UI_DIR)/$$f" "$(SWAGGER_UI_BASE)/$$f" || { echo "FAIL: $$f"; exit 1; }; \
	done
	@echo "✓ Swagger UI bundle refreshed. index.html is hand-maintained — review the diff before committing."
