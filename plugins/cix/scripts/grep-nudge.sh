#!/usr/bin/env bash
# PreToolUse(Grep|Glob) hook for the cix plugin.
#
# Behavior: if SessionStart concluded the project is cix-indexed
# ($CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID == "1"), occasionally
# inject a system reminder pointing toward `cix search` instead of
# Grep/Glob. Otherwise stay completely silent.
#
# This hook does NOT call `cix status` itself — it relies entirely on
# the cache written by SessionStart. Trade-off: a session that started
# before the cix-server came up will stay in "silent" mode for the rest
# of its life, even if the server is now reachable. That's intentional:
# we'd rather miss a few opportunities to nudge than spam a developer
# whose server is offline.
#
# Throttling: exponential backoff. Reminders fire on the 1st, 2nd, 4th,
# 8th, 16th, 32nd, 64th, ... Grep/Glob invocation in the session.
# A 100-Grep session sees ~7 reminders total (~560 bytes), loud at the
# start, fading to silence as the session wears on.

set -euo pipefail

INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# No session_id → can't read the SessionStart cache. Stay silent.
[ -z "$SESSION_ID" ] && exit 0

# ── Read SessionStart's verdict ───────────────────────────────────────────────
# Strict policy: only "1" allows nudges. Missing file or "0" → silent.
CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
CACHE_FILE="$CACHE_DIR/cix-aware-$SESSION_ID"

if [ ! -f "$CACHE_FILE" ]; then
    exit 0
fi
if [ "$(cat "$CACHE_FILE" 2>/dev/null)" != "1" ]; then
    exit 0
fi

# ── Increment per-session counter ─────────────────────────────────────────────
COUNTER_FILE="$CACHE_DIR/cix-grep-count-$SESSION_ID"
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
