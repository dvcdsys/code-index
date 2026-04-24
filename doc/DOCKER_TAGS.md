# Docker Hub Tag Strategy — dvcdsys/code-index

## Active Tags

| Tag | Architecture | Base | Size | Notes |
|---|---|---|---|---|
| `latest` | linux/amd64 + linux/arm64 | Go CPU | ~100 MB | Use with `CIX_EMBEDDINGS_ENABLED=false` |
| `cu128` | linux/amd64 | nvidia/cuda:12.8.1-base | ~1.66 GB | RTX 3090 prod; embeddings via llama-server |
| `go-cu128` | linux/amd64 | same as cu128 | ~1.66 GB | Dev alias — retire after v0.3.0 ships |
| `0.2-python-legacy` | linux/amd64 | Python FastAPI | ~5 GB | Frozen; rollback only |

## Retired Tags (kept for historical reference)

| Tag | Retired | Reason |
|---|---|---|
| `latest-cu130` | 2026-04-24 | Replaced by cu128 (3-stage build, -55% size) |
| `go-cu126` | 2026-04-24 | Replaced by go-cu128 (CUDA 12.8) |

## Tag Policy

- Tags are immutable once documented here.
- Stable aliases (`latest`, `cu128`) are updated on each server/v* release.
- Dev aliases (`go-cu128`) are removed 30 days after the stable alias is published.
- `:0.2-python-legacy` is preserved on Docker Hub indefinitely per deprecation policy.

## Versioned Tags (post v0.3.0)

Pattern: `:v<major>.<minor>.<patch>` (CPU) and `:v<major>.<minor>.<patch>-cu128` (CUDA).

See `doc/DEPRECATION_POLICY.md` for the full lifecycle policy.
