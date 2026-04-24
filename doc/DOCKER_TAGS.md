# Docker Hub Tag Strategy — dvcdsys/code-index

## Active Tags

| Tag | Architecture | Base | Size | Notes |
|---|---|---|---|---|
| `latest` | linux/amd64 + linux/arm64 | Go CPU (distroless/static) | ~100 MB | Use with `CIX_EMBEDDINGS_ENABLED=false` |
| `cu128` | linux/amd64 | distroless/cc-debian13 + CUDA libs | ~1.0 GB | RTX 3090 prod; embeddings via llama-server |
| `go-cu128` | linux/amd64 | same as cu128 | ~1.0 GB | Dev alias — retire after v0.3.0 ships |
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

## v0.3.x — distroless CUDA runtime (2026-04-24)

The CUDA image (`:cu128` / `:go-cu128`) now uses
`gcr.io/distroless/cc-debian13:nonroot` (Debian 13 trixie, glibc 2.41,
gcc 14 libstdc++) as the runtime base instead of
`nvidia/cuda:12.8.1-base-ubuntu24.04`. CUDA shared libraries
(`libcudart`, `libcublas`, `libcublasLt`, `libnccl`, `libgomp`) are
extracted from an intermediate `nvidia/cuda` stage and COPYed into
distroless — no Ubuntu OS layer, apt, dpkg, tar, util-linux, shadow, or
libgcrypt in the final image.

**Runtime user — preserved at uid/gid 1001:**
The new image keeps numeric uid/gid 1001 (matching the prior Ubuntu
`cix:cix` user) instead of switching to distroless's default `nonroot`
(65532). This avoids any volume migration on existing deployments.
Distroless has no `/etc/passwd` entry for 1001, but Linux uses the
numeric uid for all permission checks and Go binaries do not call
`getpwuid()`.

**CVE delta** (Docker Scout, 2026-04-24, vs previous `:go-cu128` digest
`03e6970e5de6`):
- Before: 0C / 4H / 12M / 3L (19 total) across 8 packages
- After: target 0C / 0H / ≤3M / 0L — Group A (Go stdlib, 9 CVEs) cleared
  by Go 1.25.9; Group B (chi 5.1.0, 1 CVE) cleared by chi 5.2.2; Group C
  (Ubuntu base, 9 CVEs) reduced to glibc residuals only — `tar`, `dpkg`,
  `util-linux`, `shadow`, `libgcrypt20` are no longer in the image.

**Size delta:** 1.1 GB Scout-reported → 1.0 GB Scout-reported
(1.55 GB → 1.29 GB on-disk). libcublasLt alone is ~750 MB and
libcublas ~110 MB; CUDA libs are the floor for any GPU-capable image.

**Symlink preservation note:** the Dockerfile stages CUDA libs into
`/opt/cuda-runtime/` in the cuda-libs intermediate stage using `cp -d`,
then a single `COPY --from=cuda-libs /opt/cuda-runtime/ /` puts them in
the final image. Without this, BuildKit dereferences each glob entry
into a regular file, doubling disk usage on `libcublas*.so.*`.

**Why Debian 13 (trixie), not Debian 12:** llama.cpp's CUDA build (Ubuntu
24.04 noble) links against GLIBC_2.38 and GLIBCXX_3.4.32. Debian 12
bookworm ships glibc 2.36 / gcc 12 — too old; the container starts but
llama-server fails to load with "GLIBC_2.38 not found" / "GLIBCXX_3.4.32
not found". Debian 13 trixie ships glibc 2.41 / gcc 14 and runs cleanly.
