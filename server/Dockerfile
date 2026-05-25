# syntax=docker/dockerfile:1.7
# Phase 1 CPU-only multi-stage Dockerfile.
# Embeddings (CUDA / llama-server sidecar) land in Phase 3.
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
FROM node:20-alpine AS dashboard
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
# opt in without rebuilding. Bump CLOUDFLARED_VERSION deliberately and re-run
# `make scout-cpu` before any prod push. Optionally pass CLOUDFLARED_SHA256 to
# verify the download.
FROM alpine:3.20 AS cloudflared
ARG TARGETARCH
ARG CLOUDFLARED_VERSION=2025.2.1
ARG CLOUDFLARED_SHA256=""
RUN apk add --no-cache curl ca-certificates
RUN curl -fsSL \
      "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}" \
      -o /cloudflared && \
    if [ -n "${CLOUDFLARED_SHA256}" ]; then \
      echo "${CLOUDFLARED_SHA256}  /cloudflared" | sha256sum -c - ; \
    fi && \
    chmod 0755 /cloudflared

# ── Stage: fetch the ngrok agent (Managed Tunnels — ngrok provider) ────────
# ngrok ships a single static binary via its equinox CDN stable channel.
# The tunnel is OFF by default; this only bundles the binary so operators
# can pick ngrok from the dashboard without rebuilding.
FROM alpine:3.20 AS ngrok
ARG TARGETARCH
ARG NGROK_CHANNEL=v3-stable
RUN apk add --no-cache curl ca-certificates tar
RUN curl -fsSL \
      "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-${NGROK_CHANNEL}-linux-${TARGETARCH}.tgz" \
      -o /tmp/ngrok.tgz && \
    tar -xzf /tmp/ngrok.tgz -C /usr/local/bin ngrok && \
    chmod 0755 /usr/local/bin/ngrok

FROM golang:1.25-alpine AS builder
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
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/cix-server \
    ./cmd/cix-server

# Pre-create an empty /data tree so the final stage can COPY it in with
# nonroot (uid 65532) ownership. Without this, a fresh Docker named volume
# initialises root-owned and the distroless nonroot uid in the runtime
# stage cannot `mkdir /data/sqlite` on first boot.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/cix-server /cix-server
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
