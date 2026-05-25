#!/usr/bin/env bash
# sync-skills.sh — keep plugin-bundled skill files byte-identical with
# the canonical sources under skills/.
#
# Fix #19 acceptance: the plugin ships byte-identical copies of files
# that have a single source of truth elsewhere in the repo. Without
# this script, contributors edit one file and forget the mirror; the
# two diverge silently until someone runs cix-workspace via the plugin
# and gets a stale workflow.
#
# Files in scope (canonical source → plugin bundle destination):
#
#   skills/cix-workspace/SKILL.md
#     → plugins/cix/skills/cix-workspace/SKILL.md
#
#   skills/cix-workspace/agents/cix-workspace-investigator.md
#     → plugins/cix/agents/cix-workspace-investigator.md
#
# Out of scope: skills/cix/SKILL.md vs plugins/cix/skills/cix/SKILL.md —
# those are INTENTIONALLY different. The plugin version carries extra
# frontmatter (description, when_to_use, allowed-tools) the standalone
# skill loader doesn't need; treating them as drift would be wrong.
#
# Usage:
#   sync-skills.sh           # copy source → plugin, print what changed
#   sync-skills.sh --check   # diff only, exit 1 on drift (for CI / pre-commit)

set -euo pipefail

# Resolve repo root from the script's location so the script works no
# matter where it's invoked from (CI, IDE task runner, manual cd).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# (source, destination) pairs. Bash 3.2-compatible parallel arrays so
# this also runs on macOS's default /bin/bash.
SRC=(
    "skills/cix-workspace/SKILL.md"
    "skills/cix-workspace/agents/cix-workspace-investigator.md"
)
DST=(
    "plugins/cix/skills/cix-workspace/SKILL.md"
    "plugins/cix/agents/cix-workspace-investigator.md"
)

MODE="copy"
if [[ "${1:-}" == "--check" ]]; then
    MODE="check"
elif [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    sed -n '2,32p' "$0"
    exit 0
elif [[ -n "${1:-}" ]]; then
    echo "sync-skills.sh: unknown argument: $1" >&2
    echo "Run with --help for usage." >&2
    exit 2
fi

drift=0
for i in "${!SRC[@]}"; do
    src="$REPO_ROOT/${SRC[$i]}"
    dst="$REPO_ROOT/${DST[$i]}"

    if [[ ! -f "$src" ]]; then
        echo "sync-skills.sh: source missing: $src" >&2
        exit 3
    fi

    if [[ "$MODE" == "check" ]]; then
        # Skip the copy; just compare. -q suppresses output, exit code 0
        # = identical, 1 = differs, 2 = error.
        if ! diff -q "$src" "$dst" >/dev/null 2>&1; then
            echo "drift: ${SRC[$i]} != ${DST[$i]}" >&2
            drift=1
        fi
        continue
    fi

    # Copy mode — only act when the destination differs, so the log
    # only mentions files that actually changed. cmp -s is the standard
    # "are these byte-identical" test (returns 0 for identical, 1 for
    # different, 2 for I/O error).
    if ! cmp -s "$src" "$dst"; then
        mkdir -p "$(dirname "$dst")"
        cp "$src" "$dst"
        echo "synced: ${SRC[$i]} → ${DST[$i]}"
    fi
done

if [[ "$MODE" == "check" && $drift -ne 0 ]]; then
    echo "" >&2
    echo "Run plugins/cix/scripts/sync-skills.sh (no args) to fix." >&2
    exit 1
fi
