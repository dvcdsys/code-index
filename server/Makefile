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

.PHONY: help build test test-gate fetch-llama bundle docker-build-cuda clean

help:
	@echo "Targets:"
	@echo "  build              — go build cmd/cix-server into dist/$(BUNDLE_NAME)/cix-server"
	@echo "  test               — go test ./... (no llama-server required)"
	@echo "  fetch-llama        — download + SHA256-verify llama.cpp $(LLAMA_VERSION) for $(OS)-$(ARCH)"
	@echo "  bundle             — build + fetch-llama, assemble dist/$(BUNDLE_NAME)/ tree"
	@echo "  test-gate          — run the Phase 3 parity gate (requires fetch-llama + GGUF)"
	@echo "  docker-build-cuda  — build + push linux/amd64 CUDA image $(IMAGE_REPO):$(IMAGE_TAG)"
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

test-gate:
	@# The gate depends on a local GGUF and a fetched llama-server. The dev
	@# fallback inside config.Validate reads bench/results/reference_gguf_path.txt
	@# when CIX_GGUF_PATH is unset, so this target Just Works on the dev
	@# machine that produced the reference vectors.
	CIX_LLAMA_BIN_DIR=$(LLAMA_DIR) \
	$(GO) test -tags=embed_gate -v -run TestEmbeddingParity ./internal/embeddings/...

docker-build-cuda:
	docker buildx build \
	  --platform linux/amd64 \
	  --build-arg VERSION=$(VERSION) \
	  -f $(ROOT)/Dockerfile.cuda \
	  -t $(IMAGE_REPO):$(IMAGE_TAG) \
	  --push \
	  $(ROOT)

clean:
	rm -rf $(DIST_DIR)
