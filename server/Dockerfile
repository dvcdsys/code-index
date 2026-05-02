# syntax=docker/dockerfile:1.7
# Phase 1 CPU-only multi-stage Dockerfile.
# Embeddings (CUDA / llama-server sidecar) land in Phase 3.

ARG VERSION=0.0.0-dev

FROM golang:1.25-alpine AS builder
ARG VERSION
# Patch Alpine base packages before building to avoid CVEs in the builder layer.
RUN apk upgrade --no-cache
WORKDIR /src

# Copy go.mod / go.sum first for a warm module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
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
