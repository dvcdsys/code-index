# mac/ — the cix.app build pipeline

Everything needed to turn the repo into a signed, drag-to-install macOS app.
User-facing installation and usage documentation lives in
[`doc/MACOS_APP.md`](../doc/MACOS_APP.md); this file is about the build.

```
mac/
  Info.plist.in                    bundle metadata template (@PLACEHOLDER@ tokens)
  Resources/                       icons — placeholders, safe to replace
  scripts/build-app.sh             assemble + sign mac/dist/cix.app
  scripts/sign-app.sh              ad-hoc codesign, innermost code first
  scripts/make-dmg.sh              wrap the app in a drag-to-Applications DMG
  scripts/make-placeholder-icons.py regenerate the placeholder artwork
  dist/                            build output (gitignored)
```

## Build

```bash
MAC_VERSION=0.1.0-dev mac/scripts/build-app.sh
MAC_VERSION=0.1.0-dev mac/scripts/make-dmg.sh
```

`build-app.sh` reads these environment variables, all optional locally and all
set explicitly by CI:

| Variable | Meaning |
|---|---|
| `MAC_VERSION` | version of the app itself, from the `mac/vX.Y.Z` tag |
| `SERVER_VERSION` | stamped into `cix-server` (default: nearest `server/v*` tag) |
| `CLI_VERSION` | stamped into `cix` (default: nearest `cli/v*` tag) |
| `OUT_DIR` | build directory (default `mac/dist`) |
| `SKIP_SERVER_BUILD` | `1` reuses an existing `server/dist` bundle — much faster when iterating on the launcher |
| `SKIP_SIGN` | `1` skips codesign; the result **will not run** |

Release builds pass the versions in rather than deriving them, because
`git describe` is not reliable on this repo: the three tag streams interleave,
and at least one shipped server tag sits on a commit reachable from no branch.
CI additionally fails the build outright if no `server/v*` and `cli/v*` tag are
reachable from the tagged commit.

## Three tag streams

`server/v*`, `cli/v*` and `mac/v*` are released independently. The app version
describes what the app does; the versions of the two binaries it bundles are
stamped separately and recorded in `Info.plist` as `CIXServerVersion`,
`CIXCLIVersion` and `CIXLlamaVersion`.

Cut `mac/v*` tags on `main`. `git describe` walks ancestors, so a tag cut on
`develop` resolves to whatever `server/v*` happens to be reachable from there,
which has been several releases behind what actually shipped.

## Signing: ad-hoc, bottom up

There is no paid Apple Developer membership, so there is no identity to sign
with and nothing to notarize. Ad-hoc signing (`codesign --sign -`) is still
mandatory — on Apple Silicon the kernel refuses to run an unsigned executable at
all. It gets the code running; it does not satisfy Gatekeeper, hence the
first-launch instructions shipped in the DMG.

`sign-app.sh` signs dylibs first, then each executable, then the bundle. Two
things about it are not stylistic:

- **`xattr -cr` runs first, on every build.** `server/Makefile` records the
  failure this avoids: on macOS 26, amfid `SIGKILL`s an ad-hoc-signed binary
  whose linked dylibs carry a stale signature or a `com.apple.provenance`
  extended attribute — with *empty stderr*. The process simply dies. Every `cp`
  into the staging tree recreates those conditions.
- **No `--deep`.** Apple deprecated it, and it is unreliable for a bundle that
  carries four executables and ~35 dylibs directly in `Contents/MacOS` rather
  than as nested `.app`/`.framework` bundles. Explicit bottom-up ordering is
  verifiable at each step.

`--options runtime` is deliberately not used: the hardened runtime only pays off
alongside notarization, and it risks breaking `llama-server`.

## DMG

A plain UDZO image containing `cix.app`, an `/Applications` symlink and a
`READ ME FIRST.txt` with the Gatekeeper instructions. No custom window layout:
positioning icons requires a read-write image driven through Finder over
AppleScript in a real GUI session, which is flaky-to-impossible on a CI runner.
A pre-baked `.DS_Store` can be added later without changing the script.

The app is copied with `ditto`, not `cp -R`. `cp -R` drops extended attributes
and signature-relevant metadata, which turns a verified bundle into one that
fails `codesign --verify --strict` after the round trip through the image.

## Icons

`mac/Resources/` holds three committed files:

| File | Size | Notes |
|---|---|---|
| `menubar.png` | 18×18 | template image — black + alpha only |
| `menubar@2x.png` | 36×36 | template image |
| `AppIcon.icns` | — | full icns |

They are placeholders. Replacing them means dropping in new files with the same
names and sizes; no code changes anywhere.

The menu-bar images **must** be template images. macOS recolours template images
to match the menu bar (dark mode, tinting, inactive state) and reads only their
alpha channel — a coloured PNG renders as a solid smudge.

`scripts/make-placeholder-icons.py` regenerates the current placeholders. It is
stdlib-only and is never run by a build; it exists so the placeholders are
reproducible rather than mystery binaries.

## Apple Silicon only

Upstream llama.cpp publishes one macOS asset, `macos-arm64`, and
`server/scripts/fetch-llama.sh` refuses anything else. An x86_64 server binary
bundled with an arm64 `llama-server` assembles cleanly, signs cleanly, and dies
at the first embedding request — so `build-app.sh` checks `uname -m` and stops
with an explanation instead.
