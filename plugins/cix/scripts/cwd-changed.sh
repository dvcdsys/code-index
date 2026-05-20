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
#   Cache absent + cix status timeout   → write "unknown" (grep-nudge re-probes)

set -euo pipefail

# Shared probe helpers (cix_resolve_bin, cix_probe_verdict).
# shellcheck source=lib-cix-probe.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-cix-probe.sh"

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
CIX_BIN="$(cix_resolve_bin)"

if [ -z "$CIX_BIN" ]; then
    printf '0' > "$CACHE_FILE"
    exit 0
fi

# ── Probe cix status (2s timeout) → three-state verdict ───────────────────────
VERDICT="$(cix_probe_verdict "$CIX_BIN" "$PROJECT_DIR" 2)"
# "1" cix-aware · "0" not indexed · "unknown" timed out (grep-nudge re-probes).
printf '%s' "$VERDICT" > "$CACHE_FILE"

# Silent — no context injection. PostToolUse(Grep|Glob|Bash) handles the
# first-Grep-in-new-project nudge through its own backoff counter.
exit 0
