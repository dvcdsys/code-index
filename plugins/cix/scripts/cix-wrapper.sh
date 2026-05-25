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

# Pinned CLI release used for auto-bootstrap. Both the install.sh script ref
# AND the --version arg below are pinned to this tag so a fresh bootstrap is
# reproducible and auditable — never silently tracking whatever is on `main`.
# Bump this as part of CLI releases (see CONTRIBUTING.md / doc/RELEASES.md).
CIX_PINNED_VERSION="cli/v0.5.0"

# ── Optional stdout cap (CIX_MAX_OUTPUT_LINES) ────────────────────────────────
# When CIX_MAX_OUTPUT_LINES is set to a positive integer, cap stdout to that
# many lines and append a one-line truncation notice (on stdout, so the cap is
# observable through a pipe). Unset/empty/0/non-numeric → behave EXACTLY like a
# bare cix call: exec the binary directly, full output, streamed, original exit
# code, zero overhead. stderr always passes through uncapped so errors are
# never hidden. The cap is layered on top of any user `--limit N` flag, not a
# replacement for it.
run_cix() {
    local bin="$1"
    shift

    case "${CIX_MAX_OUTPUT_LINES:-}" in
        ''|*[!0-9]*|0)
            exec "$bin" "$@"
            ;;
    esac

    local max="$CIX_MAX_OUTPUT_LINES"
    local out rc
    if out="$("$bin" "$@")"; then rc=0; else rc=$?; fi

    local total
    total=$(printf '%s\n' "$out" | wc -l | tr -d '[:space:]')
    if [ "$total" -gt "$max" ]; then
        printf '%s\n' "$out" | head -n "$max" || true
        printf '… [cix output truncated to %s of %s lines; unset CIX_MAX_OUTPUT_LINES for full output]\n' "$max" "$total"
    else
        printf '%s\n' "$out"
    fi
    exit "$rc"
}

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
    run_cix "$SYS_CIX" "$@"
fi

# ── Bootstrap install via install.sh (one-time) ───────────────────────────────
TARGET="${CIX_PLUGIN_BIN_DIR:-$HOME/.local/bin}"
CACHED_CIX="$TARGET/cix"

if [ ! -x "$CACHED_CIX" ]; then
    # Opt-out for corp / regulated / air-gapped environments: refuse to reach
    # out to the network. Fail cleanly with manual instructions instead of
    # silently fetching install.sh.
    if [ -n "${CIX_NO_AUTOINSTALL:-}" ] && [ "${CIX_NO_AUTOINSTALL}" != "0" ]; then
        echo "Error: cix is not installed and CIX_NO_AUTOINSTALL is set — refusing to auto-install." >&2
        echo "Install cix manually (pinned to ${CIX_PINNED_VERSION}):" >&2
        echo "  curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/${CIX_PINNED_VERSION}/install.sh | bash -s -- --version ${CIX_PINNED_VERSION}" >&2
        echo "Or set CIX_PLUGIN_BIN_DIR to a directory that already contains cix." >&2
        exit 1
    fi

    if ! command -v curl >/dev/null 2>&1; then
        echo "Error: cix is not installed and curl is not available to bootstrap it." >&2
        echo "Install cix manually: https://github.com/dvcdsys/code-index" >&2
        exit 1
    fi

    mkdir -p "$TARGET"
    echo "cix CLI not found — installing ${CIX_PINNED_VERSION} to $TARGET (one-time, no sudo)..." >&2

    # Official install script, pinned to the release tag for reproducibility:
    # the script ref AND the installed binary version are both fixed to
    # $CIX_PINNED_VERSION. Bump the constant at the top of this file on CLI
    # releases.
    INSTALL_URL="https://raw.githubusercontent.com/dvcdsys/code-index/${CIX_PINNED_VERSION}/install.sh"

    if ! curl -fsSL "$INSTALL_URL" | bash -s -- --bin-dir "$TARGET" --version "$CIX_PINNED_VERSION"; then
        echo "Error: cix install failed. Check network connectivity and try again." >&2
        echo "You can install manually: curl -fsSL $INSTALL_URL | bash -s -- --version ${CIX_PINNED_VERSION}" >&2
        exit 1
    fi

    if [ ! -x "$CACHED_CIX" ]; then
        echo "Error: install.sh ran but $CACHED_CIX was not created." >&2
        exit 1
    fi

    echo "cix installed successfully at $CACHED_CIX" >&2
fi

run_cix "$CACHED_CIX" "$@"
