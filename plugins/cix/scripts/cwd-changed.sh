#!/usr/bin/env bash
# CwdChanged hook for the cix plugin.
#
# Behavior: when Claude changes working directory mid-session (e.g. via
# `cd`), evaluate cix-awareness for the new directory and cache the
# verdict. If we already have a verdict for this (session, project_dir)
# pair, this is a no-op — Claude probably came back to a project we
# already evaluated.
#
# Why no reminder injection: PreToolUse(Grep|Glob) handles the
# "first nudge in a fresh project" case via its per-project backoff
# counter (call #1 in a new project always fires). Re-inject a SessionStart
# reminder on every `cd` would be noisy if Claude bounces between
# directories.
#
# Behavior matrix:
#   Cache exists for (session, NEW_DIR) → no-op (we know already)
#   Cache absent + cix status exit 0    → write "1" (cix-aware)
#   Cache absent + cix status exit ≠ 0  → write "0" (silent for this dir)
#   Cache absent + cix CLI not found    → write "0"
#   Cache absent + cix status timeout   → write "0"

set -euo pipefail

INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

[ -z "$SESSION_ID" ] && exit 0

CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
mkdir -p "$CACHE_DIR" 2>/dev/null || CACHE_DIR="/tmp"

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

DIR_HASH=$(printf '%s' "$PROJECT_DIR" | shasum -a 256 2>/dev/null | cut -c1-8)
if [ -z "$DIR_HASH" ]; then
    DIR_HASH=$(printf '%s' "$PROJECT_DIR" | tr -c 'a-zA-Z0-9' '-' | tail -c 16)
fi

CACHE_FILE="$CACHE_DIR/cix-aware-$SESSION_ID-$DIR_HASH"

# ── Already evaluated this (session, project) — no-op ─────────────────────────
if [ -f "$CACHE_FILE" ]; then
    exit 0
fi

# ── Resolve cix binary ────────────────────────────────────────────────────────
CIX_BIN=""
if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/cix" ]; then
    CIX_BIN="${CLAUDE_PLUGIN_ROOT}/bin/cix"
elif command -v cix >/dev/null 2>&1; then
    CIX_BIN="$(command -v cix)"
fi

if [ -z "$CIX_BIN" ]; then
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Run cix status with 2s timeout (same pattern as session-start.sh) ─────────
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

if [ "$EXIT_CODE" = "0" ]; then
    printf '1' > "$CACHE_FILE"
else
    printf '0' > "$CACHE_FILE"
fi

# Silent — no context injection. PreToolUse(Grep|Glob) will handle the
# first-Grep-in-new-project nudge through its own backoff counter.
exit 0
