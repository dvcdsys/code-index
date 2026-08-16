# Docker Hub Tag Strategy — dvcdsys/code-index

## Active Tags

| Tag | Architecture | Base | Size | Notes |
|---|---|---|---|---|
| `latest` | linux/amd64 + linux/arm64 | distroless/cc-debian13 + bundled CPU llama.cpp | ~80 MB | Embeddings work out of the box on CPU; `CIX_EMBEDDINGS_ENABLED=false` only if you want none. |
| `<version>` (e.g. `0.6.0`) | linux/amd64 + linux/arm64 | same as `latest` | ~80 MB | Version-pinned CPU image. Immutable. |
| `cu128` | linux/amd64 | distroless/cc-debian13 + CUDA libs | ~1.1 GB | RTX 3090 prod; embeddings via llama-server |
| `<version>-cu128` (e.g. `0.6.0-cu128`) | linux/amd64 | same as `cu128` | ~1.1 GB | Version-pinned CUDA image. Immutable. |
| `develop-cu128` | linux/amd64 | same as `cu128` | ~1.1 GB | Floating pre-release; force-updated on every merge to `develop` that touches `server/`. Not for production. |
| `0.2-python-legacy` | linux/amd64 | Python FastAPI | ~5 GB | Frozen; rollback only |

Sizes are the compressed pull as Docker Hub reports it for `linux/amd64`
(v0.13.0: 81 MB CPU, 1077 MB CUDA; the arm64 CPU image is 73 MB).

## Develop channels

`develop` has a matched pair of floating pre-release artifacts:

- **Server:** Docker tag `dvcdsys/code-index:develop-cu128` (CUDA only;
  CPU image is published only on `server/v*` releases). Workflow:
  `.github/workflows/prerelease-server.yml`.
- **CLI:** GitHub release `cli/develop` (no `v` prefix, so the stable
  installer's `^cli/v` filter ignores it). Installed via
  `install-develop.sh`. Workflow:
  `.github/workflows/prerelease-cli.yml`.

Both are intended for staging the next release together against the
RTX 3090 box without cutting a real tag. See [`UPDATES.md`](UPDATES.md#cli-install-channels)
for the develop-channel workflow.

## Retired Tags (kept for historical reference)

| Tag | Retired | Reason |
|---|---|---|
| `latest-cu130` | 2026-04-24 | Replaced by `cu128` (3-stage build, -55% size) |
| `go-cu126` | 2026-04-24 | Replaced by `go-cu128` (CUDA 12.8) |
| `go-cu128` | 2026-05-XX (server/v0.4.0+) | Migration-era dev alias; superseded by `cu128`. Last digest still resolvable on Docker Hub. |

## Tag Policy

- Versioned tags (`<version>`, `<version>-cu128`) are immutable once
  published.
- Stable aliases (`latest`, `cu128`) are updated on each `server/v*`
  release.
- `develop-cu128` is force-updated on every merge to `develop` that
  touches `server/`. Not for production.
- `:0.2-python-legacy` is preserved on Docker Hub indefinitely per
  deprecation policy.

## Versioned tag pattern

`:<major>.<minor>.<patch>` (CPU) and `:<major>.<minor>.<patch>-cu128`
(CUDA). No leading `v` on Docker tags (the leading `v` belongs to git
tags only — `server/v0.6.0` → Docker `:0.6.0` + `:0.6.0-cu128`).

See `doc/DEPRECATION_POLICY.md` for the full lifecycle policy.

## v0.3.x — distroless CUDA runtime (2026-04-24)

The CUDA image (`:cu128`) was migrated to
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

**CVE delta** (Docker Scout, 2026-04-24, vs previous Ubuntu-based CUDA digest
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
