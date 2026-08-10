# mac/ — the cix.app build pipeline

Everything needed to turn the repo into a signed, drag-to-install macOS app.
User-facing installation and usage documentation lives in
[`doc/MACOS_APP.md`](../doc/MACOS_APP.md); this file is about the build.

```
mac/
  Info.plist.in          bundle metadata template (placeholder tokens)
  Resources/             icon set + DMG artwork — see Resources/README.md
  scripts/build-app.sh   assemble + sign mac/dist/cix.app
  scripts/sign-app.sh    ad-hoc codesign, innermost code first
  scripts/make-dmg.sh    wrap the app in a drag-to-Applications DMG
  dist/                  build output (gitignored)
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

`make-dmg.sh` additionally takes `DMG_LAYOUT` — see "Disk image" below.

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

Three details that cost real debugging time:

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
bundled with an arm64 `llama-server` assembles cleanly, signs cleanly, and dies
at the first embedding request — so `build-app.sh` checks `uname -m` and stops
with an explanation instead.
