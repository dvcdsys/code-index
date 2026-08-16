# Keeping cix Up to Date

cix ships in three release streams — server, CLI, and the macOS app — and
has a built-in release-poll banner on the dashboard so you know when an
upgrade is available. This doc covers how the banner works, how to opt out,
and how to use the **develop channel** for testing unreleased changes.

## 1. Release-poll banner

The dashboard shows a banner when a newer `server/v*` release is
available on GitHub. The poll happens server-side, *not* in the
browser — one outbound request per cix-server, regardless of how many
clients have the dashboard open.

How it works:

- The server runs a goroutine
  (`server/internal/versioncheck/`, commit `853c9e4`) on a ticker.
- Every 6 hours (configurable) it calls
  `GET https://api.github.com/repos/<repo>/releases?per_page=30`.
- Releases are filtered to tags with prefix `server/v`, drafts and
  prereleases are dropped, and the highest semver tag wins.
- ETag-based revalidation means subsequent polls usually return
  `304 Not Modified` and consume almost no rate-limit budget. Default
  interval (6h) keeps the unauthenticated usage near 4 req/day per
  server — well under GitHub's anonymous 60/h ceiling.
- The cached snapshot (current version, latest tag, release URL,
  checked-at, last error) is exposed at
  `GET /api/v1/admin/version` for the dashboard.

The banner is informational only — it links to the release page on
GitHub. A Docker or from-source server does not self-update.

**The macOS app is the exception.** `cix.app` watches two streams of its
own — `server/v*` for the runtime it manages and `mac/v*` for itself — at
startup and at most every 30 minutes after, and updates both: the server by
unpacking beside the running version and moving a symlink (with automatic
rollback if the new one does not come back), the app by a detached helper
that swaps the bundle while it is closed. See
[`MACOS_APP.md`](MACOS_APP.md#check-for-updates).

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `CIX_VERSION_CHECK_ENABLED` | `true` | Master switch. Set `false` to disable all outbound HTTP for version checks. |
| `CIX_VERSION_CHECK_INTERVAL` | `6h` | Go duration string (`30m`, `12h`, …). Floored to a sensible minimum to avoid hammering GitHub. |
| `CIX_VERSION_CHECK_REPO` | `dvcdsys/code-index` | Override only if you're running a fork with its own release stream. |

Disabling the check (`CIX_VERSION_CHECK_ENABLED=false`) is the right
setting for air-gapped deployments — the dashboard hides the banner
and the server never makes the GitHub call.

A "0.0.0-dev" build (the local-make default; `server/cmd/cix-server/version.go`)
always treats the latest release as "newer", so the banner shows up
the first time you point a dev build at the dashboard. This is
deliberate — it keeps dev builds honest about how far behind stable
they are.

## 2. CLI install channels

Two channels share an `install.sh` family of scripts:

| Channel | Tag stream | Installer | Pairing |
|---|---|---|---|
| **Stable** | `cli/v*` GitHub releases | `install.sh` | Pair with a `server/v*` Docker tag or native build. |
| **Develop** | `cli/develop` floating tag | `install-develop.sh` | Pair with `dvcdsys/code-index:develop-cu128`. |

### Stable (default)

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
```

The installer resolves the highest `cli/v*` GitHub release for the
current OS/arch, downloads the tarball, and drops the `cix` binary in
`/usr/local/bin` (override with `--bin-dir`). Re-running upgrades
in place.

Stable CLI releases ship binaries for `darwin-arm64`, `darwin-amd64`,
`linux-arm64`, `linux-amd64`.

### Develop channel

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install-develop.sh | bash
```

What this gives you:

- The CLI binary built from the head of the `develop` branch. The
  `cli/develop` tag is **force-updated** on every PR merged into
  `develop` that touches `cli/**`, so re-running the installer always
  pulls the freshest build.
- The stable installer's `cli/v*` filter ignores the `cli/develop`
  tag (no `v` prefix), so the two channels do not collide.

Pair with the matching server tag (CUDA only — the develop pre-release
pipeline does not publish a CPU image):

```yaml
# docker-compose.develop.yml
services:
  cix-server:
    image: dvcdsys/code-index:develop-cu128
```

CI gate: PRs into `develop` build the develop-cu128 image and the
develop CLI release before merge
(`.github/workflows/prerelease-server.yml`,
`.github/workflows/prerelease-cli.yml`).

**When to use the develop channel.** Staging the next release together
against a real workload, reproducing a bug report from a `develop`-only
build, or testing a server-side feature that depends on an in-progress
CLI command. **Don't run this in production** — the develop pair has
no compatibility guarantees and may break across merges.

To switch back to stable, just re-run the stable installer — it
overwrites the develop binary at the same path.

## 3. Reindex after an upgrade

Upgrading the **server** can require a reindex in two cases:

1. **Embedding model changed.** If the new release changes the default
   model (or you change it yourself via Dashboard → Server →
   Embedding model), every project becomes stale. The dashboard's
   drift indicator paints affected projects red with a "Stale model"
   badge until you reindex. See README's *Drift indicator* section.
2. **Schema migration adds chunk-level data.** Releases that backfill
   new chunk metadata (e.g. the FTS5 mirror introduced by `f00e3d3`)
   may prompt the dashboard to recommend a reindex on existing
   projects. The recommendation is non-blocking — old projects keep
   working, just without the new signal — but acting on it gets you
   the full search quality.

Reindexing from the dashboard uses the project page's **Reindex**
button (`596748e`); from the CLI it's `cix reindex --full <path>`.

Upgrading the **CLI** never requires a reindex — the CLI is a thin
HTTP client.

**0.13.0 is not one of these cases.** It moves the vector store from
chromem-go to SQLite, and the server imports the vectors you already have
on its first boot — nothing is re-embedded and no reindex is needed. The
import runs before the listener binds, so that one boot takes longer than
usual (17 s on a 312k-document index). See
[`VECTORSTORE.md`](VECTORSTORE.md#migration-from-chromem-go).

## 4. Related files

- `server/internal/versioncheck/check.go` — release-poll service
- `install.sh` / `install-develop.sh` — stable + develop installers
- `.github/workflows/release-server.yml` / `release-cli.yml` / `release-mac.yml` — stable build pipelines
- [`MACOS_APP.md`](MACOS_APP.md) — the macOS app's own update mechanism
- `.github/workflows/prerelease-server.yml` / `prerelease-cli.yml` — develop build pipelines
- [`DOCKER_TAGS.md`](DOCKER_TAGS.md) — Docker tag lifecycle, including `develop-cu128`
- [`RELEASES.md`](RELEASES.md) — how to cut a stable release
