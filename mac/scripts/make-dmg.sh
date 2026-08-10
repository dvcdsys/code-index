#!/usr/bin/env bash
# make-dmg.sh — wrap a built cix.app in a drag-to-Applications disk image.
#
# Usage:  mac/scripts/make-dmg.sh [path/to/cix.app]
#
# Environment:
#   MAC_VERSION   version string for the volume name and filename (default: dev)
#   OUT_DIR       output directory (default: mac/dist)
#
# No custom window layout. Positioning icons in a DMG means creating a
# read-write image, mounting it, driving Finder over AppleScript to set the
# background and icon coordinates, then converting to compressed read-only.
# That needs a real GUI session; on a CI runner it is flaky at best. A plain
# UDZO image with the app and an /Applications symlink conveys the same
# instruction and always builds. A pre-baked .DS_Store can be added later
# without touching this script.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$REPO_ROOT/mac/dist}"
APP="${1:-$OUT_DIR/cix.app}"
MAC_VERSION="${MAC_VERSION:-dev}"
DMG="$OUT_DIR/cix-$MAC_VERSION-arm64.dmg"

if [[ ! -d "$APP" ]]; then
    echo "make-dmg: no such bundle: $APP" >&2
    exit 1
fi

STAGE="$(mktemp -d -t cix-dmg-XXXXXX)"
trap 'rm -rf "$STAGE"' EXIT

# ditto, not cp -R: it preserves extended attributes and, critically, the
# signature-relevant metadata. cp -R silently drops some of it, which turns a
# verified bundle into one that fails `codesign --verify --strict` after the
# round trip through the image.
ditto "$APP" "$STAGE/cix.app"
ln -s /Applications "$STAGE/Applications"

# Gatekeeper instructions have to travel with the download. Without a Developer
# ID there is no way to make a first launch quiet, and on macOS 15+ the old
# right-click → Open escape hatch no longer works — the user has to go through
# System Settings. Someone who does not know that concludes the app is broken.
cat > "$STAGE/READ ME FIRST.txt" <<EOF
cix $MAC_VERSION — macOS (Apple Silicon)

INSTALL
  1. Drag cix.app onto the Applications folder in this window.
  2. Open it from Applications, NOT from this disk image.

FIRST LAUNCH
  macOS will refuse to open the app the first time, saying it cannot be
  verified. This is expected: cix is open source and is not signed with a
  paid Apple Developer certificate.

  To allow it:
    - Open System Settings > Privacy & Security
    - Scroll to Security. There will be a message about cix being blocked.
    - Click "Open Anyway", then confirm.

  You only have to do this once per installed version.

  (On macOS 15 and later, right-clicking the app and choosing Open no longer
  works as a shortcut for this — use System Settings.)

WHAT IS INSIDE
  cix.app/Contents/MacOS/
    cix-launcher   the app itself
    cix-server     the indexing + search server
    cix            the command-line client
    llama/         a Metal-accelerated llama-server for local embeddings

  The app runs entirely on your machine. Nothing is uploaded anywhere unless
  you configure an external embedding provider yourself.

DOCS
  https://github.com/dvcdsys/code-index/blob/main/doc/MACOS_APP.md
EOF

echo "make-dmg: creating $DMG"
rm -f "$DMG"
hdiutil create \
    -volname "cix $MAC_VERSION" \
    -srcfolder "$STAGE" \
    -fs HFS+ \
    -format UDZO \
    -imagekey zlib-level=9 \
    -quiet \
    "$DMG"

echo "make-dmg: verifying image"
hdiutil verify "$DMG"

# Ad-hoc sign the image too. It buys no Gatekeeper trust, but it makes
# tampering after publication detectable with codesign rather than only by
# checksum, and it costs one command.
codesign --force --sign - "$DMG"

echo "make-dmg: ok — $DMG"
shasum -a 256 "$DMG"
