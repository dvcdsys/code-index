# server/Makefile — Phase 3 build, fetch-llama, bundle, and gate targets.
#
# The llama.cpp upstream asset is pinned by LLAMA_VERSION so a fetch is
# reproducible across machines. Bumping the version is deliberately a separate
# commit — the new SHA256 must be appended to scripts/llama-checksums.txt and
# the parity gate re-run before shipping.

LLAMA_VERSION ?= b10238
LLAMA_REPO    ?= ggml-org/llama.cpp

# Where fetch-llama.sh keeps checksum-verified release archives. Outside the
# repo on purpose: it survives `make clean` and a fresh clone, which is what
# makes a version bump (or a re-clone) cost no network.
LLAMA_CACHE_DIR ?= $(if $(XDG_CACHE_HOME),$(XDG_CACHE_HOME),$(HOME)/.cache)/cix/llama

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

# Version stamped into the binary (-X main.version) and into Docker images.
#
# The tag stream must be filtered. `git describe --tags` without --match picks
# whichever namespace happens to be nearest, and this repo has three of them
# (server/v*, cli/v*, mac/v*) — on develop it resolved to a CLI tag, so every
# locally built *server* image was labelled with a *CLI* version. CI never hit
# this (release-server.yml passes VERSION= explicitly from the tag), which is
# why it went unnoticed; `make build` / `bundle` / `docker-build-cuda*` did.
#
# Note the tag must be reachable from HEAD for describe to see it. Release
# workflows always pass the version in explicitly rather than deriving it.
SERVER_VERSION ?= $(shell git describe --tags --match 'server/v*' 2>/dev/null | sed 's|^server/v||')
ifeq ($(strip $(SERVER_VERSION)),)
SERVER_VERSION := 0.0.0-dev
endif
VERSION     ?= $(SERVER_VERSION)

# -w drops DWARF only. Do NOT add -s: govulncheck -mode=binary needs the Go
# symbol table for symbol-level analysis; on a stripped binary it falls back to
# module-level reporting and flags advisories for packages that are not linked.
# Same rationale, and same flags, as server/Dockerfile:98-106 and release-cli.yml.
LDFLAGS     ?= -w -X main.version=$(SERVER_VERSION)

# Floating dev tag — overwritten on every `docker-build-cuda-dev` push. Used
# by the operator to deploy "current local work" onto the RTX 3090 box for
# manual smoke testing before merging a PR.
DEV_TAG     ?= cu128-dev
GIT_SHA     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "nogit")

BUILDER     ?= cix-builder
SCOUT_TAG   ?= scout-$(shell date +%Y%m%d-%H%M)

.PHONY: help build test test-gate fetch-llama bundle run docker-build-cuda \
        docker-build-cuda-dev scout-cuda scout-cpu promote-cuda clean \
        openapi-gen openapi-check swagger-ui-fetch \
        dashboard-deps dashboard-gen-types dashboard-build dashboard-dev dashboard-clean

# Pin the Swagger UI bundle version. Bumping this is a deliberate commit:
# update the version, run `make swagger-ui-fetch`, eyeball the diff, ship.
SWAGGER_UI_VERSION ?= 5.18.2

# Dashboard frontend — Vite project under server/dashboard/. Output lands in
# internal/httpapi/dashboard/dist/ and is picked up automatically by the Go
# embed.FS in dashboard/embed.go.
DASHBOARD_DIR := $(ROOT)/dashboard
DASHBOARD_OUT := $(ROOT)/internal/httpapi/dashboard/dist

help:
	@echo "Targets:"
	@echo "  build              — dashboard-build + go build cmd/cix-server into dist/$(BUNDLE_NAME)/cix-server (stamps version $(SERVER_VERSION))"
	@echo "  test               — go test ./... (no llama-server required)"
	@echo "  fetch-llama        — download + SHA256-verify llama.cpp $(LLAMA_VERSION) for $(OS)-$(ARCH) (LLAMA_STRICT=1 to require a recorded checksum)"
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
	@echo "  dashboard-deps     — npm ci inside server/dashboard/"
	@echo "  dashboard-gen-types — regenerate src/api/generated.ts from doc/openapi.yaml"
	@echo "  dashboard-build    — build the React dashboard into $(DASHBOARD_OUT)"
	@echo "  dashboard-dev      — run vite dev server on :5173 (proxies /api to :21847)"
	@echo "  dashboard-clean    — remove built dashboard artefacts (keeps PLACEHOLDER.html)"
	@echo "  clean              — remove dist/"
	@echo ""
	@echo "build / bundle / run skip any step that would reproduce identical output."
	@echo "Force one:"
	@echo "  LLAMA_FORCE=1      — refetch llama.cpp, restage dist/llama/, rebuild the bundle"
	@echo "  BUNDLE_FORCE=1     — re-copy + re-sign the bundle's llama/ only"
	@echo "  DASHBOARD_FORCE=1  — rebuild the React dashboard"
	@echo "Verified llama.cpp archives are cached in $(LLAMA_CACHE_DIR) (override LLAMA_CACHE_DIR)."

# `go build` embeds internal/httpapi/dashboard/dist/ via go:embed — the
# dashboard must be built first so the binary picks up fresh assets.
#
# The ldflags are not optional cosmetics: without them `cix-server -v` reports
# the zero value of main.version, so every non-Docker build (make bundle, the
# macOS .app, install-server.sh --mode native) shipped an unidentifiable binary.
build: dashboard-build
	mkdir -p $(BUNDLE_DIR)
	$(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BUNDLE_DIR)/cix-server ./cmd/cix-server

test:
	$(GO) test ./...

# LLAMA_STRICT=1 turns the first-run "record the checksum" convenience into a
# hard failure. Release builds set it so an upstream asset that was swapped
# after the pin was recorded cannot be silently shipped.
LLAMA_STRICT ?= 0

fetch-llama:
	LLAMA_VERSION=$(LLAMA_VERSION) \
	LLAMA_REPO=$(LLAMA_REPO) \
	LLAMA_OS=$(OS) \
	LLAMA_ARCH=$(ARCH) \
	LLAMA_STRICT=$(LLAMA_STRICT) \
	LLAMA_FORCE=$(LLAMA_FORCE) \
	LLAMA_CACHE_DIR=$(LLAMA_CACHE_DIR) \
	DEST_DIR=$(LLAMA_DIR) \
	CHECKSUMS_FILE=$(ROOT)/scripts/llama-checksums.txt \
	$(ROOT)/scripts/fetch-llama.sh

bundle: fetch-llama build
	@# Copy the llama/ directory next to cix-server so the runtime's
	@# filepath.Dir(os.Executable())/llama default resolves correctly.
	@# Wipe it first: `cp -R` merges, so without this the bundle accumulates
	@# every dylib of every version ever built here. fetch-llama.sh cleans its
	@# own staging dir for exactly this reason ("stale dylibs could get picked
	@# up by DYLD") — but the bundle is the directory that actually ships and
	@# runs. A b8914 bundle upgraded in place kept 9 stale dylibs (13 MB),
	@# including libllama.0.0.8914.dylib, sitting next to the b10238 set.
	@#
	@# …and skip the whole thing when the bundle already holds the exact
	@# version staged in dist/llama. `make run` goes through here on every
	@# server restart; copying 52 MB and re-signing it to produce a
	@# byte-identical tree is pure latency. The comparison is on the
	@# .llama-version stamp fetch-llama.sh writes last, so a bundle assembled
	@# from an interrupted fetch has no stamp and is always rebuilt.
	@# BUNDLE_FORCE=1 rebuilds regardless.
	@#
	@# When the copy DOES run on macOS, the xattr strip + deep re-sign run with
	@# it, every time. Sequoia (26+) tightened amfid: ad-hoc-signed binaries
	@# whose linked dylibs carry stale signatures or a com.apple.provenance
	@# xattr from the previous bundle get SIGKILL'd within milliseconds of
	@# execve — the supervisor sees "signal: killed" with empty stderr. `cp -R`
	@# creates new files macOS treats as untrusted, so re-signing is tied to
	@# the copy and must never be skipped independently of it.
	@# LLAMA_FORCE implies BUNDLE_FORCE. A forced refetch means the operator
	@# distrusts what is in dist/llama; re-staging it and then leaving the
	@# bundle on its old copy — which the stamp comparison would happily do,
	@# the version being unchanged — would defeat the point of asking.
	@if [ "$(BUNDLE_FORCE)" != "1" ] && [ "$(LLAMA_FORCE)" != "1" ] \
	    && [ -x "$(BUNDLE_DIR)/llama/llama-server" ] \
	    && [ -f "$(LLAMA_DIR)/.llama-version" ] \
	    && cmp -s "$(BUNDLE_DIR)/llama/.llama-version" "$(LLAMA_DIR)/.llama-version"; then \
	    echo "→ bundle llama/ already at $$(cat $(LLAMA_DIR)/.llama-version) — skipping copy + codesign"; \
	else \
	    rm -rf $(BUNDLE_DIR)/llama; \
	    mkdir -p $(BUNDLE_DIR)/llama; \
	    cp -R $(LLAMA_DIR)/. $(BUNDLE_DIR)/llama/; \
	    if [ "$(OS)" = "darwin" ]; then \
	        xattr -cr $(BUNDLE_DIR)/llama/; \
	        codesign --force --deep --sign - $(BUNDLE_DIR)/llama/llama-server; \
	    fi; \
	fi
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
	@# The gate depends on a local GGUF and a fetched llama-server. PR-E
	@# removed the implicit config.Validate fallback that used to read
	@# bench/results/reference_gguf_path.txt — the dashboard now requires
	@# operators to pick a model source explicitly, so we do the same here:
	@# read the file (if it exists) and stamp CIX_GGUF_PATH for the test
	@# run only.
	@if [ ! -f bench/results/reference_gguf_path.txt ]; then \
		echo "ERROR: bench/results/reference_gguf_path.txt missing — run bench/emit_reference_embeddings.py first."; \
		exit 1; \
	fi
	CIX_LLAMA_BIN_DIR=$(LLAMA_DIR) \
	CIX_GGUF_PATH="$$(cat bench/results/reference_gguf_path.txt)" \
	$(GO) test -tags=embed_gate -v -run TestEmbeddingParity ./internal/embeddings/...

# NGROK_NOCACHE forces the `ngrok` stage to re-run on every build. ngrok is
# served from a floating "v3-stable" channel with no per-version URL, so its
# RUN layer would otherwise sit in the builder cache indefinitely and keep
# baking in whatever build first populated it — "floating" is not the same as
# "fresh" once buildx caches the download. The cloudflared stage needs no such
# flag: its ARG is an exact version, so a bump invalidates the layer by itself.
# CI builds run on clean runners with no layer cache, so this is a
# local/dev-builder concern only.
NGROK_NOCACHE := --no-cache-filter=ngrok

docker-build-cuda:
	docker buildx build \
	  --builder $(BUILDER) \
	  --platform linux/amd64 \
	  $(NGROK_NOCACHE) \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION) \
	  --build-context openapi=$(ROOT)/../doc \
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
	  $(NGROK_NOCACHE) \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION)-dev-$(GIT_SHA) \
	  --build-context openapi=$(ROOT)/../doc \
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
	  $(NGROK_NOCACHE) \
	  --pull \
	  --provenance=mode=max \
	  --sbom=true \
	  --build-arg VERSION=$(VERSION) \
	  --build-context openapi=$(ROOT)/../doc \
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
	  $(NGROK_NOCACHE) \
	  --build-arg VERSION=$(VERSION) \
	  --build-context openapi=$(ROOT)/../doc \
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

# Dashboard frontend — Vite + React project under server/dashboard/.
#
# Output lives at $(DASHBOARD_OUT) (= internal/httpapi/dashboard/dist) which
# is referenced by `//go:embed dist` in dashboard/embed.go. After
# `dashboard-build` finishes, a regular `go build` picks up the bundle
# automatically — no extra wiring.

dashboard-deps:
	cd $(DASHBOARD_DIR) && npm ci

dashboard-gen-types:
	cd $(DASHBOARD_DIR) && npm run gen:api

# `dashboard-deps` runs `npm ci` which is strict about lockfile match. For
# day-to-day rebuilds where node_modules already exists this is wasteful;
# fall through to a no-op when node_modules is present and the lockfile
# hasn't changed since the last install.
#
# The build itself is content-addressed against DASHBOARD_STAMP: `make run`
# pulls this target in on every server restart, and re-running tsc + vite to
# emit the same bundle is the single biggest chunk of a no-op rebuild. The
# stamp records the digest of every build input (see the script for what
# counts) and is written only after a build succeeds, so an interrupted or
# failing build never leaves a stamp that would suppress the retry.
#
# Escape hatches: DASHBOARD_FORCE=1 to rebuild anyway; `make dashboard-clean`
# drops the stamp along with the output.
DASHBOARD_STAMP := $(DASHBOARD_DIR)/.build-stamp

dashboard-build:
	@if [ ! -d "$(DASHBOARD_DIR)/node_modules" ]; then \
	    echo "→ npm ci (no node_modules/)"; \
	    cd $(DASHBOARD_DIR) && npm ci; \
	fi
	@want=$$($(ROOT)/scripts/dashboard-inputs-hash.sh "$(DASHBOARD_DIR)" "$(ROOT)/../doc/openapi.yaml"); \
	if [ "$(DASHBOARD_FORCE)" != "1" ] \
	    && [ -f "$(DASHBOARD_OUT)/index.html" ] \
	    && [ -f "$(DASHBOARD_STAMP)" ] \
	    && [ "$$(cat $(DASHBOARD_STAMP))" = "$$want" ]; then \
	    echo "→ dashboard unchanged ($${want}) — skipping build (DASHBOARD_FORCE=1 to rebuild)"; \
	else \
	    rm -f "$(DASHBOARD_STAMP)"; \
	    (cd $(DASHBOARD_DIR) && npm run gen:api && npm run build) || exit 1; \
	    printf '%s\n' "$$want" > "$(DASHBOARD_STAMP)"; \
	fi

dashboard-dev: dashboard-gen-types
	cd $(DASHBOARD_DIR) && npm run dev

dashboard-clean:
	@# Wipe everything in dist/ except the committed .gitkeep so that
	@# `//go:embed all:dist` still has at least one entry afterwards
	@# (otherwise `go build` would fail with "embed: no matching files").
	@if [ -d "$(DASHBOARD_OUT)" ]; then \
	    find $(DASHBOARD_OUT) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +; \
	    echo "→ Cleaned $(DASHBOARD_OUT) (kept .gitkeep)"; \
	fi
	@# The stamp lives outside dist/ (it must not end up inside the embed FS),
	@# so it has to be dropped explicitly — otherwise the next build sees a
	@# matching digest and skips, leaving dist/ empty.
	@rm -f "$(DASHBOARD_STAMP)"
