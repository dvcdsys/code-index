#!/usr/bin/env bash
# SessionStart hook for the cix plugin.
#
# Behavior: at session start, ask `cix status` whether the current
# project is indexed. If yes, inject a one-line system reminder into
# Claude's context and cache the "is indexed" decision in
# /tmp/cix-aware-$SESSION_ID so the PreToolUse hook can short-circuit
# without re-querying the server.
#
# Why `cix status` and not `.cixignore`:
#   - Authoritative: queries the cix-server, returns exit 0 only if the
#     project is registered AND reachable at session start.
#   - Fast: ~17ms on a local server (HTTP + SQLite lookup).
#   - Robust: works for projects that don't keep a `.cixignore` file.
#
# Output: nothing if the project isn't indexed (or cix isn't available);
# a JSON object with `hookSpecificOutput.additionalContext` otherwise.
#
# Cache contract (read by grep-nudge.sh):
#   /tmp/cix-aware-$SESSION_ID exists with content "1" → project is indexed
#   File absent or content "0" → not indexed, stay silent

set -euo pipefail

# ── Read session_id from stdin JSON (Claude Code provides it) ─────────────────
INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
CACHE_FILE=""
if [ -n "$SESSION_ID" ]; then
    CACHE_FILE="/tmp/cix-aware-$SESSION_ID"
fi

# ── Resolve a working `cix` binary ────────────────────────────────────────────
# Prefer the plugin-bundled wrapper, fall back to whatever is on PATH.
CIX_BIN=""
if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/cix" ]; then
    CIX_BIN="${CLAUDE_PLUGIN_ROOT}/bin/cix"
elif command -v cix >/dev/null 2>&1; then
    CIX_BIN="$(command -v cix)"
fi

if [ -z "$CIX_BIN" ]; then
    # No cix CLI yet (would be auto-installed on first call). Stay silent.
    [ -n "$CACHE_FILE" ] && printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Run `cix status` with a 2-second timeout (no coreutils dependency) ────────
# We background the call, then wait up to 2s for it to finish. If it doesn't,
# kill it and treat as "not indexed".
(
    "$CIX_BIN" status -p "$PROJECT_DIR" >/dev/null 2>&1
    echo "$?" > "${CACHE_FILE:-/dev/null}.exit" 2>/dev/null || true
) &
CIX_PID=$!

# Poll for completion up to 20 × 100ms = 2s
SLEPT=0
while kill -0 "$CIX_PID" 2>/dev/null && [ "$SLEPT" -lt 20 ]; do
    sleep 0.1
    SLEPT=$((SLEPT + 1))
done

if kill -0 "$CIX_PID" 2>/dev/null; then
    # Still running — kill it and treat as not indexed.
    kill -9 "$CIX_PID" 2>/dev/null || true
    wait "$CIX_PID" 2>/dev/null || true
    [ -n "$CACHE_FILE" ] && printf '0' > "$CACHE_FILE"
    exit 0
fi

wait "$CIX_PID" 2>/dev/null || true

# Read the captured exit code (file written by the backgrounded subshell).
EXIT_CODE=1
if [ -n "$CACHE_FILE" ] && [ -f "${CACHE_FILE}.exit" ]; then
    EXIT_CODE=$(cat "${CACHE_FILE}.exit" 2>/dev/null || echo 1)
    rm -f "${CACHE_FILE}.exit"
fi

if [ "$EXIT_CODE" != "0" ]; then
    # Not indexed (or cix-server unreachable). Stay silent.
    [ -n "$CACHE_FILE" ] && printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Project IS indexed — cache + inject reminder ──────────────────────────────
[ -n "$CACHE_FILE" ] && printf '1' > "$CACHE_FILE"

MESSAGE='💡 This project has a cix semantic code index. For semantic queries — finding code by meaning, cross-file lookups, symbol navigation, "where is X used", "how does Y work" — prefer `cix search`, `cix def`, `cix refs`, or the slash commands `/cix:search`, `/cix:def`, `/cix:refs`. Use Grep only for exact strings (error messages, config keys, import paths). Run `cix status` if results seem stale.'

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
