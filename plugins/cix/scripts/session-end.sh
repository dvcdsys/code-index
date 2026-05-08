#!/usr/bin/env bash
# SessionEnd hook for the cix plugin.
#
# Behavior: when the Claude Code session terminates, remove every
# cache file belonging to this session from $CLAUDE_PLUGIN_DATA.
# A single session may have visited multiple projects (via `cd`), so
# we glob-delete by session_id prefix. Cleanup is best-effort:
# SessionEnd may not fire if the process was killed forcibly (kill -9,
# OOM, panic) — session-start.sh also runs a 30-day GC sweep as a
# safety net.
#
# Files removed (per session_id, all directory hashes):
#   $CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID-*       (verdict caches)
#   $CLAUDE_PLUGIN_DATA/cix-grep-count-$SESSION_ID-*  (backoff counters)
#
# Output: nothing. Failures are silently ignored.

set -euo pipefail

INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# Without a session_id we don't know what to clean. Exit cleanly.
[ -z "$SESSION_ID" ] && exit 0

CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"

# ── Safety guard (mirror of session-start.sh) ─────────────────────────────────
# Refuse to operate outside known-safe locations before invoking find -delete.
case "$CACHE_DIR" in
    "$HOME/.claude/plugins/data"|"$HOME/.claude/plugins/data/"*) ;;
    "/tmp"|"/tmp/"*) ;;
    *)
        if [ -n "${TMPDIR:-}" ]; then
            case "$CACHE_DIR" in
                "${TMPDIR%/}"|"${TMPDIR%/}"/*) ;;
                *)
                    echo "cix plugin (session-end): refusing to operate on cache dir outside whitelist: $CACHE_DIR" >&2
                    exit 1
                    ;;
            esac
        else
            echo "cix plugin (session-end): refusing to operate on cache dir outside whitelist: $CACHE_DIR" >&2
            exit 1
        fi
        ;;
esac
[ -d "$CACHE_DIR" ] || exit 0

# Glob-delete every per-(session, dir) marker. Use find for safe handling
# of patterns when there are no matches (avoids "rm: ... no such file" noise).
# -maxdepth 1 + -type f + restrictive -name patterns ensures we only touch
# our own one-byte marker files.
find "$CACHE_DIR" -maxdepth 1 -type f \
    \( -name "cix-aware-$SESSION_ID-*" -o -name "cix-grep-count-$SESSION_ID-*" \) \
    -delete 2>/dev/null || true

exit 0
