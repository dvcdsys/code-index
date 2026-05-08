#!/usr/bin/env bash
# PreToolUse(Grep|Glob) hook for the cix plugin.
#
# Behavior: when the model is about to invoke Grep or Glob in a project
# that has a cix index, occasionally inject a system reminder pointing
# the model toward `cix search` for semantic queries.
#
# Throttling: exponential backoff. Reminders fire on the 1st, 2nd, 4th,
# 8th, 16th, 32nd, 64th, ... Grep/Glob invocation in the session.
# This means a 100-Grep session sees ~7 reminders total (~560 bytes),
# loud at the start where the model is "learning" the workflow, fading
# to silence as the session wears on.
#
# Per-session counter is kept in /tmp/cix-grep-count-$SESSION_ID. The
# session_id comes from the hook's stdin JSON.
#
# Output: nothing (silent) on non-power-of-2 invocations or in projects
# without .cix/. JSON with hookSpecificOutput.additionalContext on the
# 1st, 2nd, 4th, ... call.

set -euo pipefail

# Read JSON input from stdin
INPUT=$(cat 2>/dev/null || echo "{}")

# Extract session_id (required for state-tracking)
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    # Crude fallback regex extraction
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# No session_id → can't dedupe, stay silent.
if [ -z "$SESSION_ID" ]; then
    exit 0
fi

# Check whether the project is cix-indexed. Use only the fast path
# (`.cixignore` at project root) here — this hook fires on every Grep/Glob
# call, so we can't afford to query the cix server. Projects without
# `.cixignore` are silently ignored (false negative is acceptable; better
# than blocking the model's tool call on a network round-trip).
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
if [ ! -f "$PROJECT_DIR/.cixignore" ]; then
    exit 0
fi

# Increment per-session counter atomically-enough for our purposes.
# (Race conditions on parallel tool calls are fine — we may emit one
# extra or one fewer reminder, no big deal.)
COUNTER_FILE="/tmp/cix-grep-count-$SESSION_ID"
COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo 0)
# Sanitize counter (must be a non-negative integer)
case "$COUNT" in
    ''|*[!0-9]*) COUNT=0 ;;
esac
COUNT=$((COUNT + 1))
printf '%d' "$COUNT" > "$COUNTER_FILE"

# Power-of-2 check: COUNT & (COUNT - 1) == 0 means COUNT is 1, 2, 4, 8, 16, ...
# This implements exponential backoff cleanly.
if [ "$((COUNT & (COUNT - 1)))" -ne 0 ]; then
    # Not a power of 2 — stay silent.
    exit 0
fi

# Build the reminder message.
MESSAGE="💡 You're about to use Grep/Glob (call #$COUNT this session). This project has a cix semantic index — for queries by meaning (find by concept, cross-file lookups, symbol navigation), \`cix search\` / \`cix def\` / \`cix refs\` outperform Grep. Grep is best for exact strings (error messages, config keys, import paths). The \`/cix:search\` slash command is also available."

# Emit JSON with hookSpecificOutput.additionalContext.
if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "PreToolUse", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
