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
#   File present with content "1"       → project is indexed, nudge allowed
#   File present with content "0"       → not indexed, nudge MUST stay silent
#   File present with content "unknown" → cix status timed out; grep-nudge
#                                          re-probes and upgrades to 0 or 1
#   File absent                         → no verdict yet, nudge stays silent
#
# Why "unknown" instead of "0" on timeout: a slow/unreachable server at
# session start used to write "0", which silenced nudges for the WHOLE
# session even if the server came up moments later. "unknown" lets the
# next Grep re-probe (see grep-nudge.sh) and recover.
#
# Why grep-nudge still won't fabricate nudges from "0": if cix status
# completed and said "not indexed" (project not registered, etc.), the
# user should NOT see Grep nudges suggesting `cix search` — that's a
# definitive negative, not a transient one.

set -euo pipefail

# Shared probe helpers (cix_resolve_bin, cix_probe_verdict).
# shellcheck source=lib-cix-probe.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-cix-probe.sh"

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
# We do NOT whitelist parent paths — users can have non-standard layouts
# (custom $CLAUDE_PLUGIN_DATA, XDG dirs, corporate setups). Safety comes
# from the file-level checks below: -maxdepth 1, -type f, exact -name
# patterns matching only our session-id-prefixed markers.
CACHE_DIR="${CLAUDE_PLUGIN_DATA:-/tmp}"
mkdir -p "$CACHE_DIR" 2>/dev/null || CACHE_DIR="/tmp"
[ -d "$CACHE_DIR" ] || exit 0

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
#
# Safety constraints on the find:
#   -maxdepth 1                — never recurse into subdirectories
#   -type f                    — files only (skips dirs, symlinks)
#   -name 'cix-aware-*' OR
#   -name 'cix-grep-count-*'   — exact prefix match on our marker names
#   -mtime +30                 — older than 30 days
#
# A file outside this prefix is invisible to find — it's never even
# considered for deletion, regardless of how the cache dir is configured.
find "$CACHE_DIR" -maxdepth 1 -type f \
    \( -name 'cix-aware-*' -o -name 'cix-grep-count-*' \) \
    -mtime +30 -delete 2>/dev/null || true

# ── Resolve a working `cix` binary ────────────────────────────────────────────
CIX_BIN="$(cix_resolve_bin)"

if [ -z "$CIX_BIN" ]; then
    # CLI not yet installed (would auto-bootstrap on first call). Mark off.
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Probe `cix status` (2s timeout) → three-state verdict ─────────────────────
VERDICT="$(cix_probe_verdict "$CIX_BIN" "$PROJECT_DIR" 2)"

if [ "$VERDICT" = "unknown" ]; then
    # Timed out — record "unknown" so grep-nudge re-probes later instead of
    # being silenced for the whole session.
    printf 'unknown' > "$CACHE_FILE"
    exit 0
fi

if [ "$VERDICT" != "1" ]; then
    # Definitive "not indexed". Stay silent for the session in this project.
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Project IS indexed — cache + inject reminder ──────────────────────────────
printf '1' > "$CACHE_FILE"

MESSAGE='💡 This project has a cix semantic code index. For semantic queries — finding code by meaning, cross-file lookups, symbol navigation, "where is X used", "how does Y work" — use the CLI: `cix search`, `cix def`, `cix refs` (via Bash). Activate the /cix SKILL for guidance. Use Grep only for exact strings (error messages, config keys, import paths). Run `cix status` if results seem stale.'

if command -v jq >/dev/null 2>&1; then
    jq -n --arg msg "$MESSAGE" \
        '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $msg}}'
else
    ESC=$(printf '%s' "$MESSAGE" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$ESC"
fi

exit 0
