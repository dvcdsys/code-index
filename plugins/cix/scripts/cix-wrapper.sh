#!/usr/bin/env bash
# cix CLI wrapper for the Claude Code plugin.
#
# Strategy: "use system cix if available, else bootstrap install via the
# official install.sh script". We do NOT bundle the binary in git or
# maintain a separate cache — install.sh is the single source of truth.
#
# Resolution order:
#   1. If `cix` is found anywhere in PATH (excluding our own dir),
#      exec it directly.
#   2. Otherwise, run install.sh with --bin-dir=$HOME/.local/bin
#      (no sudo required), then exec the freshly installed binary.

set -euo pipefail

# ── Resolve our own directory (real path, dereferencing symlinks) ─────────────
# bin/cix is a symlink to ../scripts/cix-wrapper.sh, so BASH_SOURCE points to
# the real script under scripts/, not the symlink under bin/. We need the
# directory of the symlink (which is what's actually on PATH) — derive it
# from $0 instead, which preserves the invocation path.

if [ -n "${0:-}" ] && [ "${0:0:1}" = "/" ]; then
    INVOKED_PATH="$0"
else
    # When called as bare `cix` via PATH, $0 is just "cix" — fall back to
    # which/command -v to find ourselves.
    INVOKED_PATH="$(command -v "$0" 2>/dev/null || echo "$0")"
fi

SELF_DIR="$(cd "$(dirname "$INVOKED_PATH")" 2>/dev/null && pwd 2>/dev/null || echo "")"

# ── Look for a cix binary elsewhere in PATH ───────────────────────────────────
# Build a "safe PATH" that excludes our own directory so command -v doesn't
# find us recursively.

SYS_CIX=""
if [ -n "$SELF_DIR" ]; then
    SAFE_PATH=""
    OLD_IFS="$IFS"
    IFS=':'
    # shellcheck disable=SC2086
    for dir in $PATH; do
        [ -z "$dir" ] && continue
        DIR_REAL="$(cd "$dir" 2>/dev/null && pwd 2>/dev/null || echo "$dir")"
        if [ "$DIR_REAL" != "$SELF_DIR" ]; then
            SAFE_PATH="${SAFE_PATH:+$SAFE_PATH:}$dir"
        fi
    done
    IFS="$OLD_IFS"
    SYS_CIX="$(PATH="$SAFE_PATH" command -v cix 2>/dev/null || true)"
else
    SYS_CIX="$(command -v cix 2>/dev/null || true)"
fi

if [ -n "$SYS_CIX" ]; then
    exec "$SYS_CIX" "$@"
fi

# ── Bootstrap install via install.sh (one-time) ───────────────────────────────
TARGET="${CIX_PLUGIN_BIN_DIR:-$HOME/.local/bin}"
CACHED_CIX="$TARGET/cix"

if [ ! -x "$CACHED_CIX" ]; then
    if ! command -v curl >/dev/null 2>&1; then
        echo "Error: cix is not installed and curl is not available to bootstrap it." >&2
        echo "Install cix manually: https://github.com/dvcdsys/code-index" >&2
        exit 1
    fi

    mkdir -p "$TARGET"
    echo "cix CLI not found — installing to $TARGET (one-time, no sudo)..." >&2

    # Use the official install script. Pinned to main; future versions of the
    # plugin can pin to a tag (e.g. cli/v0.4.0) for reproducibility.
    INSTALL_URL="https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh"

    if ! curl -fsSL "$INSTALL_URL" | bash -s -- --bin-dir "$TARGET"; then
        echo "Error: cix install failed. Check network connectivity and try again." >&2
        echo "You can install manually: curl -fsSL $INSTALL_URL | bash" >&2
        exit 1
    fi

    if [ ! -x "$CACHED_CIX" ]; then
        echo "Error: install.sh ran but $CACHED_CIX was not created." >&2
        exit 1
    fi

    echo "cix installed successfully at $CACHED_CIX" >&2
fi

exec "$CACHED_CIX" "$@"
