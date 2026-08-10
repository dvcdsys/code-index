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
#   LLAMA_STRICT    — "1" to require a pre-recorded checksum (default "0")
#
# First-run bootstrap flow
# ------------------------
# The first time a contributor runs this on a new LLAMA_VERSION the checksum
# for the asset is unknown. Rather than fail, we compute the SHA256 after the
# download and APPEND it to CHECKSUMS_FILE, printing a very visible message.
# The expectation is that the contributor then commits that checksum file
# update in the same PR that bumps LLAMA_VERSION.
#
# Every subsequent run on the same LLAMA_VERSION uses the recorded checksum
# as the authoritative verifier; mismatches fail.
#
# Strict mode (LLAMA_STRICT=1)
# ----------------------------
# Record-on-first-run is the right behaviour for a developer bootstrapping a
# bump, and the wrong behaviour for a build that ships. In a release build a
# missing checksum row means "trust whatever the network just handed us" —
# the recorded value is derived from the download itself, so it verifies
# nothing. Release workflows set LLAMA_STRICT=1 so an unpinned LLAMA_VERSION
# fails the build instead, and the fix is to run `make fetch-llama` locally
# and commit the resulting llama-checksums.txt line.

set -euo pipefail

: "${LLAMA_VERSION:?LLAMA_VERSION is required}"
: "${LLAMA_REPO:=ggml-org/llama.cpp}"
: "${LLAMA_OS:?LLAMA_OS is required}"
: "${LLAMA_ARCH:?LLAMA_ARCH is required}"
: "${DEST_DIR:?DEST_DIR is required}"
: "${CHECKSUMS_FILE:?CHECKSUMS_FILE is required}"
: "${LLAMA_STRICT:=0}"

if [[ "$LLAMA_OS" != "darwin" || "$LLAMA_ARCH" != "arm64" ]]; then
    echo "fetch-llama.sh: only darwin-arm64 is supported in Phase 3 (got $LLAMA_OS-$LLAMA_ARCH)" >&2
    exit 1
fi

# Asset naming — verified against the ggml-org/llama.cpp b8914 release.
# Example: llama-b8914-bin-macos-arm64.tar.gz
ASSET="llama-${LLAMA_VERSION}-bin-macos-arm64.tar.gz"
URL="https://github.com/${LLAMA_REPO}/releases/download/${LLAMA_VERSION}/${ASSET}"

# Look the pin up BEFORE downloading, so strict mode fails in a second rather
# than after pulling ~50 MB it is going to reject anyway.
EXPECTED_SHA=""
if [[ -f "$CHECKSUMS_FILE" ]]; then
    EXPECTED_SHA=$(awk -v a="$ASSET" '$2 == a { print $1 }' "$CHECKSUMS_FILE" || true)
fi

if [[ "$LLAMA_STRICT" == "1" && -z "$EXPECTED_SHA" ]]; then
    cat >&2 <<EOF
fetch-llama: LLAMA_STRICT=1 and no checksum is recorded for $ASSET.

  A release build will not record a checksum for itself: the value would be
  computed from the very download it is meant to verify. Pin it first.

  Locally, from the repo root:
      cd server && make fetch-llama LLAMA_VERSION=$LLAMA_VERSION
      git add scripts/llama-checksums.txt
      git commit -m "chore(server): pin llama.cpp $LLAMA_VERSION checksum"

  Checksum file: $CHECKSUMS_FILE
EOF
    exit 1
fi

TMP_DIR="$(mktemp -d -t cix-fetch-llama-XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT
ARCHIVE="$TMP_DIR/$ASSET"

echo "fetch-llama: downloading $URL"
curl --fail --location --show-error --silent --output "$ARCHIVE" "$URL"

# SHA256 verify, or record-on-first-run (non-strict only — strict mode already
# bailed above when EXPECTED_SHA was empty).
OBSERVED_SHA=$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')

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

# Files we ship: llama-server plus the WHOLE dylib set, not a hand-maintained
# list. Upstream periodically refactors the shared-library layout — b10238
# moved each tool's logic into its own libllama-<tool>-impl.dylib, so the old
# fixed list produced a bundle whose llama-server died at dyld load time with
# "Library not loaded: @rpath/libllama-server-impl.dylib". The same lesson is
# already encoded in Dockerfile/Dockerfile.cuda (`COPY /app/*.so*`). The other
# tools' impl dylibs cost ~1 MB out of a ~52 MB bundle — far cheaper than a
# broken bundle. Standalone binaries (llama-cli, llama-bench, llama-quantize,
# ggml-rpc-server, mtmd-*, …) are still dropped.
cp -p "$INNER_DIR/llama-server" "$DEST_DIR/"
for match in "$INNER_DIR"/*.dylib; do
    [[ -e "$match" ]] || continue
    cp -p "$match" "$DEST_DIR/"
done

# Sanity: llama-server must be present and executable.
if [[ ! -x "$DEST_DIR/llama-server" ]]; then
    echo "fetch-llama: llama-server missing or not executable in $DEST_DIR" >&2
    exit 1
fi

# Sanity: every @rpath dependency of llama-server must have landed in
# DEST_DIR. Without this the breakage only surfaces at runtime, on the
# operator's machine, as a dyld abort — exactly how the b10238 layout change
# was found. Fail the fetch instead.
#
# The check must not pass vacuously. otool exits 0 even when handed a
# non-Mach-O file (it prints "… is not an object file" and returns success),
# and an absent otool would simply feed the loop an empty list — either way
# an empty dependency list reads as "nothing missing" on exactly the broken
# bundle this exists to reject. So: require the tool, require it to succeed,
# and reject output that is not a dependency listing.
if ! command -v otool >/dev/null 2>&1; then
    echo "fetch-llama: otool not found — install the Xcode Command Line Tools (xcode-select --install)" >&2
    exit 1
fi
if ! DEPS="$(otool -L "$DEST_DIR/llama-server" 2>&1)"; then
    echo "fetch-llama: otool -L failed on $DEST_DIR/llama-server:" >&2
    printf '%s\n' "$DEPS" >&2
    exit 1
fi
case "$DEPS" in
    ""|*"is not an object file"*|*"can't open file"*)
        echo "fetch-llama: $DEST_DIR/llama-server is not a Mach-O binary — the upstream asset layout may have changed:" >&2
        printf '%s\n' "$DEPS" >&2
        exit 1
        ;;
esac
missing=()
while read -r dep; do
    [[ -n "$dep" ]] || continue
    [[ -e "$DEST_DIR/$dep" ]] || missing+=("$dep")
done < <(printf '%s\n' "$DEPS" | awk '/@rpath\//{sub(/^.*@rpath\//, "", $1); print $1}')
if [[ ${#missing[@]} -gt 0 ]]; then
    echo "fetch-llama: llama-server has unresolved @rpath dependencies:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    echo "fetch-llama: upstream $LLAMA_VERSION likely changed its library layout — inspect the release archive." >&2
    exit 1
fi

# macOS Gatekeeper quarantine can apply to downloaded binaries even via curl.
# Strip the attribute so end users do not hit a silent kill on first run.
if command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$DEST_DIR" 2>/dev/null || true
fi

echo "fetch-llama: wrote $(ls -1 "$DEST_DIR" | wc -l | tr -d ' ') files to $DEST_DIR"
