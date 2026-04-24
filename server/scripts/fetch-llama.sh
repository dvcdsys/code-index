#!/usr/bin/env bash
# fetch-llama.sh — download a pinned llama.cpp release, verify SHA256, and
# extract only the files cix-server ships with (llama-server + required dylibs).
#
# Inputs come from the Makefile as environment variables:
#   LLAMA_VERSION   — e.g. "b8914"
#   LLAMA_REPO      — e.g. "ggml-org/llama.cpp"
#   LLAMA_OS        — "darwin" (Phase 3 only supports darwin)
#   LLAMA_ARCH      — "arm64"  (Phase 3 only supports arm64)
#   DEST_DIR        — target directory for the slimmed binary set
#   CHECKSUMS_FILE  — path to scripts/llama-checksums.txt
#
# First-run bootstrap flow
# ------------------------
# The first time a contributor runs this on a new LLAMA_VERSION the checksum
# for the asset is unknown. Rather than fail, we compute the SHA256 after the
# download and APPEND it to CHECKSUMS_FILE, printing a very visible message.
# The expectation is that the contributor then commits that checksum file
# update in the same PR that bumps LLAMA_VERSION — downstream CI fails hard
# if the asset's SHA256 does not match an existing line.
#
# Every subsequent run on the same LLAMA_VERSION uses the recorded checksum
# as the authoritative verifier; mismatches fail.

set -euo pipefail

: "${LLAMA_VERSION:?LLAMA_VERSION is required}"
: "${LLAMA_REPO:=ggml-org/llama.cpp}"
: "${LLAMA_OS:?LLAMA_OS is required}"
: "${LLAMA_ARCH:?LLAMA_ARCH is required}"
: "${DEST_DIR:?DEST_DIR is required}"
: "${CHECKSUMS_FILE:?CHECKSUMS_FILE is required}"

if [[ "$LLAMA_OS" != "darwin" || "$LLAMA_ARCH" != "arm64" ]]; then
    echo "fetch-llama.sh: only darwin-arm64 is supported in Phase 3 (got $LLAMA_OS-$LLAMA_ARCH)" >&2
    exit 1
fi

# Asset naming — verified against the ggml-org/llama.cpp b8914 release.
# Example: llama-b8914-bin-macos-arm64.tar.gz
ASSET="llama-${LLAMA_VERSION}-bin-macos-arm64.tar.gz"
URL="https://github.com/${LLAMA_REPO}/releases/download/${LLAMA_VERSION}/${ASSET}"

TMP_DIR="$(mktemp -d -t cix-fetch-llama-XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT
ARCHIVE="$TMP_DIR/$ASSET"

echo "fetch-llama: downloading $URL"
curl --fail --location --show-error --silent --output "$ARCHIVE" "$URL"

# SHA256 verify or record-on-first-run.
OBSERVED_SHA=$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')
EXPECTED_SHA=""
if [[ -f "$CHECKSUMS_FILE" ]]; then
    EXPECTED_SHA=$(awk -v a="$ASSET" '$2 == a { print $1 }' "$CHECKSUMS_FILE" || true)
fi

if [[ -z "$EXPECTED_SHA" ]]; then
    echo "fetch-llama: first-run — recording checksum for $ASSET → $OBSERVED_SHA"
    echo "fetch-llama: COMMIT the updated $(basename "$CHECKSUMS_FILE") file so subsequent builds are reproducible."
    mkdir -p "$(dirname "$CHECKSUMS_FILE")"
    printf '%s  %s\n' "$OBSERVED_SHA" "$ASSET" >> "$CHECKSUMS_FILE"
else
    if [[ "$EXPECTED_SHA" != "$OBSERVED_SHA" ]]; then
        echo "fetch-llama: SHA256 mismatch for $ASSET" >&2
        echo "  expected: $EXPECTED_SHA" >&2
        echo "  observed: $OBSERVED_SHA" >&2
        exit 1
    fi
    echo "fetch-llama: SHA256 ok ($OBSERVED_SHA)"
fi

# Extract into a scratch dir, then pull only the files we ship.
EXTRACT_DIR="$TMP_DIR/extract"
mkdir -p "$EXTRACT_DIR"
tar -xzf "$ARCHIVE" -C "$EXTRACT_DIR"

# Upstream layout is "llama-<version>/<file>". Find the inner dir
# regardless of the version pin so this script survives future bumps.
INNER_DIR=$(find "$EXTRACT_DIR" -mindepth 1 -maxdepth 1 -type d | head -n 1)
if [[ -z "$INNER_DIR" ]]; then
    echo "fetch-llama: archive layout unexpected; no inner directory under $EXTRACT_DIR" >&2
    exit 1
fi

mkdir -p "$DEST_DIR"
# Clean out any previous fetch — stale dylibs could get picked up by DYLD.
rm -f "$DEST_DIR"/* 2>/dev/null || true

# Files we ship. llama-server is the only binary we need; dylibs are its
# runtime deps. We deliberately drop llama-cli, llama-bench, llama-quantize,
# rpc-server, llama-server's *-debug variants, mtmd-*, etc. to keep the
# bundle lean.
SHIP=(
    "llama-server"
    "libllama.dylib"
    "libllama-common.dylib"
    "libmtmd.dylib"
    "libggml.dylib"
    "libggml-base.dylib"
    "libggml-cpu.dylib"
    "libggml-metal.dylib"
    "libggml-blas.dylib"
    "libggml-rpc.dylib"
)
# Versioned dylib aliases — dyld resolves these via symlink/rpath. Include
# everything that matches the base names so @rpath lookups do not break.
for base in "${SHIP[@]}"; do
    # Copy the bare file if present.
    if [[ -e "$INNER_DIR/$base" ]]; then
        cp -p "$INNER_DIR/$base" "$DEST_DIR/"
    fi
    # Copy any versioned variants (libfoo.0.dylib, libfoo.0.0.1234.dylib, ...)
    # that begin with the same stem. Loose glob: for each dylib name stem we
    # look for "<stem>.*.dylib".
    stem="${base%.dylib}"
    for match in "$INNER_DIR/$stem".*.dylib; do
        [[ -e "$match" ]] || continue
        cp -p "$match" "$DEST_DIR/"
    done
done

# Sanity: llama-server must be present and executable.
if [[ ! -x "$DEST_DIR/llama-server" ]]; then
    echo "fetch-llama: llama-server missing or not executable in $DEST_DIR" >&2
    exit 1
fi

# macOS Gatekeeper quarantine can apply to downloaded binaries even via curl.
# Strip the attribute so end users do not hit a silent kill on first run.
if command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$DEST_DIR" 2>/dev/null || true
fi

echo "fetch-llama: wrote $(ls -1 "$DEST_DIR" | wc -l | tr -d ' ') files to $DEST_DIR"
