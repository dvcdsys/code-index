# mac/ — the cix.app build pipeline

Everything needed to turn the repo into a signed, drag-to-install macOS app.
User-facing installation and usage documentation lives in
[`doc/MACOS_APP.md`](../doc/MACOS_APP.md); this file is about the build.

```
mac/
  Info.plist.in             bundle metadata template (placeholder tokens)
  Resources/                icon set + DMG artwork — see Resources/README.md
  scripts/common.sh         shared version derivation + signing helpers
  scripts/build-runtime.sh  package cix-server + cix + llama/ as a tarball
  scripts/build-app.sh      assemble + sign mac/dist/cix.app (launcher only)
  scripts/sign-app.sh       ad-hoc codesign
  scripts/make-dmg.sh       wrap the app in a drag-to-Applications DMG
  dist/                     build output (gitignored)
```

## Two artefacts, two release streams

The Mac gets two things, and they are released separately because they are
separate things:

| Asset | Stream | Contents | Size |
|---|---|---|---|
| `cix-<ver>-arm64.dmg` | `mac/v*` | the app — `cix-launcher` and its icons, nothing else | ~4 MB |
| `cix-runtime-<ver>-darwin-arm64.tar.gz` | `server/v*` | `cix-server`, the `cix` CLI, `llama/` | ~37 MB |

The runtime **is** the server, so it carries the server's version and ships from
the server's tag — the same `server/vX.Y.Z` and the same workflow run that
publishes the Docker images (`release-server.yml`, job `macos-runtime`). A Mac
install on 0.13.0 and a container on 0.13.0 are the same server. That is also
why llama has no version of its own here: a llama bump is a server release, as
it has always been.

The app installs the runtime into `~/.cix/runtime/<server version>/` and points a
`current` symlink at it. `cli/launcher/runtime_darwin.go` documents the design;
the short version is that a server update becomes a download and a `rename`,
with the app still running and the previous version kept for rollback.

The two update independently and neither waits for the other: a server release
reaches a Mac the day it reaches Docker Hub, and the app ships when the app
changes.

## Build

```bash
SERVER_VERSION=0.0.0-dev mac/scripts/build-runtime.sh
MAC_VERSION=0.1.0-dev    mac/scripts/build-app.sh
MAC_VERSION=0.1.0-dev    mac/scripts/make-dmg.sh
```

Both default their versions from `git describe`, so the arguments are only
needed when you care what the result is labelled.

| Variable | Used by | Meaning |
|---|---|---|
| `SERVER_VERSION` | runtime | the runtime's version (default: nearest `server/v*` tag) |
| `CLI_VERSION` | runtime | stamped into `cix` (default: nearest `cli/v*` tag) |
| `MAC_VERSION` | app | the app's version, from the `mac/vX.Y.Z` tag |
| `OUT_DIR` | both | build directory (default `mac/dist`) |
| `SKIP_SERVER_BUILD` | runtime | `1` reuses an existing `server/dist` bundle — much faster when iterating |
| `SKIP_VERIFY` | runtime | `1` skips the tarball round trip; do not use for a release |
| `SKIP_SIGN` | app | `1` skips codesign; the result **will not run** |

`make-dmg.sh` additionally takes `DMG_LAYOUT` — see "Disk image" below.

`build-runtime.sh` unpacks the tarball it has just written into a temp
directory and checks it as a stranger would: `codesign --verify --strict` on
every Mach-O, `otool -L` for llama-server's `@rpath` dependencies, and an actual
`cix-server -v`. That check is in the script rather than in a workflow step so a
local build gets it too, and because the failure it catches is silent — a
signature the kernel rejects means SIGKILL with empty stderr, indistinguishable
from a crash.

It also refuses to ship a payload that does not match its label: if the
extracted server reports a different version from `SERVER_VERSION`, the build
fails. That is usually `SKIP_SERVER_BUILD=1` against a stale `server/dist`.

## Testing a runtime install without a release

`CIX_RUNTIME_TARBALL` points the app at a local tarball instead of GitHub. It is
the only way to install a runtime for a build that has no release behind it:

```bash
CIX_RUNTIME_TARBALL="$PWD/mac/dist/cix-runtime-0.0.0-dev-darwin-arm64.tar.gz" \
  open mac/dist/cix.app
```

The install directory is named from the tarball's filename, so it lands in
`~/.cix/runtime/0.0.0-dev/`.

Update and rollback need a release to update *to*. Build a second runtime at a
higher version, serve it over HTTP with a `checksums.txt` beside it, and point
`CIX_UPDATE_BASE_URL` at a local stand-in for the GitHub API — it overrides the
base URL for **both** streams. Note that the updater filters out any version
containing `-` or `+`, so test versions have to be plain semver — `0.13.0`, not
`0.13.0-test`.

Rollback is worth exercising deliberately, and a runtime whose `cix-server` is a
signed copy of `/bin/echo` is the cheapest way to do it: it passes every static
check and exits the moment launchd runs it, which is exactly the failure the
rollback exists for.

Release builds pass the versions in rather than deriving them, because
`git describe` is not reliable on this repo: the three tag streams interleave,
and at least one shipped server tag sits on a commit reachable from no branch.
CI additionally fails the build outright if no `server/v*` and `cli/v*` tag are
reachable from the tagged commit.

## Three tag streams

`server/v*`, `cli/v*` and `mac/v*` are released independently. The app version
describes what the app does; the server it manages carries the server version.

`Info.plist` deliberately records neither the server nor the llama version. It
used to carry `CIXServerVersion`, `CIXCLIVersion` and `CIXLlamaVersion`, and
with the runtime outside the bundle those would be claims about somebody else's
files — wrong the first time either half is updated on its own. What is actually
installed lives in `~/.cix/runtime/current/runtime.json`.

`mac/v*` no longer needs a `server/v*` tag reachable, because nothing in the app
is stamped from one. `server/v*` still must be cut on `main`, and its
`macos-runtime` job needs a reachable `cli/v*` — the runtime bundles the CLI and
will not ship it stamped `0.0.0-dev`.

## Signing: ad-hoc, bottom up

There is no paid Apple Developer membership, so there is no identity to sign
with and nothing to notarize. Ad-hoc signing (`codesign --sign -`) is still
mandatory — on Apple Silicon the kernel refuses to run an unsigned executable at
all. It gets the code running; it does not satisfy Gatekeeper, hence the
first-launch instructions shipped in the DMG.

`sign-app.sh` signs the launcher and then the bundle; `build-runtime.sh` signs
the runtime's dylibs first, then its executables. Two things about both are not
stylistic:

- **`xattr -cr` runs first, on every build.** `server/Makefile` records the
  failure this avoids: on macOS 26, amfid `SIGKILL`s an ad-hoc-signed binary
  whose linked dylibs carry a stale signature or a `com.apple.provenance`
  extended attribute — with *empty stderr*. The process simply dies. Every `cp`
  into the staging tree recreates those conditions.
- **Dylibs before the executables that load them.** `llama-server` links its
  ~35 dylibs by `@rpath`; a dylib whose signature is stale relative to the
  executable loading it fails at dyld time, *after* `codesign` has reported
  success on the executable itself. This is why `--deep` is not used anywhere
  here — Apple deprecated it, and explicit bottom-up ordering is verifiable at
  each step.

`--options runtime` is deliberately not used: the hardened runtime only pays off
alongside notarization, and it risks breaking `llama-server`.

## Disk image

The image carries exactly two visible items — `cix.app` and an `/Applications`
symlink — over the designed background, with the red installer icon as the
volume icon.

A third item, a `READ ME FIRST.txt` carrying the Gatekeeper instructions, was
tried and removed. The block happens *after* the user has dragged the app to
Applications and ejected the image, so the file is on screen exactly when it is
not needed and gone when it is. Those instructions belong in the release body,
next to the download button, and in `doc/MACOS_APP.md`.

Getting the window laid out means creating a read-write image, mounting it, and
driving Finder over AppleScript — Finder stores the layout in the volume's
`.DS_Store`, which cannot be written into a compressed read-only image
afterwards. That needs a GUI session, which a CI runner may not have, and an
automation-consent prompt would *hang* the build rather than fail it. So the
step runs under a 90-second watchdog and `DMG_LAYOUT` decides what its failure
means:

| value | behaviour |
|---|---|
| `auto` (default) | try; on failure warn (a `::warning::` under Actions) and ship a valid but unstyled image |
| `require` | try; on failure abort the release |
| `off` | skip the Finder pass entirely |

Once a release has proved the runner can do it, `require` is the right setting.

Four details that cost real debugging time:

- **A volume named `cix` already mounted makes the whole step a no-op — and a
  silent one.** hdiutil then mounts the build's image at `/Volumes/cix 1`, the
  AppleScript addresses the disk *by name*, and Finder styles the other one.
  `osascript` still exits 0, so the build prints "layout applied" and ships an
  image with no `.DS_Store`: no background, no icon positions. Leaving the last
  DMG open in Finder is enough to trigger it, which makes it a near-certainty on
  a development machine and rare on CI. The script now detaches a leftover
  *disk image* of that name before creating (a real volume so named stops the
  build instead), and refuses to continue if the mount point still comes back
  suffixed.
- **The volume icon must be installed after the Finder pass**, not staged up
  front. Staging it looks like it works — `hdiutil` copies the file, `SetFile`
  sets the flag — and then Finder removes both while laying out the window, and
  the image ships with a generic disk icon.
- **Finder's item `position` is the top-left of the cell**, not its centre, and
  a cell is the icon size plus its label (~124 px at icon size 104).
- **Positions below y≈68 are not clamped, they translate everything.** Asking
  for y=22 moved *every* item down by 46 px, sliding the app and the
  Applications symlink off the arrow they are aligned with. Both of these only
  matter if someone adds an item — worth knowing before trying.

The volume name is a constant `cix` with no version in it: the layout is stored
per volume and the AppleScript addresses the disk by name, so a version-stamped
volume would make both version-specific for nothing. The version is in the
filename.

The app is copied with `ditto`, not `cp -R`. `cp -R` drops extended attributes
and signature-relevant metadata, which turns a verified bundle into one that
fails `codesign --verify --strict` after the round trip through the image.

## Icons

Real artwork, not placeholders — see [`Resources/README.md`](Resources/README.md)
for the full set, the brand palette and the rules of use. In short:

- `Resources/cix.iconset/` → `iconutil` → `Contents/Resources/cix.icns` (app)
- `Resources/cix-installer.iconset/` → `iconutil` → the DMG volume icon
- `Resources/menubar/cixTemplate-{18,36}.png` → `Contents/Resources/cixTemplate{,@2x}.png`
- `Resources/dmg/dmg-background{,@2x}.png` → the disk image window background

The `.icns` files are built rather than committed, so the PNG iconsets stay the
single source of truth. Replacing the artwork means dropping in files with the
same names — no code changes.

Cream is the app, red is the installer; that inversion is the only thing telling
the two apart in a Downloads folder. The menu-bar glyphs must stay template
images (pure black plus alpha): macOS recolours them for dark mode and for the
pressed state and reads nothing but the alpha channel, so a coloured PNG renders
as a smudge.

## Apple Silicon only

Upstream llama.cpp publishes one macOS asset, `macos-arm64`, and
`server/scripts/fetch-llama.sh` refuses anything else. An x86_64 server binary
paired with an arm64 `llama-server` assembles cleanly, signs cleanly, and dies
at the first embedding request — so both build scripts check `uname -m` and stop
with an explanation instead.

## A server-side bug the split surfaced

`cix-server` persists its embedding provider config to the database on first
boot and treats the stored blob as authoritative from then on. Two of its fields
are derived from the process rather than chosen by anyone: `bin_dir`, which is
`filepath.Dir(os.Executable())/llama`, and `socket_path`, which is
`<TMPDIR>/cix-llama-<pid>.sock`.

Frozen at first boot, `bin_dir` means every later boot launches the
`llama-server` of whichever install wrote the row — so the app would keep
running the *previous* runtime's sidecar after an update, and stop working
entirely once that version was pruned. A frozen `socket_path` defeats the
uniqueness it exists for: a new server can find an orphaned `llama-server`
already bound to that name and talk to it instead of spawning its own.

`embeddings.RefreshOllamaSidecarPaths` re-derives both at boot
(`server/cmd/cix-server/main.go`). Everything a person can choose — the model,
the tuning — still comes from the database untouched. This is not macOS-specific:
the same thing happens to a container whose image layout changes.
