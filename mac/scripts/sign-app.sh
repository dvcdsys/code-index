#!/usr/bin/env bash
# sign-app.sh — ad-hoc sign cix.app, innermost code first.
#
# Usage: mac/scripts/sign-app.sh <path/to/cix.app>
#
# Why ad-hoc and not Developer ID
# -------------------------------
# This is an open-source project with no paid Apple Developer membership, so
# there is no identity to sign with and nothing to notarize. Ad-hoc signing
# (`--sign -`) is still mandatory: on Apple Silicon every executable must carry
# *some* valid signature or the kernel refuses to run it outright. Ad-hoc gets
# the code running; it does not satisfy Gatekeeper, which is why the DMG ships
# with first-run instructions.
#
# Why not --deep
# --------------
# `codesign --deep` is deprecated by Apple and unreliable for a bundle like this
# one, which carries four executables and a pile of dylibs directly in
# Contents/MacOS rather than as nested .app/.framework bundles. Signing bottom
# up is explicit, ordered, and each step is verifiable.
#
# Why xattr -cr first
# -------------------
# server/Makefile records the failure this prevents: on macOS 26, amfid SIGKILLs
# an ad-hoc-signed binary whose linked dylibs carry a stale signature or a
# com.apple.provenance xattr, and it does so with EMPTY STDERR — the process
# just dies. Every `cp` into the staging tree recreates those conditions, so the
# strip has to happen on every build, not once at install time.
set -euo pipefail

APP="${1:-}"
if [[ -z "$APP" ]]; then
    echo "usage: $(basename "$0") <path/to/cix.app>" >&2
    exit 2
fi
if [[ ! -d "$APP" ]]; then
    echo "sign-app: no such bundle: $APP" >&2
    exit 1
fi

echo "sign-app: stripping extended attributes from $APP"
xattr -cr "$APP"

MACOS_DIR="$APP/Contents/MacOS"

sign_one() {
    local target="$1"
    [[ -e "$target" ]] || { echo "sign-app: missing signing target: $target" >&2; exit 1; }
    codesign --force --sign - "$target"
}

# 1. Libraries. llama-server links these by @rpath; if a dylib's signature is
#    stale relative to the executable that loads it, the load fails at dyld
#    time — after codesign has happily reported success on the executable.
shopt -s nullglob
dylibs=("$MACOS_DIR"/llama/*.dylib)
shopt -u nullglob
if [[ ${#dylibs[@]} -eq 0 ]]; then
    echo "sign-app: no dylibs found under $MACOS_DIR/llama — the bundle is incomplete" >&2
    exit 1
fi
echo "sign-app: signing ${#dylibs[@]} dylib(s)"
for lib in "${dylibs[@]}"; do
    sign_one "$lib"
done

# 2. Executables, leaf-most first.
for bin in llama/llama-server cix cix-server cix-launcher; do
    echo "sign-app: signing $bin"
    sign_one "$MACOS_DIR/$bin"
done

# 3. The bundle last — this seals everything above into Contents/_CodeSignature.
echo "sign-app: signing bundle"
codesign --force --sign - "$APP"

# --strict is the point of this step: the lenient default accepts a bundle whose
# sealed resources no longer match what is on disk, which is exactly the state a
# partially re-copied bundle ends up in.
echo "sign-app: verifying"
codesign --verify --strict --verbose=2 "$APP"

echo "sign-app: ok"
