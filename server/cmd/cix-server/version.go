package main

// version is set at build time via -ldflags "-X main.version=...". Default
// placeholder makes bare `go run` still produce a meaningful status response.
var version = "0.0.0-dev"

// apiVersion mirrors api/app/version.py. Bumped independently from server
// version when the HTTP contract changes.
const apiVersion = "v1"

const backend = "go"
