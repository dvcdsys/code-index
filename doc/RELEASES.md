# Releases

Server, CLI and the macOS app ship on three independent tag streams so a
bugfix on one doesn't drag the others through a rebuild + retest cycle.

| Component | Tag pattern | Workflow | Artifact |
|---|---|---|---|
| Server (`cix-server`) | `server/v*` (e.g. `server/v0.6.0`) | [`release-server.yml`](../.github/workflows/release-server.yml) | Docker images on Docker Hub: `:latest`, `:<version>`, `:cu128`, `:<version>-cu128` — **plus** `cix-runtime-<version>-darwin-arm64.tar.gz` on the GitHub Release |
| CLI (`cix`) | `cli/v*` (e.g. `cli/v0.6.0`) | [`release-cli.yml`](../.github/workflows/release-cli.yml) | `cix-{darwin,linux}-{amd64,arm64}.tar.gz` on a GitHub Release |
| macOS app (`cix.app`) | `mac/v*` (e.g. `mac/v0.1.1`) | [`release-mac.yml`](../.github/workflows/release-mac.yml) | `cix-<version>-arm64.dmg` + `checksums.txt` on a GitHub Release |

The app and the server it runs are deliberately not the same release. The
app is ~4 MB and holds one executable; the *runtime* it installs — the
server, the CLI and a Metal `llama-server` — ships from the `server/v*`
tag, so a Mac and a container on the same version run the same server and
a new server reaches a Mac without a new app. See
[`MACOS_APP.md`](MACOS_APP.md) and [`../mac/README.md`](../mac/README.md).

Bare `v*` tags are the historical pre-split CLI line — the installer
still falls back to them when no `cli/v*` release exists, but no new
bare `v*` tags should be created. See [`DEPRECATION_POLICY.md`](DEPRECATION_POLICY.md).

The two streams advance independently. Server and CLI must remain
contract-compatible (the CLI is a thin HTTP client), so when changing
shared shapes — endpoints, JSON payloads — update both sides in the
same PR but release them on their own tags and verify the older CLI
still speaks the newer server (and vice versa).

For testing unreleased builds together, use the **develop channel** —
see [`UPDATES.md`](UPDATES.md#cli-install-channels).

---

## Cutting a CLI release

1. Bump `cli/cmd/version.go` to `var Version = "0.7.0"` (no leading `v`).
2. Tag and push:

   ```bash
   git tag cli/v0.7.0
   git push origin cli/v0.7.0
   ```

3. CI (`release-cli.yml`) builds binaries for macOS + Linux (amd64 +
   arm64), uploads them to a GitHub Release named `cli/v0.7.0`, and
   updates the `cli/latest` floating tag. The stable installer
   auto-picks them up on the next run.

Local cross-build (no release — useful to test the archive shape
before tagging):

```bash
cd cli && make release VERSION=v0.7.0
```

Produces archives in `cli/dist/` plus `checksums.txt`. Supported
targets: `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`.

## Cutting a server release

The server release adds a pre-tag CVE scan and an image build that
takes >30 min on CI, so this is more disciplined than the CLI path:

1. **CVE scan** — run on a native amd64 builder:

   ```bash
   cd server && make scout-cuda
   ```

   Verify 0 CRITICAL / 0 HIGH. The workflow scans on `linux/amd64`
   (CUDA image is amd64-only).

2. **Bump version**: edit `server/cmd/cix-server/version.go` to
   `var version = "0.7.0"`.

3. **Tag and push**:

   ```bash
   git tag server/v0.7.0
   git push origin server/v0.7.0
   ```

4. CI (`release-server.yml`) builds CPU multi-arch + CUDA `amd64`
   images with provenance + SBOM attestations, pushes them to Docker
   Hub with both pinned (`:0.7.0`, `:0.7.0-cu128`) and floating
   (`:latest`, `:cu128`) tags, builds the macOS runtime tarball, and
   creates a GitHub Release carrying it.

5. **Promote** in production (Portainer, your compose file, etc.) by
   updating the image tag to `:0.7.0` / `:0.7.0-cu128` and
   redeploying.

CI does not deploy to production. It stops at Docker Hub push by
design — promotion is a manual operator step.

Two constraints the `macos-runtime` job adds to a server tag:

- **A `cli/v*` tag must be reachable from the tagged commit.** The runtime
  bundles the `cix` CLI and the job fails rather than ship it stamped
  `0.0.0-dev`. In practice this means cutting `server/v*` on `main`.
- **The release is not publishable without it.** `macos-runtime` is a hard
  dependency of the release job, because a server release with no runtime
  attached is one no Mac can install or update to — `cix.app` reads its
  server from exactly these assets.

## Cutting a macOS app release

Only when the *app* changes — a server release reaches Macs on its own.

1. Bump the version wherever the app advertises it, then tag:

   ```bash
   git tag mac/v0.1.2
   git push origin mac/v0.1.2
   ```

2. CI (`release-mac.yml`) builds and ad-hoc-signs `cix.app` on an arm64
   runner, verifies the bundle (`codesign --verify --strict`, a
   `cix-launcher -report`, and a check that `Contents/MacOS` holds exactly
   one executable), wraps it in the styled DMG, writes `checksums.txt`,
   and publishes a GitHub Release whose body carries the Gatekeeper
   instructions.

The release is created with `make_latest: false` on purpose: the Docker
image is this project's primary deliverable and owns the "latest" pointer.
Mac installs and the in-app updater filter releases by the `mac/` tag
prefix instead.

Unlike `server/v*`, this stream needs no other tag reachable — nothing in
the app is stamped from the server or the CLI. What is actually installed
on a Mac is recorded in `~/.cix/runtime/current/runtime.json`.

There is no Apple Developer certificate and therefore no notarization;
signing is ad-hoc, which macOS blocks on first launch by design. The
signing order and the failure modes it avoids are documented in
[`../mac/README.md`](../mac/README.md).

## Docker Scout workflow (iterate before pushing)

For non-tag iterations on the CUDA image (debugging a new layer,
testing a base-image bump):

```bash
# 1. Build on native amd64 builder → push temp tag → scan
cd server && make scout-cuda
# prints SCOUT_TAG=scout-YYYYMMDD-HHMM

# 2. If 0 HIGH/CRITICAL → promote (no rebuild, imagetools retag)
make promote-cuda SCOUT_TAG=scout-YYYYMMDD-HHMM

# 3. CPU image
make scout-cpu   # builds locally, no push
```

Key: always pass `--platform linux/amd64` to `docker scout cves` for
the CUDA image — on Apple Silicon the default platform is `arm64` and
the CUDA image is `amd64`-only. The `make scout-cuda` target handles
this.

## Server make targets (full list)

```bash
cd server
make build                  # compile cix-server binary
make bundle                 # build + fetch llama-server (macOS Metal)
make run                    # bundle + launch with .env (dev)
make test                   # go test ./...
make test-gate              # parity gate vs reference embeddings (requires GGUF)
make docker-build-cuda      # build + push CUDA image (uses cix-builder)
make docker-build-cuda-dev  # build + push :cu128-dev tag (smoke testing)
make scout-cuda             # safe pre-push CVE scan workflow
make promote-cuda SCOUT_TAG=scout-…  # retag without rebuild
```

The `cix-builder` buildx instance has two nodes — a local desktop
arm64 node and an SSH-bound `linux/amd64` node on the RTX 3090 server.
CUDA builds run natively on the amd64 node (no QEMU, full speed).

## Pre-built Docker images

See [`DOCKER_TAGS.md`](DOCKER_TAGS.md) for the full active-tag matrix
and historical lifecycle. The quick version:

| Tag | Architecture | Use case |
|-----|-------------|----------|
| `dvcdsys/code-index:latest` | linux/amd64 + linux/arm64 | CPU |
| `dvcdsys/code-index:<version>` | linux/amd64 + linux/arm64 | CPU, version-pinned |
| `dvcdsys/code-index:cu128` | linux/amd64 | NVIDIA GPU (CUDA 12.8) |
| `dvcdsys/code-index:<version>-cu128` | linux/amd64 | NVIDIA, version-pinned |
| `dvcdsys/code-index:develop-cu128` | linux/amd64 | Pre-release CUDA — pairs with the develop CLI channel |

## Related files

- `.github/workflows/release-server.yml` — stable server build/release pipeline (Docker images + the macOS runtime)
- `.github/workflows/release-cli.yml` — stable CLI build/release pipeline
- `.github/workflows/release-mac.yml` — macOS app + DMG
- `.github/workflows/prerelease-server.yml` / `prerelease-cli.yml` — develop channels
- [`MACOS_APP.md`](MACOS_APP.md) — what the app does with what this stream ships
- [`../mac/README.md`](../mac/README.md) — the app build pipeline and signing order
- [`DOCKER_TAGS.md`](DOCKER_TAGS.md) — Docker Hub tag lifecycle
- [`DEPRECATION_POLICY.md`](DEPRECATION_POLICY.md) — when tags / behaviours retire
- [`UPDATES.md`](UPDATES.md) — release-poll banner + install channels
