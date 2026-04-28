# Migration from Python to Go server

The Go server (`server/`) replaces the Python FastAPI backend as of `server/v0.3.0`.
The CLI (`cix`) and HTTP API contract are unchanged — no CLI updates required.

## Which image to pull

| Deployment | Image tag |
|---|---|
| CPU only | `dvcdsys/code-index:latest` |
| NVIDIA GPU (recommended) | `dvcdsys/code-index:cu128` |

## Environment variable changes

All variables are now prefixed with `CIX_`:

| Python (old) | Go (new) | Notes |
|---|---|---|
| `API_KEY` | `CIX_API_KEY` | value unchanged |
| `EMBEDDING_MODEL` | `CIX_EMBEDDING_MODEL` | value unchanged |
| `CHROMA_PERSIST_DIR` | `CIX_CHROMA_PERSIST_DIR` | path unchanged; see vector store note below |
| `SQLITE_PATH` | `CIX_SQLITE_PATH` | schema compatible, no migration needed |
| `MAX_FILE_SIZE` | `CIX_MAX_FILE_SIZE` | value unchanged |
| `EXCLUDED_DIRS` | `CIX_EXCLUDED_DIRS` | value unchanged |
| `N_GPU_LAYERS` | `CIX_N_GPU_LAYERS` | value unchanged |
| *(new)* | `CIX_GGUF_CACHE_DIR` | GGUF cache; default `/data/models` |
| *(new)* | `CIX_LLAMA_BIN_DIR` | path to llama-server; default `/app` in container |
| *(new)* | `CIX_LLAMA_STARTUP_TIMEOUT` | seconds; default 60 |
| *(new)* | `CIX_EMBEDDINGS_ENABLED` | disable embeddings for CPU-only mode; default `true` |

See `.env.example` for a complete template.

## Vector store (action required)

The Python server used ChromaDB (DuckDB + parquet).
The Go server uses chromem-go (JSON format). **These are not compatible.**

On first boot the Go server automatically detects the old ChromaDB layout
(`chroma.sqlite3` in the persist dir) and backs it up:

```
/data/chroma.python-backup.20260424-120000/
```

After that, re-run `cix init` for each project to rebuild the index:

```bash
cix init /path/to/your/project
```

Typical reindex time: under 2 minutes per 10k-file project.

## SQLite

The schema is fully compatible — no migration needed.

## Rollback

If you need to go back to the Python server:

```bash
# In Portainer: change image to dvcdsys/code-index:0.2-python-legacy
# The chroma backup is preserved at /data/chroma.python-backup.*
# Rename it back to /data/chroma to restore the old index.
```

## Sunset timeline

The Python code in `legacy/python-api/` was deleted in `server/v0.4.0`
(2026-04-28). This document is retained for historical reference and as
the rollback recipe for the preserved `:0.2-python-legacy` Docker tag,
which stays on Docker Hub indefinitely.
