#!/usr/bin/env bash
# make-dmg.sh — wrap a built cix.app in a drag-to-Applications disk image.
#
# Usage:  mac/scripts/make-dmg.sh [path/to/cix.app]
#
# Environment:
#   MAC_VERSION   version string for the DMG filename (default: dev)
#   OUT_DIR       output directory (default: mac/dist)
#   DMG_LAYOUT    auto (default) | require | off — see "Window layout" below
#
# The image carries three things beyond the app itself: the installer icon as
# the volume icon, the designed window background, and a Finder layout that
# puts cix.app and the Applications symlink where the background's arrow points.
#
# Two visible items, and deliberately no third. A "READ ME FIRST.txt" with the
# Gatekeeper instructions was tried and removed: the block happens after the
# user has dragged the app to Applications and ejected the image, so the file is
# on screen exactly when it is not needed and gone when it is. Those
# instructions belong where people actually are at that moment — the release
# body next to the download button, and doc/MACOS_APP.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$REPO_ROOT/mac/dist}"
APP="${1:-$OUT_DIR/cix.app}"
MAC_VERSION="${MAC_VERSION:-dev}"
DMG="$OUT_DIR/cix-$MAC_VERSION-arm64.dmg"
DMG_LAYOUT="${DMG_LAYOUT:-auto}"

# The volume name is deliberately constant, with no version in it. Finder stores
# the window layout in a .DS_Store keyed to this volume, and the AppleScript
# below has to address `disk "cix"` by name — a version-stamped volume would
# make both version-specific for no benefit. The version lives in the filename,
# where people actually look for it.
VOLNAME="cix"

# Layout from mac/Resources/README.md — these coordinates are where the arrow in
# the background image points. Changing one without the other breaks the design.
WIN_W=640
WIN_H=420
ICON_SIZE=104
APP_X=175
APP_Y=250
LINK_X=465
LINK_Y=250
# Before adding an item here, two Finder behaviours, both measured rather than
# documented: `position` is the TOP-LEFT of the item cell (icon size plus label,
# ~124 px), not its centre; and a y below roughly 68 is not clamped for that one
# item — Finder translates EVERY item down by the difference, sliding the app
# and the symlink off the arrow they are aligned with.
#
# Finder's window `bounds` are {left, top, right, bottom} on screen, so the
# right/bottom edges are origin+size — not the size itself. Getting this wrong
# yields a window smaller than the background, which Finder then crops.
WIN_LEFT=160
WIN_TOP=120
WIN_RIGHT=$((WIN_LEFT + WIN_W))
WIN_BOTTOM=$((WIN_TOP + WIN_H))

if [[ ! -d "$APP" ]]; then
    echo "make-dmg: no such bundle: $APP" >&2
    exit 1
fi
case "$DMG_LAYOUT" in
    auto|require|off) ;;
    *) echo "make-dmg: DMG_LAYOUT must be auto, require or off (got '$DMG_LAYOUT')" >&2; exit 2 ;;
esac

STAGE="$(mktemp -d -t cix-dmg-XXXXXX)"
RW_DMG="$(mktemp -u -t cix-dmg-rw-XXXXXX).dmg"
ATTACHED_DEV=""
cleanup() {
    [[ -n "$ATTACHED_DEV" ]] && hdiutil detach "$ATTACHED_DEV" -force -quiet 2>/dev/null || true
    rm -rf "$STAGE"
    rm -f "$RW_DMG"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Stage the volume contents
# ---------------------------------------------------------------------------

# ditto, not cp -R: it preserves extended attributes and signature-relevant
# metadata. cp -R silently drops some of it, which turns a verified bundle into
# one that fails `codesign --verify --strict` after the round trip.
ditto "$APP" "$STAGE/cix.app"
ln -s /Applications "$STAGE/Applications"

mkdir -p "$STAGE/.background"
cp "$REPO_ROOT/mac/Resources/dmg/dmg-background.png" "$STAGE/.background/dmg-background.png"
cp "$REPO_ROOT/mac/Resources/dmg/dmg-background@2x.png" "$STAGE/.background/dmg-background@2x.png"

# The volume icon is installed later, after Finder has finished with the volume
# — see the note above the "Volume icon" step below.


# ---------------------------------------------------------------------------
# Build a read-write image, dress it, then compress
# ---------------------------------------------------------------------------
# The layout has to be applied to a mounted, writable volume: Finder stores it
# in the volume's .DS_Store, which cannot be written into a compressed
# read-only image after the fact.
echo "make-dmg: creating writable image"
hdiutil create \
    -volname "$VOLNAME" \
    -srcfolder "$STAGE" \
    -fs HFS+ \
    -format UDRW \
    -quiet \
    "$RW_DMG"

echo "make-dmg: mounting"
# A volume called cix already mounted — a previous build, or simply the last
# DMG still open in Finder — makes hdiutil mount this one at "/Volumes/cix 1".
# The Finder layout below addresses the disk BY NAME, and with two volumes of
# that name it styles the wrong one. Silently: osascript still exits 0, so the
# build reports "layout applied" and ships an image with no .DS_Store, hence no
# background and no icon positions. Bisected: same script and inputs, occupied
# /Volumes/cix produces no .DS_Store, clean /Volumes produces one.
#
# Only a disk image is detached here. A real volume that happens to be called
# cix is somebody's disk, and ejecting it to build a DMG would be outrageous —
# so that case stops the build instead.
stale_mount="/Volumes/$VOLNAME"
if [[ -d "$stale_mount" ]]; then
    if hdiutil info | sed -n 's|.*\(/Volumes/.*\)$|\1|p' | grep -qxF "$stale_mount"; then
        echo "make-dmg: detaching a leftover disk image at $stale_mount"
        hdiutil detach "$stale_mount" -force -quiet || true
    else
        echo "make-dmg: $stale_mount exists and is not a disk image — refusing to touch it." >&2
        echo "make-dmg: eject or rename that volume and run again." >&2
        exit 1
    fi
fi

ATTACH_OUT="$(hdiutil attach -readwrite -noverify -noautoopen "$RW_DMG")"
ATTACHED_DEV="$(printf '%s\n' "$ATTACH_OUT" | awk '/^\/dev\// { print $1; exit }')"
MOUNT_POINT="$(printf '%s\n' "$ATTACH_OUT" | sed -n 's|.*\(/Volumes/.*\)$|\1|p' | tail -1)"
if [[ -z "$ATTACHED_DEV" || -z "$MOUNT_POINT" ]]; then
    echo "make-dmg: could not determine the mount point:" >&2
    printf '%s\n' "$ATTACH_OUT" >&2
    exit 1
fi
echo "make-dmg: mounted $ATTACHED_DEV at $MOUNT_POINT"

# Belt and braces to the detach above. If the image still landed on a suffixed
# path, another volume of this name appeared between then and now, and the
# Finder step would style it instead of this one — producing an unstyled image
# and reporting success. Stop rather than ship that.
if [[ "$MOUNT_POINT" != "/Volumes/$VOLNAME" ]]; then
    echo "make-dmg: mounted at $MOUNT_POINT, expected /Volumes/$VOLNAME." >&2
    echo "make-dmg: another volume named $VOLNAME is in the way; the window layout would be applied to it." >&2
    exit 1
fi

# --- Window layout ----------------------------------------------------------
# This is the one step that needs Finder, and Finder needs a real GUI session.
# On a CI runner that may be unavailable, or blocked by an automation-consent
# prompt that nobody is there to answer — which would hang the build rather
# than fail it. So: run it under a watchdog, and treat the outcome according to
# DMG_LAYOUT.
#
#   auto     try; on failure warn and ship an unstyled but valid DMG (default)
#   require  try; on failure abort the build
#   off      skip entirely
layout_applied=0
if [[ "$DMG_LAYOUT" == "off" ]]; then
    echo "make-dmg: DMG_LAYOUT=off — skipping Finder layout"
else
    echo "make-dmg: applying Finder layout"
    read -r -d '' LAYOUT_SCRIPT <<APPLESCRIPT || true
tell application "Finder"
    tell disk "$VOLNAME"
        open
        set current view of container window to icon view
        set toolbar visible of container window to false
        set statusbar visible of container window to false
        set the bounds of container window to {${WIN_LEFT}, ${WIN_TOP}, ${WIN_RIGHT}, ${WIN_BOTTOM}}
        set opts to the icon view options of container window
        set arrangement of opts to not arranged
        set icon size of opts to $ICON_SIZE
        set background picture of opts to file ".background:dmg-background.png"
        set position of item "cix.app" of container window to {$APP_X, $APP_Y}
        set position of item "Applications" of container window to {$LINK_X, $LINK_Y}
        close
        open
        update without registering applications
        delay 2
        close
    end tell
end tell
APPLESCRIPT

    set +e
    osascript -e "$LAYOUT_SCRIPT" >/tmp/cix-dmg-layout.log 2>&1 &
    osa_pid=$!
    ( sleep 90; kill -9 "$osa_pid" 2>/dev/null ) &
    watchdog_pid=$!
    wait "$osa_pid"
    osa_rc=$?
    kill "$watchdog_pid" 2>/dev/null
    wait "$watchdog_pid" 2>/dev/null
    set -e

    if [[ $osa_rc -eq 0 ]]; then
        layout_applied=1
        echo "make-dmg: layout applied"
    else
        echo "make-dmg: WARNING — Finder layout failed (exit $osa_rc). The DMG is valid but unstyled." >&2
        sed 's/^/make-dmg:   /' /tmp/cix-dmg-layout.log >&2 || true
        # Make the degradation visible in the run summary, not just buried in a
        # log nobody opens when the job is green.
        if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
            echo "::warning title=DMG shipped unstyled::Finder layout failed on the runner; the disk image has no background or icon positions."
        fi
        if [[ "$DMG_LAYOUT" == "require" ]]; then
            echo "make-dmg: DMG_LAYOUT=require — aborting" >&2
            exit 1
        fi
    fi
fi

# --- Volume icon ------------------------------------------------------------
# The red installer colourway, so the mounted disk and the app inside it are
# never confused for each other in Finder.
#
# This has to happen AFTER the Finder layout, not in the staging directory.
# Staging it up front looks like it works — hdiutil copies the file and SetFile
# sets the flag — but Finder then removes both while laying the window out, and
# the finished image ships with a generic disk icon. Verified by bisecting the
# steps: create/attach/SetFile/detach/convert preserves the icon; adding the
# Finder pass is what loses it.
echo "make-dmg: installing volume icon"
iconutil -c icns "$REPO_ROOT/mac/Resources/cix-installer.iconset" -o "$MOUNT_POINT/.VolumeIcon.icns"
# Without the custom-icon flag, .VolumeIcon.icns is just a hidden file.
SetFile -a C "$MOUNT_POINT"

sync
echo "make-dmg: unmounting"
hdiutil detach "$ATTACHED_DEV" -quiet
ATTACHED_DEV=""

echo "make-dmg: compressing"
rm -f "$DMG"
hdiutil convert "$RW_DMG" -format UDZO -imagekey zlib-level=9 -quiet -o "$DMG"

echo "make-dmg: verifying image"
hdiutil verify "$DMG"

# Ad-hoc sign the image too. It buys no Gatekeeper trust, but it makes tampering
# after publication detectable with codesign rather than only by checksum.
codesign --force --sign - "$DMG"

if [[ $layout_applied -eq 1 ]]; then
    echo "make-dmg: ok (styled) — $DMG"
else
    echo "make-dmg: ok (unstyled) — $DMG"
fi
shasum -a 256 "$DMG"
