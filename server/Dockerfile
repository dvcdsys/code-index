# syntax=docker/dockerfile:1.7
# Phase 1 CPU-only multi-stage Dockerfile.
# Embeddings (CUDA / llama-server sidecar) land in Phase 3.

ARG VERSION=0.0.0-dev

FROM golang:1.24-alpine AS builder
ARG VERSION
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

USER nonroot:nonroot
ENTRYPOINT ["/cix-server"]
