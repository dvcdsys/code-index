# Deprecation Policy

## Server (Go binary / Docker images)

- **One minor version notice** before removal. If a feature or API endpoint is
  deprecated in `server/v0.X.0`, it will be removed in `server/v0.(X+1).0`.
- **Breaking API changes** bump the major version (e.g., `server/v1.0.0`).
- The current API version is `v1`; all `/api/v1/*` endpoints are stable.

## Docker tags

- Stable alias tags (`latest`, `cu128`) are updated on each `server/v*` release.
- Versioned tags (`v0.3.0`, `v0.3.0-cu128`) are immutable once published.
- Dev alias tags (`go-cu128`) are retired 30 days after the corresponding stable
  alias is published.
- Legacy tags (`0.2-python-legacy`) are preserved on Docker Hub indefinitely.

See `doc/DOCKER_TAGS.md` for the current tag inventory.

## Python backend

The Python FastAPI backend (`legacy/python-api/`) was deprecated in
`server/v0.3.0` (2026-04-24) and removed from the repository in
`server/v0.4.0` (2026-04-28).

The Docker image `dvcdsys/code-index:0.2-python-legacy` is preserved on
Docker Hub indefinitely as a rollback option.

See `doc/MIGRATION_FROM_PYTHON.md` for migration instructions and the
rollback recipe.
