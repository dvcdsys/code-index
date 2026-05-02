// Package openapi contains the generated server types and chi-compatible
// ServerInterface for the cix-server HTTP API. The single source of truth is
// doc/openapi.yaml at the repo root; this directory holds nothing but the
// generator config (oapi.yaml) and the generated output (openapi.gen.go).
//
// Regenerate with `make openapi-gen` (from server/) or `go generate ./...`.
package openapi

//go:generate go tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=oapi.yaml ../../../../doc/openapi.yaml
