#!/usr/bin/env bash
# fetch-llama-windows.sh — download the pinned llama.cpp Windows CUDA release,
# verify SHA256, and extract llama-server.exe plus every DLL it needs, flat,
# into DEST_DIR.
#
# Selected by fetch-llama.sh when LLAMA_OS=windows (it execs this script), so
# `make fetch-llama OS=windows ARCH=amd64` is the entry point. The inputs are
# the same environment variables fetch-llama.sh documents; the differences:
#
#   LLAMA_OS     — must be "windows"
#   LLAMA_ARCH   — must be "amd64"
#   LLAMA_CUDA   — CUDA toolkit generation of the upstream asset (default 13.3)
#
# Why TWO archives
# ----------------
# Upstream splits the CUDA build in two: `llama-<ver>-bin-win-cuda-<cuda>-x64`
# holds llama-server.exe and the ggml/llama DLLs (including ggml-cuda.dll), and
# `cudart-llama-bin-win-cuda-<cuda>-x64` holds the CUDA runtime redistributables
# (cudart, cublas, cublasLt — ~370 MB zipped, almost all of it cublasLt). Without
# the second archive llama-server.exe dies at load time on any machine that does
# not happen to have a matching CUDA toolkit installed — i.e. every user's.
#
# Why CUDA 13.x and not 12.4
# --------------------------
# Blackwell (RTX 50-series, sm_120) needs CUDA >= 12.8. The 12.4 asset carries
# no sm_120 kernels, so on a Blackwell card (RTX 50 series) it either fails or JIT-compiles PTX on every
# launch. The 13.x build needs a driver that supports CUDA 13 (R580 or newer);
# the launcher checks for that before it offers to start the server.
#
# Same discipline as fetch-llama.sh: record-on-first-run unless LLAMA_STRICT=1,
# a `.llama-version` stamp written last, verified archives cached in
# LLAMA_CACHE_DIR, and no download at all when DEST_DIR is already at the pin.

set -euo pipefail

: "${LLAMA_VERSION:?LLAMA_VERSION is required}"
: "${LLAMA_REPO:=ggml-org/llama.cpp}"
: "${LLAMA_OS:?LLAMA_OS is required}"
: "${LLAMA_ARCH:?LLAMA_ARCH is required}"
: "${DEST_DIR:?DEST_DIR is required}"
: "${CHECKSUMS_FILE:?CHECKSUMS_FILE is required}"
: "${LLAMA_CUDA:=13.3}"
: "${LLAMA_STRICT:=0}"
: "${LLAMA_FORCE:=0}"
: "${LLAMA_CACHE_DIR:=${XDG_CACHE_HOME:-$HOME/.cache}/cix/llama}"

STAMP_FILE="$DEST_DIR/.llama-version"

if [[ "$LLAMA_OS" != "windows" || "$LLAMA_ARCH" != "amd64" ]]; then
    echo "fetch-llama-windows.sh: only windows-amd64 is supported (got $LLAMA_OS-$LLAMA_ARCH)" >&2
    exit 1
fi

ASSETS=(
    "llama-${LLAMA_VERSION}-bin-win-cuda-${LLAMA_CUDA}-x64.zip"
    "cudart-llama-bin-win-cuda-${LLAMA_CUDA}-x64.zip"
)

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

expected_for() {
    [[ -f "$CHECKSUMS_FILE" ]] || return 0
    awk -v a="$1" '$2 == a { print $1 }' "$CHECKSUMS_FILE" || true
}

# Look every pin up BEFORE downloading anything, so strict mode fails in a
# second instead of after pulling ~500 MB it is going to reject.
if [[ "$LLAMA_STRICT" == "1" ]]; then
    for asset in "${ASSETS[@]}"; do
        if [[ -z "$(expected_for "$asset")" ]]; then
            cat >&2 <<EOT
fetch-llama: LLAMA_STRICT=1 and no checksum is recorded for $asset.

  A release build will not record a checksum for itself: the value would be
  computed from the very download it is meant to verify. Pin it first.

  Locally, from the repo root:
      cd server && make fetch-llama OS=windows ARCH=amd64 LLAMA_VERSION=$LLAMA_VERSION
      git add scripts/llama-checksums.txt
      git commit -m "chore(server): pin llama.cpp $LLAMA_VERSION windows checksums"

  Checksum file: $CHECKSUMS_FILE
EOT
            exit 1
        fi
    done
fi

if [[ "$LLAMA_FORCE" != "1" && -f "$DEST_DIR/llama-server.exe" && -f "$STAMP_FILE" ]] \
    && [[ "$(cat "$STAMP_FILE" 2>/dev/null)" == "$LLAMA_VERSION+cuda$LLAMA_CUDA" ]]; then
    echo "fetch-llama: $DEST_DIR already at $LLAMA_VERSION (CUDA $LLAMA_CUDA) — nothing to do (LLAMA_FORCE=1 to refetch)"
    exit 0
fi

TMP_DIR="$(mktemp -d -t cix-fetch-llama-XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT

extract_zip() { # extract_zip <archive> <dir>
    if command -v unzip >/dev/null 2>&1; then
        unzip -q -o "$1" -d "$2"
    elif tar --version 2>/dev/null | grep -qi bsdtar; then
        tar -xf "$1" -C "$2"
    elif command -v python3 >/dev/null 2>&1; then
        python3 -m zipfile -e "$1" "$2"
    else
        echo "fetch-llama: need unzip, bsdtar or python3 to extract $1" >&2
        exit 1
    fi
}

EXTRACT_DIR="$TMP_DIR/extract"
mkdir -p "$EXTRACT_DIR"

for asset in "${ASSETS[@]}"; do
    url="https://github.com/${LLAMA_REPO}/releases/download/${LLAMA_VERSION}/${asset}"
    archive="$TMP_DIR/$asset"
    cached="$LLAMA_CACHE_DIR/$asset"
    expected="$(expected_for "$asset")"
    from_cache=0

    # A cached copy is trusted only against a recorded checksum.
    if [[ -n "$expected" && -f "$cached" ]]; then
        if [[ "$(sha256_of "$cached")" == "$expected" ]]; then
            echo "fetch-llama: using cached $cached"
            cp -p "$cached" "$archive"
            from_cache=1
        else
            echo "fetch-llama: cached $asset failed checksum — discarding and refetching" >&2
            rm -f "$cached"
        fi
    fi

    if [[ "$from_cache" != "1" ]]; then
        echo "fetch-llama: downloading $url"
        curl --fail --location --show-error --silent --output "$archive" "$url"
    fi

    observed="$(sha256_of "$archive")"
    if [[ -z "$expected" ]]; then
        echo "fetch-llama: first-run — recording checksum for $asset → $observed"
        echo "fetch-llama: COMMIT the updated $(basename "$CHECKSUMS_FILE") file so subsequent builds are reproducible."
        mkdir -p "$(dirname "$CHECKSUMS_FILE")"
        printf '%s  %s\n' "$observed" "$asset" >> "$CHECKSUMS_FILE"
    elif [[ "$expected" != "$observed" ]]; then
        echo "fetch-llama: SHA256 mismatch for $asset" >&2
        echo "  expected: $expected" >&2
        echo "  observed: $observed" >&2
        exit 1
    else
        echo "fetch-llama: SHA256 ok ($asset)"
    fi

    if [[ "$from_cache" != "1" ]]; then
        if mkdir -p "$LLAMA_CACHE_DIR" 2>/dev/null; then
            if cp -p "$archive" "$cached.$$" 2>/dev/null; then
                mv -f "$cached.$$" "$cached" 2>/dev/null || rm -f "$cached.$$"
            fi
        fi
    fi

    extract_zip "$archive" "$EXTRACT_DIR"
    rm -f "$archive"   # a ~400 MB archive per asset: do not hold both at once
done

# Both archives are flat (no inner directory). Tolerate one anyway, so a future
# upstream layout change fails the sanity check below rather than here.
SRC="$EXTRACT_DIR"
if [[ ! -f "$SRC/llama-server.exe" ]]; then
    inner="$(find "$EXTRACT_DIR" -name llama-server.exe -print -quit)"
    [[ -n "$inner" ]] && SRC="$(dirname "$inner")"
fi

mkdir -p "$DEST_DIR"
# The stamp first, then the old contents: from here until the final write
# DEST_DIR is a half-built directory that must not be mistaken for a complete one.
rm -f "$STAMP_FILE"
find "$DEST_DIR" -mindepth 1 -maxdepth 1 -type f -exec rm -f {} +

# llama-server.exe plus the WHOLE DLL set rather than a hand-maintained list —
# the same lesson fetch-llama.sh records for the macOS dylibs: upstream moves
# each tool's logic into its own llama-<tool>-impl.dll from time to time, and a
# fixed list yields a bundle that dies at load time. The unrelated tools'
# executables (llama-cli, llama-bench, llama-quantize, …) are dropped.
cp -p "$SRC/llama-server.exe" "$DEST_DIR/"
for f in "$SRC"/*.dll; do
    [[ -e "$f" ]] || continue
    cp -p "$f" "$DEST_DIR/"
done

# The CUDA runtime is what makes this the CUDA build at all; a bundle without
# these runs on nothing but the machine that built it.
for need in llama-server.exe ggml-cuda.dll cudart64_13.dll cublas64_13.dll cublasLt64_13.dll; do
    if [[ ! -s "$DEST_DIR/$need" ]]; then
        echo "fetch-llama: $need is missing from $DEST_DIR — the upstream asset layout or CUDA generation may have changed" >&2
        exit 1
    fi
done

# Last thing written, once every check above has passed. Carries the CUDA
# generation as well as the llama version: bumping either must refetch.
printf '%s\n' "$LLAMA_VERSION+cuda$LLAMA_CUDA" > "$STAMP_FILE"

echo "fetch-llama: wrote $(find "$DEST_DIR" -maxdepth 1 -type f | wc -l | tr -d ' ') files to $DEST_DIR"
