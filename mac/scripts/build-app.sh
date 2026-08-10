#!/usr/bin/env bash
# build-app.sh — assemble mac/dist/cix.app.
#
# Usage:  mac/scripts/build-app.sh
#
# Environment (all optional; CI sets the version explicitly):
#   MAC_VERSION  version of the .app itself, from the mac/vX.Y.Z tag
#   OUT_DIR      build directory  (default: mac/dist)
#   SKIP_SIGN    "1" skips codesign (leaves an unrunnable bundle; debug only)
#
# The .app contains the launcher and nothing else that runs. cix-server, the cix
# CLI and llama-server are built and packaged by build-runtime.sh, published
# alongside this bundle under the same tag, and installed into ~/.cix/runtime/
# by the app itself — see cli/launcher/runtime_darwin.go for why.
#
# One consequence worth stating: this bundle carries no server version, so
# Info.plist records none. The truth about what is installed lives in the
# runtime's own runtime.json, which can change without this app changing.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=mac/scripts/common.sh
source "$REPO_ROOT/mac/scripts/common.sh"
cd "$REPO_ROOT"

# The launcher itself is architecture-agnostic Go, but the app it manages is
# Apple Silicon only, so a bundle built anywhere else could never do its job.
require_apple_silicon "build-app"

OUT_DIR="${OUT_DIR:-$REPO_ROOT/mac/dist}"
APP="$OUT_DIR/cix.app"
MAC_VERSION="${MAC_VERSION:-dev}"

echo "build-app: app=$MAC_VERSION"

# --- 1. launcher ------------------------------------------------------------
# -w without -s: govulncheck -mode=binary needs the Go symbol table. Same
# reasoning, and same flags, as .github/workflows/release-cli.yml.
echo "build-app: building cix-launcher"
mkdir -p "$OUT_DIR/stage"
(cd cli && go build \
    -trimpath \
    -ldflags "-w -X 'main.version=${MAC_VERSION}'" \
    -o "$OUT_DIR/stage/cix-launcher" ./launcher)

# --- 2. assemble ------------------------------------------------------------
# Rebuild the bundle from scratch. `cp` merges into an existing tree, so an
# incremental assembly accumulates artefacts from every previous build — which
# now includes the server, CLI and llama/ this bundle no longer ships.
echo "build-app: assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp "$OUT_DIR/stage/cix-launcher" "$APP/Contents/MacOS/cix-launcher"
rm -rf "$OUT_DIR/stage"

# The app icon is built from the iconset rather than committed as a binary, so
# the PNGs stay the single source of truth. CFBundleIconFile in Info.plist.in
# names this file without its extension, as macOS expects.
iconutil -c icns mac/Resources/cix.iconset -o "$APP/Contents/Resources/cix.icns"

# Menu-bar glyphs: 1x and 2x of the 18 px status item. These must stay template
# images — pure black plus alpha — because macOS recolours them for dark mode
# and for the pressed state and ignores everything but the alpha channel.
cp mac/Resources/menubar/cixTemplate-18.png "$APP/Contents/Resources/cixTemplate.png"
cp mac/Resources/menubar/cixTemplate-36.png "$APP/Contents/Resources/cixTemplate@2x.png"

sed \
    -e "s|@SHORT_VERSION@|${MAC_VERSION}|g" \
    -e "s|@BUNDLE_VERSION@|${MAC_VERSION}|g" \
    mac/Info.plist.in > "$APP/Contents/Info.plist"

# Catch an unsubstituted token before it ships as a literal @TOKEN@ version.
if grep -q '@[A-Z_]*@' "$APP/Contents/Info.plist"; then
    echo "build-app: unsubstituted token left in Info.plist:" >&2
    grep -n '@[A-Z_]*@' "$APP/Contents/Info.plist" >&2
    exit 1
fi
plutil -lint "$APP/Contents/Info.plist"

# Classic-era metadata. LaunchServices no longer requires PkgInfo, but codesign
# and some archive tooling still expect it beside Info.plist, and it costs 8 bytes.
printf 'APPL????' > "$APP/Contents/PkgInfo"

# --- 3. sign ----------------------------------------------------------------
if [[ "${SKIP_SIGN:-0}" == "1" ]]; then
    echo "build-app: SKIP_SIGN=1 — bundle is UNSIGNED and will be killed on launch"
else
    mac/scripts/sign-app.sh "$APP"
fi

echo "build-app: ready — $APP"
du -sh "$APP" | sed 's/^/build-app: size /'
