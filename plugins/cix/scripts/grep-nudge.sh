#!/usr/bin/env bash
# PreToolUse(Grep|Glob) hook for the cix plugin.
#
# Behavior: when the model is about to invoke Grep or Glob in a project
# that the SessionStart hook flagged as cix-indexed, occasionally inject
# a system reminder pointing toward `cix search`.
#
# Throttling: exponential backoff. Reminders fire on the 1st, 2nd, 4th,
# 8th, 16th, 32nd, 64th, ... Grep/Glob invocation in the session.
# A 100-Grep session sees ~7 reminders total (~560 bytes), loud at the
# start where the model is "learning" the workflow, fading to silence
# as the session wears on.
#
# Project detection: read /tmp/cix-aware-$SESSION_ID, written by
# session-start.sh after a successful `cix status` query. We do NOT
# call `cix status` ourselves here — this hook fires on every Grep/Glob
# call, so re-querying the server would be wasteful. Falls back to
# checking `.cixignore` if the cache file is missing (e.g. session
# resumed without re-running SessionStart).
#
# Output: nothing on non-power-of-2 invocations or in non-indexed
# projects. JSON with hookSpecificOutput.additionalContext on the 1st,
# 2nd, 4th, ... call.

set -euo pipefail

# Read JSON input from stdin
INPUT=$(cat 2>/dev/null || echo "{}")

if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# No session_id → can't dedupe nor read cache, stay silent.
if [ -z "$SESSION_ID" ]; then
    exit 0
fi

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

# ── Decide whether project is cix-aware ───────────────────────────────────────
# Primary source: cache written by session-start.sh.
# Fallback: presence of .cixignore (less authoritative but doesn't require
# the SessionStart hook to have run, useful for resumed sessions).
CACHE_FILE="/tmp/cix-aware-$SESSION_ID"
IS_AWARE=0

if [ -f "$CACHE_FILE" ]; then
    [ "$(cat "$CACHE_FILE" 2>/dev/null)" = "1" ] && IS_AWARE=1
elif [ -f "$PROJECT_DIR/.cixignore" ]; then
    IS_AWARE=1
fi

if [ "$IS_AWARE" != "1" ]; then
    exit 0
fi

# ── Increment per-session counter ─────────────────────────────────────────────
COUNTER_FILE="/tmp/cix-grep-count-$SESSION_ID"
COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo 0)
case "$COUNT" in
    ''|*[!0-9]*) COUNT=0 ;;
esac
COUNT=$((COUNT + 1))
printf '%d' "$COUNT" > "$COUNTER_FILE"

# Power-of-2 check: COUNT & (COUNT - 1) == 0 means COUNT is 1, 2, 4, 8, ...
if [ "$((COUNT & (COUNT - 1)))" -ne 0 ]; then
    exit 0
fi

# ── Emit nudge ────────────────────────────────────────────────────────────────
MESSAGE="💡 You're about to use Grep/Glob (call #$COUNT this session). This project has a cix semantic index — for queries by meaning (find by concept, cross-file lookups, symbol navigation), \`cix search\` / \`cix def\` / \`cix refs\` outperform Grep. Grep is best for exact strings (error messages, config keys, import paths). The \`/cix:search\` slash command is also available."

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "PreToolUse", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
