#!/usr/bin/env bash
# PostCompact hook for the cix plugin.
#
# Behavior: after Claude Code compacts the conversation, re-inject the
# SessionStart reminder if this (session, project) is cix-aware.
#
# Why this matters: skill bodies survive auto-compaction (Claude Code
# re-attaches them with up to 5K tokens per skill, see
# https://code.claude.com/docs/en/skills#skill-content-lifecycle).
# But the SessionStart `additionalContext` reminder — and PreToolUse
# nudges — are NOT skills. They live as regular tool result messages
# and are dropped/summarised during compaction.
#
# In long sessions (8+ hours of work) where the cix skill hasn't been
# invoked yet, the model may "forget" cix exists after compaction.
# Re-injecting the same one-line reminder keeps cix-awareness alive.
#
# This is a no-op if SessionStart concluded the project is not indexed
# (cache=0) or if no verdict exists yet.

set -euo pipefail

INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

[ -z "$SESSION_ID" ] && exit 0

CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

DIR_HASH=$(printf '%s' "$PROJECT_DIR" | shasum -a 256 2>/dev/null | cut -c1-8)
if [ -z "$DIR_HASH" ]; then
    DIR_HASH=$(printf '%s' "$PROJECT_DIR" | tr -c 'a-zA-Z0-9' '-' | tail -c 16)
fi

CACHE_FILE="$CACHE_DIR/cix-aware-$SESSION_ID-$DIR_HASH"

# ── Read verdict ──────────────────────────────────────────────────────────────
# Strict policy mirrors grep-nudge.sh: only "1" triggers re-injection.
if [ ! -f "$CACHE_FILE" ]; then
    exit 0
fi
if [ "$(cat "$CACHE_FILE" 2>/dev/null)" != "1" ]; then
    exit 0
fi

# ── Re-inject the SessionStart reminder ───────────────────────────────────────
MESSAGE='💡 (Post-compact reminder) This project has a cix semantic code index. For semantic queries — finding code by meaning, cross-file lookups, symbol navigation, "where is X used", "how does Y work" — prefer `cix search`, `cix def`, `cix refs`, or the slash commands `/cix:search`, `/cix:def`, `/cix:refs`. Use Grep only for exact strings (error messages, config keys, import paths).'

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "PostCompact", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"PostCompact","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
