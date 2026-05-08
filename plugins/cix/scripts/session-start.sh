#!/usr/bin/env bash
# SessionStart hook for the cix plugin.
#
# Behavior: at session start, if the current project has a cix index
# (.cix/ directory present), inject a small system reminder into Claude's
# context telling it that semantic search is available.
#
# This is the "initial nudge" layer — fires exactly once per session,
# never repeats. The PreToolUse(Grep|Glob) hook handles ongoing
# anti-Grep nudges with exponential backoff.
#
# Output: nothing if no .cix/ exists; a JSON object with
# `hookSpecificOutput.additionalContext` otherwise.

set -euo pipefail

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"

# Detect whether this project is cix-indexed.
# Priority order:
#   1. `.cixignore` exists at project root (typical for indexed projects
#      that need to ignore node_modules / build dirs) — fast path.
#   2. `cix list` shows this project as registered on the server — slower
#      fallback that runs only at session start.
is_cix_project() {
    if [ -f "$PROJECT_DIR/.cixignore" ]; then
        return 0
    fi

    # Fallback: query cix server. Use plugin-bundled wrapper first,
    # then fall back to system PATH. Skip silently if neither is available.
    local cix_bin=""
    if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/cix" ]; then
        cix_bin="${CLAUDE_PLUGIN_ROOT}/bin/cix"
    elif command -v cix >/dev/null 2>&1; then
        cix_bin="$(command -v cix)"
    fi

    if [ -n "$cix_bin" ]; then
        # `cix list` is cheap (~100ms); grep for our project path.
        if "$cix_bin" list 2>/dev/null | grep -qF "$PROJECT_DIR"; then
            return 0
        fi
    fi

    return 1
}

if ! is_cix_project; then
    exit 0
fi

# We have an index. Inject a short reminder.
MESSAGE='💡 This project has a cix semantic code index. For semantic queries — finding code by meaning, cross-file lookups, symbol navigation, "where is X used", "how does Y work" — prefer `cix search`, `cix def`, `cix refs`, or the slash commands `/cix:search`, `/cix:def`, `/cix:refs`. Use Grep only for exact strings (error messages, config keys, import paths). Run `cix status` if results seem stale.'

# Emit JSON with hookSpecificOutput.additionalContext.
# jq is preferred for safe escaping; fall back to manual escaping if absent.
if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $msg}}'
else
    # Crude fallback — escape backslashes, quotes, and newlines.
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
