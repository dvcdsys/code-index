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

## chromem-go vector store and `CIX_CHROMA_PERSIST_DIR`

Deprecated in `server/v0.13.0`, which replaced the chromem-go store with a
SQLite one (`doc/VECTORSTORE.md`). Nothing writes to the chromem tree any
more:

- `CIX_CHROMA_PERSIST_DIR` is still read, for exactly two things — locating
  the legacy gob files for the one-time import, and deriving the default
  `CIX_VECTORS_DIR` beside them. Set `CIX_VECTORS_DIR` explicitly and it
  stops mattering.
- The `<data>/chroma` tree is kept deliberately: it is what a downgrade to a
  pre-0.13 server would read. **Server → Resources → Clean** offers it as the
  `legacy_chromem` category, which is the supported way to reclaim it once
  you have decided you will not roll back.

Neither is scheduled for removal yet. When one is, the notice lands here one
minor version ahead, per the rule above.

## Python backend

The Python FastAPI backend (`legacy/python-api/`) was deprecated in
`server/v0.3.0` (2026-04-24) and removed from the repository in
`server/v0.4.0` (2026-04-28).

The Docker image `dvcdsys/code-index:0.2-python-legacy` is preserved on
Docker Hub indefinitely as a rollback option.

See `doc/MIGRATION_FROM_PYTHON.md` for migration instructions and the
rollback recipe.
