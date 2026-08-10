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
# `codesign --deep` is deprecated by Apple and unreliable in general. It is not
# needed here at all now that the bundle holds a single executable — the ordered
# dylib-then-executable signing this used to do moved to build-runtime.sh, which
# is where the dylibs went.
#
# Why xattr -cr first
# -------------------
# server/Makefile records the failure this prevents: on macOS 26, amfid SIGKILLs
# an ad-hoc-signed binary carrying a stale signature or a com.apple.provenance
# xattr, and it does so with EMPTY STDERR — the process just dies. Every `cp`
# into the staging tree recreates those conditions, so the strip has to happen
# on every build, not once at install time.
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

# 1. The executable.
LAUNCHER="$MACOS_DIR/cix-launcher"
[[ -e "$LAUNCHER" ]] || { echo "sign-app: missing signing target: $LAUNCHER" >&2; exit 1; }

# The bundle should carry exactly one executable. Anything else means a stale
# tree — most likely a cix-server, cix or llama/ left over from a build that
# predates the runtime split, which would then be sealed into the signature and
# shipped as ~90 MB of dead weight.
shopt -s nullglob extglob
extra=("$MACOS_DIR"/!(cix-launcher))
shopt -u nullglob extglob
if [[ ${#extra[@]} -gt 0 ]]; then
    echo "sign-app: unexpected files in Contents/MacOS — this bundle was not built from scratch:" >&2
    printf '  %s\n' "${extra[@]}" >&2
    exit 1
fi

echo "sign-app: signing cix-launcher"
codesign --force --sign - "$LAUNCHER"

# 2. The bundle last — this seals the executable and the resources into
#    Contents/_CodeSignature.
echo "sign-app: signing bundle"
codesign --force --sign - "$APP"

# --strict is the point of this step: the lenient default accepts a bundle whose
# sealed resources no longer match what is on disk, which is exactly the state a
# partially re-copied bundle ends up in.
echo "sign-app: verifying"
codesign --verify --strict --verbose=2 "$APP"

echo "sign-app: ok"
