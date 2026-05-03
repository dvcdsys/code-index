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

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/cix-server /cix-server

# Default port; override with CIX_PORT at runtime.
ENV CIX_PORT=8001
EXPOSE 8001

# Container data layout — pin paths under /data so the named volume in
# portainer-stack.yml / docker-compose receives both SQLite and chromem-go
# state. The Go default ($HOME/.cix/data) targets local `make run` and is
# inappropriate inside distroless where there is no usable HOME.
ENV CIX_SQLITE_PATH=/data/sqlite/projects.db
ENV CIX_CHROMA_PERSIST_DIR=/data/chroma
VOLUME ["/data"]

USER nonroot:nonroot
ENTRYPOINT ["/cix-server"]
