#!/usr/bin/env bash
# SessionStart hook for the cix plugin.
#
# Behavior: at session start, ask `cix status` whether the current
# project is indexed. The result is cached for the (session, project)
# pair in $CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID-$DIR_HASH so the
# PreToolUse hook can short-circuit without re-querying the server.
#
# Cache key includes a hash of the project directory, so a single
# session that traverses multiple projects (via `cd`, see CwdChanged
# hook) keeps a separate verdict per project — fresh backoff counter
# per project, correct cix-aware state per directory.
#
# State location: $CLAUDE_PLUGIN_DATA is plugin-persistent storage
# managed by Claude Code (resolves to ~/.claude/plugins/data/<plugin>/).
# It survives plugin updates and is NOT periodically cleaned by the OS,
# unlike /tmp (macOS daily cleanup of 3-day-old files; Linux on reboot).
# Falls back to /tmp only when run outside a plugin context (tests).
#
# Decision contract (read by grep-nudge.sh, post-compact.sh):
#   File present with content "1" → project is indexed, nudge allowed
#   File present with content "0" → not indexed, nudge MUST stay silent
#   File absent                   → no verdict yet, nudge stays silent
#
# Why no fallback in grep-nudge: if SessionStart (or CwdChanged) concluded
# "not indexed" (server unreachable, project not registered, etc.), the
# user should NOT see Grep nudges suggesting `cix search` for the rest
# of the session. Sending nudges based on `.cixignore` presence anyway
# would create false positives.

set -euo pipefail

# ── Read session_id from stdin JSON ───────────────────────────────────────────
INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# Without a session_id we can't write a session-scoped marker. Stay silent.
if [ -z "$SESSION_ID" ]; then
    exit 0
fi

# ── Resolve cache directory ───────────────────────────────────────────────────
# Prefer plugin-persistent storage; fall back to /tmp for ad-hoc/test invocations.
CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
mkdir -p "$CACHE_DIR" 2>/dev/null || CACHE_DIR="/tmp"

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

# Hash the project dir so the cache file name is short and stable.
# `shasum -a 256` exists on both macOS (Perl-based) and Linux (coreutils).
DIR_HASH=$(printf '%s' "$PROJECT_DIR" | shasum -a 256 2>/dev/null | cut -c1-8)
if [ -z "$DIR_HASH" ]; then
    # shasum unavailable; fall back to a path-derived suffix.
    DIR_HASH=$(printf '%s' "$PROJECT_DIR" | tr -c 'a-zA-Z0-9' '-' | tail -c 16)
fi

CACHE_FILE="$CACHE_DIR/cix-aware-$SESSION_ID-$DIR_HASH"

# ── Light maintenance: clear markers older than 30 days ───────────────────────
# Long-running Claude Code installs would accumulate one-byte markers
# otherwise. Cheap, runs once per session. Failures ignored.
find "$CACHE_DIR" -maxdepth 1 -type f \( -name 'cix-aware-*' -o -name 'cix-grep-count-*' \) -mtime +30 -delete 2>/dev/null || true

# ── Resolve a working `cix` binary ────────────────────────────────────────────
CIX_BIN=""
if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/cix" ]; then
    CIX_BIN="${CLAUDE_PLUGIN_ROOT}/bin/cix"
elif command -v cix >/dev/null 2>&1; then
    CIX_BIN="$(command -v cix)"
fi

if [ -z "$CIX_BIN" ]; then
    # CLI not yet installed (would auto-bootstrap on first call). Mark off.
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Run `cix status` with a 2-second timeout ──────────────────────────────────
# macOS lacks `timeout`/`gtimeout` by default — implement in pure bash.
EXIT_FILE="$CACHE_FILE.exit"
(
    "$CIX_BIN" status -p "$PROJECT_DIR" >/dev/null 2>&1
    echo "$?" > "$EXIT_FILE" 2>/dev/null
) &
CIX_PID=$!

SLEPT=0
while kill -0 "$CIX_PID" 2>/dev/null && [ "$SLEPT" -lt 20 ]; do
    sleep 0.1
    SLEPT=$((SLEPT + 1))
done

if kill -0 "$CIX_PID" 2>/dev/null; then
    kill -9 "$CIX_PID" 2>/dev/null || true
    wait "$CIX_PID" 2>/dev/null || true
    printf '0' > "$CACHE_FILE"
    rm -f "$EXIT_FILE"
    exit 0
fi
wait "$CIX_PID" 2>/dev/null || true

EXIT_CODE=1
if [ -f "$EXIT_FILE" ]; then
    EXIT_CODE=$(cat "$EXIT_FILE" 2>/dev/null || echo 1)
    rm -f "$EXIT_FILE"
fi

if [ "$EXIT_CODE" != "0" ]; then
    # Not indexed (or server unreachable). Lock the session into "off" mode.
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Project IS indexed — cache + inject reminder ──────────────────────────────
printf '1' > "$CACHE_FILE"

MESSAGE='💡 This project has a cix semantic code index. For semantic queries — finding code by meaning, cross-file lookups, symbol navigation, "where is X used", "how does Y work" — prefer `cix search`, `cix def`, `cix refs`, or the slash commands `/cix:search`, `/cix:def`, `/cix:refs`. Use Grep only for exact strings (error messages, config keys, import paths). Run `cix status` if results seem stale.'

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
