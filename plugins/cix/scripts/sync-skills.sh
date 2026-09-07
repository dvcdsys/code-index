#!/usr/bin/env bash
# sync-skills.sh — keep Claude Code plugin bundles byte-identical with their
# canonical sources, and keep Codex skill bodies synchronized with native
# Codex frontmatter.
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
#     → plugins/cix-openai/agents/cix-workspace-investigator.md
#
#   plugins/cix/skills/cix/SKILL.md (body + name/description)
#     → plugins/cix-openai/skills/cix/SKILL.md (Codex frontmatter)
#
#   skills/cix-workspace/SKILL.md (body + name/description)
#     → plugins/cix-openai/skills/cix-workspace/SKILL.md (Codex frontmatter)
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
    "skills/cix-workspace/agents/cix-workspace-investigator.md"
)
DST=(
    "plugins/cix/skills/cix-workspace/SKILL.md"
    "plugins/cix/agents/cix-workspace-investigator.md"
    "plugins/cix-openai/agents/cix-workspace-investigator.md"
)

CODEX_SRC=(
    "plugins/cix/skills/cix/SKILL.md"
    "skills/cix-workspace/SKILL.md"
)
CODEX_DST=(
    "plugins/cix-openai/skills/cix/SKILL.md"
    "plugins/cix-openai/skills/cix-workspace/SKILL.md"
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

# Codex requires only name and description in SKILL.md frontmatter. Preserve
# the canonical body verbatim while dropping Claude-only routing/tool fields;
# Codex invocation policy and UI metadata live in agents/openai.yaml.
render_codex_skill() {
    awk '
        BEGIN { fence = 0; skill_name = ""; skill_description = "" }
        /^---$/ {
            fence++
            if (fence == 2) {
                print "---"
                print "name: " skill_name
                print "description: " skill_description
                print "---"
            }
            next
        }
        fence == 1 {
            if ($0 ~ /^name: /) {
                skill_name = substr($0, 7)
            } else if ($0 ~ /^description: /) {
                skill_description = substr($0, 14)
            }
            next
        }
        fence >= 2 { print }
    ' "$1"
}

for i in "${!CODEX_SRC[@]}"; do
    src="$REPO_ROOT/${CODEX_SRC[$i]}"
    dst="$REPO_ROOT/${CODEX_DST[$i]}"
    tmp="$(mktemp "${TMPDIR:-/tmp}/cix-codex-skill.XXXXXX")"
    render_codex_skill "$src" >"$tmp"

    if [[ "$MODE" == "check" ]]; then
        if ! diff -q "$tmp" "$dst" >/dev/null 2>&1; then
            echo "drift: ${CODEX_SRC[$i]} != ${CODEX_DST[$i]} (Codex projection)" >&2
            drift=1
        fi
    elif ! cmp -s "$tmp" "$dst"; then
        mkdir -p "$(dirname "$dst")"
        cp "$tmp" "$dst"
        echo "synced: ${CODEX_SRC[$i]} → ${CODEX_DST[$i]} (Codex projection)"
    fi

    rm -f "$tmp"
done

if [[ "$MODE" == "check" && $drift -ne 0 ]]; then
    echo "" >&2
    echo "Run plugins/cix/scripts/sync-skills.sh (no args) to fix." >&2
    exit 1
fi
