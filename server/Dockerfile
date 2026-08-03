# syntax=docker/dockerfile:1.7
# CPU multi-stage Dockerfile (multi-arch: linux/amd64 + linux/arm64).
#
# Bundles the CPU llama.cpp embeddings sidecar (llama-server + the ggml .so
# set) so semantic search works out of the box. It is the SAME "ollama"
# provider the CUDA image (Dockerfile.cuda) runs — the only difference
# between the two images is the NVIDIA/CUDA layer; the sidecar ships in both.
# Without it cix-server still boots and serves symbol/keyword search, but
# every semantic query fails ("llama-server not found at /app/llama-server").
#
# The build context is `server/` but the OpenAPI spec lives at
# `<repo-root>/doc/openapi.yaml`. We expose it via a named build context so
# the dashboard stage can `npm run gen:api` against the canonical spec:
#
#   docker buildx build \
#     --build-context openapi=../doc \
#     -f server/Dockerfile server/

ARG VERSION=0.0.0-dev

# ── Stage 1: build the React dashboard ─────────────────────────────────────
# Output lands inside the Go embed.FS root (internal/httpapi/dashboard/dist)
# so the Go build picks it up automatically.
FROM node:24-alpine AS dashboard
WORKDIR /src
RUN apk upgrade --no-cache
# Install deps first for warm cache.
COPY dashboard/package.json dashboard/package-lock.json* ./dashboard/
RUN cd dashboard && npm ci --no-audit --no-fund
# Bring in the spec from the named context, then the rest of the dashboard.
COPY --from=openapi openapi.yaml /spec/openapi.yaml
COPY dashboard/ ./dashboard/
RUN cd dashboard && \
    npx openapi-typescript /spec/openapi.yaml -o src/api/generated.ts && \
    npm run build

# ── Stage: fetch the cloudflared binary (Managed Tunnels feature) ──────────
# Pinned static binary, fetched per target arch. The tunnel is OFF by default
# (CIX_TUNNEL_ENABLED unset) — this only bundles the binary so operators can
# opt in without rebuilding.
#
# THE PIN ROTS SILENTLY. Dependabot only tracks `FROM` images, and CI scans
# the source tree — neither ever sees a binary fetched at image-build time.
# The 2025.2.1 pin sat here unbumped from 2026-05-20 until 2026-07-24 and was
# carrying 46 of the image's 50 HIGH/CRITICAL findings on its own (Go 1.22.10
# stdlib, x/crypto 0.31.0, coredns 1.11.3). When bumping:
#   1. update BOTH Dockerfiles AND `cloudflaredVersion` in
#      internal/tunnels/installer.go (the managed installer downloads its own
#      copy — a stale const there silently re-installs the old binary),
#   2. refresh the checksums below and `pinnedSHA256` in installer.go,
#   3. re-run `make scout-cpu` / `make scout-cuda` before any prod push.
FROM alpine:3.24 AS cloudflared
ARG TARGETARCH
ARG CLOUDFLARED_VERSION=2026.7.3
# SHA-256 of the release artifact, per arch. Cloudflare publishes no checksum
# file, so these are computed from the downloaded assets and pinned here —
# keep them in sync with pinnedSHA256 in internal/tunnels/installer.go.
ARG CLOUDFLARED_SHA256_AMD64=9d71c677db00134c1bd4144b7783486b654ad281b1ea62b4972098d19f770f17
ARG CLOUDFLARED_SHA256_ARM64=65259e652a7bea08bf5df603233ab22b8bf3116af8df9f9206209af6a1b955c0
RUN apk add --no-cache curl ca-certificates
RUN case "${TARGETARCH}" in \
      amd64) want="${CLOUDFLARED_SHA256_AMD64}" ;; \
      arm64) want="${CLOUDFLARED_SHA256_ARM64}" ;; \
      *) echo "no pinned cloudflared sha256 for arch '${TARGETARCH}'" >&2; exit 1 ;; \
    esac && \
    curl -fsSL \
      "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}" \
      -o /cloudflared && \
    echo "${want}  /cloudflared" | sha256sum -c - && \
    chmod 0755 /cloudflared

# ── Stage: fetch the ngrok agent (Managed Tunnels — ngrok provider) ────────
# ngrok ships a single static binary via its equinox CDN stable channel.
# The tunnel is OFF by default; this only bundles the binary so operators
# can pick ngrok from the dashboard without rebuilding.
FROM alpine:3.24 AS ngrok
ARG TARGETARCH
ARG NGROK_CHANNEL=v3-stable
RUN apk add --no-cache curl ca-certificates tar
RUN curl -fsSL \
      "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-${NGROK_CHANNEL}-linux-${TARGETARCH}.tgz" \
      -o /tmp/ngrok.tgz && \
    tar -xzf /tmp/ngrok.tgz -C /usr/local/bin ngrok && \
    chmod 0755 /usr/local/bin/ngrok

FROM golang:1.26-alpine AS builder
ARG VERSION
# Patch Alpine base packages before building to avoid CVEs in the builder layer.
RUN apk upgrade --no-cache
WORKDIR /src

# Copy go.mod / go.sum first for a warm module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Drop the built dashboard into the embed root so `go build` includes it.
COPY --from=dashboard /src/internal/httpapi/dashboard/dist/ ./internal/httpapi/dashboard/dist/
# -w drops DWARF only. Keep the Go symbol table (no -s) so a binary-mode
# vulnerability scan of the shipped image can do symbol-level analysis; on a
# stripped binary govulncheck falls back to module-level reporting and flags
# advisories for packages that are not even linked. Cost: ~+7% on the binary
# (~1.7 MB), negligible inside the image.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-w -X main.version=${VERSION}" \
    -o /out/cix-server \
    ./cmd/cix-server

# Pre-create the /data tree so the final stage can COPY it in with nonroot
# (uid 65532) ownership. Without this, a fresh Docker named volume initialises
# root-owned and the distroless nonroot uid in the runtime stage cannot
# `mkdir /data/sqlite` on first boot. /data/models is pre-created too so the
# separate `cix-models:/data/models` named volume in docker-compose.yml
# inherits 65532 ownership (a fresh volume copies the image dir's uid/gid) —
# otherwise the GGUF cache dir mounts root-owned and the model download fails
# with "mkdir /data/models: permission denied".
RUN mkdir -p /out/data/models

# ── Stage: CPU llama.cpp embeddings sidecar (multi-arch source) ────────────
# Pinned by digest for reproducible builds. The floating :server tag drifts on
# every upstream release and the shared-library layout periodically changes
# (the server logic now lives in libllama-server-impl.so) — an unpinned tag
# means a rebuild silently picks up a new layout and can break the runtime
# copy below. This is the CPU-only MULTI-ARCH index (linux/amd64 + linux/arm64
# + others), so buildx resolves the matching arch automatically for each
# target platform. Bump this digest deliberately and re-run `make scout-cpu`.
# Resolve a new digest with:
#   docker buildx imagetools inspect ghcr.io/ggml-org/llama.cpp:server
FROM ghcr.io/ggml-org/llama.cpp:server@sha256:9e60dd363798776f451bcca97f22f5d75579480e53b0b46303d061a69ceea4df AS llama-source

# ── Stage: distroless runtime ──────────────────────────────────────────────
# gcr.io/distroless/cc-debian13:nonroot (Debian 13 trixie) — NOT static-debian12.
# The bundled llama-server is dynamically linked and needs glibc + libstdc++ +
# libgomp + libssl/libcrypto, none of which exist in the static image. Debian
# 13 is required because llama-server needs GLIBC_2.38 + GLIBCXX_3.4.32 (gcc 13+);
# Debian 12 ships only glibc 2.36 / gcc 12. cc-debian13:nonroot provides every
# system lib llama-server links (verified) + ca-certificates for the first-boot
# HF model download, and keeps the same nonroot uid 65532 as the old image.
FROM gcr.io/distroless/cc-debian13:nonroot
WORKDIR /
COPY --from=builder /out/cix-server /cix-server

# ── Embeddings sidecar: CPU llama.cpp (the "ollama" provider) ──────────────
# Copy the whole /app/*.so* set rather than a fixed list: upstream loads the
# CPU backend variants (libggml-cpu-*.so) via dlopen at runtime and the exact
# file set shifts between releases; a fixed list silently drops a now-required
# .so → llama-server exit 127. The extra unused variants add only a few MB.
COPY --from=llama-source /app/llama-server /app/llama-server
COPY --from=llama-source /app/*.so* /app/
# Distroless has no ldconfig — resolve llama-server's libs via LD_LIBRARY_PATH.
ENV LD_LIBRARY_PATH=/app
ENV CIX_LLAMA_BIN_DIR=/app
# CPU-only: no GPU offload. (The Go default is already 0 on linux; pinned here
# so `docker run` without compose is unambiguous.)
ENV CIX_N_GPU_LAYERS=0
ENV CIX_LLAMA_TRANSPORT=unix
ENV CIX_LLAMA_STARTUP_TIMEOUT=120
# GGUF cache lives on the /data volume (see the cix-models named volume in
# docker-compose.yml); /data/models is pre-owned 65532 in the builder stage.
ENV CIX_GGUF_CACHE_DIR=/data/models
# Managed Tunnels provider binaries (the feature is configured & enabled
# from the dashboard; both are exec'd directly — no shell needed).
COPY --from=cloudflared /cloudflared /cloudflared
ENV CIX_TUNNEL_CLOUDFLARE_BIN_PATH=/cloudflared
COPY --from=ngrok /usr/local/bin/ngrok /ngrok
ENV CIX_TUNNEL_NGROK_BIN_PATH=/ngrok
# Bundled binaries live on a read-only layer, so enable dashboard-managed
# install/update into a writable dir on the /data volume — operators can
# update the agents without rebuilding the image.
ENV CIX_TUNNEL_BIN_MANAGED=true
ENV CIX_TUNNEL_BIN_DIR=/data/tunnel-bin

# Default port — matches CIX_PORT in docker-compose.yml port mapping
# (21847:21847) and the CUDA image (Dockerfile.cuda). The earlier 8001
# default was a Python-FastAPI compat carry-over from the migration era;
# the Python backend was archived 2026-04, so the parity is no longer
# meaningful and the mismatch caused fresh `docker compose up -d` runs
# to leave the host port pointing at a non-listening container port.
ENV CIX_PORT=21847
EXPOSE 21847

# Container data layout — pin paths under /data so the named volume in
# portainer-stack.yml / docker-compose receives both SQLite and chromem-go
# state. The Go default ($HOME/.cix/data) targets local `make run` and is
# inappropriate inside distroless where there is no usable HOME.
ENV CIX_SQLITE_PATH=/data/sqlite/projects.db
ENV CIX_CHROMA_PERSIST_DIR=/data/chroma

# Pre-owned /data directory (distroless has no shell to mkdir+chown).
# A fresh Docker named volume mounted onto /data inherits the 65532:65532
# ownership baked here, so the nonroot runtime user can write the sqlite
# and chroma trees on first boot. Bind mounts still need host-side chown
# to 65532:65532 — see doc/SECURITY_DEPLOYMENT.md.
COPY --from=builder --chown=65532:65532 /out/data /data
VOLUME ["/data"]

USER nonroot:nonroot
ENTRYPOINT ["/cix-server"]
