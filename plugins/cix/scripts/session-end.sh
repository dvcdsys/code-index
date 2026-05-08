#!/usr/bin/env bash
# SessionEnd hook for the cix plugin.
#
# Behavior: when the Claude Code session terminates, remove this
# session's cache files from $CLAUDE_PLUGIN_DATA. Cleanup is best-effort:
# SessionEnd may not fire if the process was killed forcibly (kill -9,
# OOM, panic), which is why session-start.sh also runs a 30-day GC sweep.
#
# Files removed (per session_id):
#   $CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID       (verdict cache)
#   $CLAUDE_PLUGIN_DATA/cix-grep-count-$SESSION_ID  (backoff counter)
#
# Output: nothing. Failures are silently ignored — there's no useful
# action Claude could take if cleanup fails, and a noisy hook here would
# leak into the user's last view of the session.

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

rm -f \
    "$CACHE_DIR/cix-aware-$SESSION_ID" \
    "$CACHE_DIR/cix-grep-count-$SESSION_ID" \
    2>/dev/null || true

exit 0
