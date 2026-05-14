#!/usr/bin/env bash
# PostToolUse(Grep|Glob|Bash) hook for the cix plugin.
#
# Behavior: if SessionStart (or CwdChanged) concluded the current
# project is cix-indexed (cache file for this (session, project_dir)
# pair contains "1"), occasionally inject a system reminder pointing
# toward `cix search` instead of grep. Otherwise stay silent.
#
# Wired on PostToolUse (not PreToolUse) because current Claude Code
# only surfaces hookSpecificOutput.additionalContext for PostToolUse,
# UserPromptSubmit, and SessionStart — PreToolUse output without an
# explicit permissionDecision goes nowhere visible to the model.
# Firing after the grep means the model gets the nudge in time for
# the NEXT decision; behaviorally equivalent for an advisory hook.
#
# Bash is matched in addition to Grep/Glob because real-session usage
# of `grep`/`rg`/`find` happens through the Bash tool (pipelines, `| head`,
# `cd && grep …`). The Bash branch inspects tool_input.command and
# only proceeds when it looks like a grep- or find-family call; other
# Bash (ls, git status, make, go test) is fully silent and does not
# even increment the backoff counter.
#
# `find` is included because the agent often uses it to locate files
# by name pattern when a cix symbol/semantic query would be faster —
# `find . -name '*Auth*'` vs `cix search "auth"` / `cix def Auth`.
#
# This hook does NOT call `cix status` itself — it relies entirely on
# the cache written by SessionStart and refreshed by CwdChanged.
# Trade-off: a session that started before the cix-server came up will
# stay in "silent" mode for the rest of its life in that project, even
# if the server later comes back online. Intentional: better to miss a
# few nudge opportunities than spam a developer whose server is down.
#
# Per-(session, project) backoff: each project Claude visits has its
# own exponential-backoff counter. A new `cd` into a fresh project
# starts the backoff from scratch (call #1 → nudge), so the first Grep
# in a new cix-aware project always gets a reminder.
#
# Throttling: exponential backoff. Reminders fire on the 1st, 2nd, 4th,
# 8th, 16th, 32nd, 64th, ... Grep/Glob invocation in the current
# project. ~7 reminders per 100-Grep span, loud at the start, fading
# as the model "learns" the workflow.

set -euo pipefail

INPUT=$(cat 2>/dev/null || echo "{}")
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || echo "")
else
    SESSION_ID=$(printf '%s' "$INPUT" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi

# No session_id → can't read the SessionStart cache. Stay silent.
[ -z "$SESSION_ID" ] && exit 0

# ── Gate by tool_name + command shape ─────────────────────────────────────────
# For Grep/Glob the intent is unambiguous — always proceed (still subject to
# the cache check and exponential backoff below). For Bash we additionally
# inspect tool_input.command and only proceed when it looks like a grep- or
# find-family command; other Bash exits silently here WITHOUT bumping the
# backoff counter, so ls/git status/make/etc. stay invisible.
#
# Without jq the Bash branch falls through to "silent": parsing shell commands
# out of a JSON blob with sed invites false positives, and silent is safer than
# nudging on every Bash call.
TOOL_NAME=""
if command -v jq >/dev/null 2>&1; then
    TOOL_NAME=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null || echo "")
fi

case "$TOOL_NAME" in
    Grep|Glob)
        : # always proceed
        ;;
    Bash)
        TOOL_CMD=""
        if command -v jq >/dev/null 2>&1; then
            TOOL_CMD=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || echo "")
        fi
        # Match grep/egrep/fgrep/rg/ripgrep/find as a standalone token.
        # Anchors: start-of-string, whitespace, `|`, `;`, `&`, backtick, `(`.
        # The regex rejects `git grep` (subcommand after `git`, not a
        # standalone shell `grep`) and substring hits like `grepl`,
        # `egrep_helper`, `findme`.
        if ! [[ "$TOOL_CMD" =~ (^|[[:space:]\|\&\;\`\(])(grep|egrep|fgrep|rg|ripgrep|find)([[:space:]]|$) ]]; then
            exit 0
        fi
        ;;
    *)
        # Unknown tool_name, or no jq available → silent.
        exit 0
        ;;
esac

CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

# Compute per-project hash — same algorithm as session-start.sh.
DIR_HASH=$(printf '%s' "$PROJECT_DIR" | shasum -a 256 2>/dev/null | cut -c1-8)
if [ -z "$DIR_HASH" ]; then
    DIR_HASH=$(printf '%s' "$PROJECT_DIR" | tr -c 'a-zA-Z0-9' '-' | tail -c 16)
fi

# ── Read SessionStart's verdict for THIS project ──────────────────────────────
# Strict policy: only "1" allows nudges. Missing file or "0" → silent.
CACHE_FILE="$CACHE_DIR/cix-aware-$SESSION_ID-$DIR_HASH"

if [ ! -f "$CACHE_FILE" ]; then
    exit 0
fi
if [ "$(cat "$CACHE_FILE" 2>/dev/null)" != "1" ]; then
    exit 0
fi

# ── Increment per-(session, project) counter ──────────────────────────────────
COUNTER_FILE="$CACHE_DIR/cix-grep-count-$SESSION_ID-$DIR_HASH"
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
MESSAGE="💡 You just ran a file/text search in this project (call #$COUNT this session). This project has a cix semantic index — next time, for queries by meaning (find by concept, cross-file lookups, symbol navigation, locating files by symbol name), the CLI commands \`cix search\` / \`cix def\` / \`cix refs\` outperform grep/find. grep and find are best for exact strings or filename patterns (error messages, config keys, import paths, glob extensions). Recommended to activate /cix SKILL to use cix effectively"

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "PostToolUse", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
