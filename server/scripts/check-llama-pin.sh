#!/usr/bin/env bash
# check-llama-pin.sh — warn (loudly, never failing) when the llama.cpp digest
# pinned in Dockerfile.cuda has fallen behind the upstream :server-cuda tag.
#
# We deliberately pin ghcr.io/ggml-org/llama.cpp:server-cuda by digest for
# reproducible CUDA builds (an unpinned tag drifted onto a release that split
# the server logic into libllama-server-impl.so and took prod down — see the
# Dockerfile.cuda comment). The trade-off is staleness: nothing tells us when
# upstream ships a newer build. This script closes that gap — it runs on every
# CUDA image build (and on a weekly schedule) and emits a GitHub annotation +
# step-summary note when a bump is available. It NEVER fails the build by
# default (set FAIL_ON_STALE=true to opt in).
#
# Usage:
#   server/scripts/check-llama-pin.sh [path/to/Dockerfile.cuda]
#
# Requires: docker buildx (for `imagetools inspect`). The upstream image is
# public, so no registry login is needed.
set -euo pipefail

DOCKERFILE="${1:-server/Dockerfile.cuda}"
IMAGE_REF="ghcr.io/ggml-org/llama.cpp:server-cuda"

if [[ ! -f "$DOCKERFILE" ]]; then
    echo "check-llama-pin: $DOCKERFILE not found" >&2
    exit 2
fi

pinned="$(grep -oE 'llama\.cpp:server-cuda@sha256:[0-9a-f]{64}' "$DOCKERFILE" | head -n1 | sed 's/.*@//')"
if [[ -z "$pinned" ]]; then
    echo "check-llama-pin: no pinned @sha256 digest found in $DOCKERFILE" >&2
    echo "::warning title=llama.cpp not pinned::$DOCKERFILE does not pin ghcr.io/ggml-org/llama.cpp:server-cuda by digest — builds are not reproducible."
    exit 0
fi

current="$(docker buildx imagetools inspect "$IMAGE_REF" --format '{{.Manifest.Digest}}' 2>/dev/null || true)"
if [[ -z "$current" ]]; then
    echo "check-llama-pin: could not resolve current digest for $IMAGE_REF (registry/network issue) — skipping" >&2
    exit 0 # transient registry hiccups must not break a build
fi

echo "check-llama-pin: pinned  = $pinned"
echo "check-llama-pin: current = $current"

stale=false
[[ "$pinned" != "$current" ]] && stale=true

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
        echo "pinned=$pinned"
        echo "current=$current"
        echo "stale=$stale"
    } >> "$GITHUB_OUTPUT"
fi

if [[ "$stale" == "false" ]]; then
    echo "check-llama-pin: up to date ✓"
    exit 0
fi

msg="A newer llama.cpp :server-cuda is available — pinned ${pinned} → current ${current}. Bump the digest in ${DOCKERFILE} and run 'make scout-cuda' to validate (the shared-lib layout may have changed)."
echo "::warning title=llama.cpp pin is stale::${msg}"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
        echo "### ⚠️ llama.cpp pin is stale"
        echo ""
        echo "| | digest |"
        echo "|---|---|"
        echo "| pinned (\`${DOCKERFILE}\`) | \`${pinned}\` |"
        echo "| current \`:server-cuda\` | \`${current}\` |"
        echo ""
        echo "Bump the \`FROM ghcr.io/ggml-org/llama.cpp:server-cuda@sha256:…\` digest, then \`make scout-cuda\` to validate the lib layout before promoting."
    } >> "$GITHUB_STEP_SUMMARY"
fi

if [[ "${FAIL_ON_STALE:-false}" == "true" ]]; then
    exit 1
fi
exit 0
