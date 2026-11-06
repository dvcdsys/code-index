#!/usr/bin/env bash
# build-runtime.sh — package the Windows cix runtime: the server, the CLI, and
# the CUDA llama-server they need, as one zip.
#
# Usage:  win/scripts/build-runtime.sh
#
# Runs anywhere bash and Go do — macOS, Linux (CI), Git Bash on Windows. The Go
# binaries are cross-compiled (CGO is not used) and llama.cpp is downloaded, so
# nothing here needs a Windows host or a GPU. Proving the result actually runs is
# win/scripts/verify-runtime.ps1's job, and that does need Windows.
#
# Environment (all optional; CI sets the versions explicitly):
#   SERVER_VERSION        the runtime's version    (default: nearest server/v* tag)
#   CLI_VERSION           stamped into cix.exe     (default: nearest cli/v* tag)
#   OUT_DIR               build directory          (default: win/dist)
#   SKIP_DASHBOARD_BUILD  "1" reuses the dashboard already in
#                         server/internal/httpapi/dashboard/dist
#   LLAMA_STRICT          "0" lets fetch-llama record unpinned checksums
#                         (default "1": a release must not trust its own download)
#
# Same shape as mac/scripts/build-runtime.sh, deliberately: the launcher installs
# both through one code path, so the runtime is
#
#   cix-runtime-<server version>/
#     cix-server.exe  cix.exe  runtime.json
#     llama/          llama-server.exe + DLLs (ggml, CUDA runtime, cuBLAS)
#
# and ships from the server's own tag stream (server/v*), next to the Docker
# images — a Windows install and a container on the same version are the same
# server.
#
# Why llama/ is a sibling of cix-server.exe: the server resolves its sidecar at
# <dir of its own executable>\llama, so shipping them together means
# CIX_LLAMA_BIN_DIR never has to be set.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Version derivation is shared with the mac pipeline so the two cannot drift.
# shellcheck source=mac/scripts/common.sh
source "$REPO_ROOT/mac/scripts/common.sh"
cd "$REPO_ROOT"

OUT_DIR="${OUT_DIR:-$REPO_ROOT/win/dist}"
SERVER_VERSION="${SERVER_VERSION:-$(describe_or_dev 'server/v*' 'server/v')}"
CLI_VERSION="${CLI_VERSION:-$(describe_or_dev 'cli/v*' 'cli/v')}"
LLAMA_VERSION="$(read_llama_version "$REPO_ROOT")"
LLAMA_CUDA="${LLAMA_CUDA:-13.3}"
LLAMA_STRICT="${LLAMA_STRICT:-1}"
LLAMA_STAGE="$REPO_ROOT/server/dist/llama-windows-amd64"

# Every value below is interpolated into JSON and into a file name. Reject
# anything outside the set `git describe` and the Makefile pin can legitimately
# produce, rather than lint the result afterwards.
for v in "$SERVER_VERSION" "$CLI_VERSION" "$LLAMA_VERSION" "$LLAMA_CUDA"; do
    if [[ ! "$v" =~ ^[A-Za-z0-9._+-]+$ ]]; then
        echo "build-runtime: version string is not safe to embed: '$v'" >&2
        exit 1
    fi
done

RUNTIME_NAME="cix-runtime-$SERVER_VERSION"
ZIP_NAME="$RUNTIME_NAME-windows-amd64.zip"
STAGE="$OUT_DIR/runtime/$RUNTIME_NAME"
ZIP="$OUT_DIR/$ZIP_NAME"

echo "build-runtime: server=$SERVER_VERSION cli=$CLI_VERSION llama=$LLAMA_VERSION cuda=$LLAMA_CUDA"

# --- 1. llama.cpp (CUDA) ----------------------------------------------------
# LLAMA_STRICT=1: a release must not accept an unpinned upstream asset. See the
# notes in server/scripts/fetch-llama-windows.sh.
make -C server fetch-llama OS=windows ARCH=amd64 LLAMA_CUDA="$LLAMA_CUDA" LLAMA_STRICT="$LLAMA_STRICT"
[[ -f "$LLAMA_STAGE/llama-server.exe" ]] || { echo "build-runtime: fetch-llama staged nothing at $LLAMA_STAGE" >&2; exit 1; }

# --- 2. dashboard (embedded into the server by go:embed) ---------------------
if [[ "${SKIP_DASHBOARD_BUILD:-0}" == "1" ]]; then
    echo "build-runtime: SKIP_DASHBOARD_BUILD=1 — reusing the embedded dashboard"
else
    if [[ ! -d server/dashboard/node_modules ]]; then
        echo "build-runtime: installing dashboard dependencies"
        make -C server dashboard-deps
    fi
    make -C server dashboard-build
fi
if [[ ! -f server/internal/httpapi/dashboard/dist/index.html ]]; then
    echo "build-runtime: the dashboard is not built (no dist/index.html) — the server would embed an empty UI" >&2
    exit 1
fi

# --- 3. binaries -------------------------------------------------------------
# Build into a scratch dir first; assembly below starts from nothing.
echo "build-runtime: building cix-server.exe and cix.exe (windows/amd64)"
BIN="$OUT_DIR/bin"
rm -rf "$BIN"
mkdir -p "$BIN"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
(cd server && go build -trimpath \
    -ldflags "-w -X main.version=${SERVER_VERSION}" \
    -o "$BIN/cix-server.exe" ./cmd/cix-server)
# -w without -s: govulncheck -mode=binary needs the Go symbol table. Same flags
# as .github/workflows/release-cli.yml.
(cd cli && go build -trimpath \
    -ldflags "-w -X 'github.com/dvcdsys/code-index/cli/cmd.Version=${CLI_VERSION}'" \
    -o "$BIN/cix.exe" .)
unset GOOS GOARCH CGO_ENABLED

# --- 4. assemble ------------------------------------------------------------
# From scratch every time: `cp` merges, so an incremental assembly accumulates
# artefacts of every previous build.
echo "build-runtime: assembling $STAGE"
rm -rf "$OUT_DIR/runtime"
mkdir -p "$STAGE/llama"
cp "$BIN/cix-server.exe" "$BIN/cix.exe" "$STAGE/"
# The stamp is a build-time bookkeeping file, not part of the payload.
for f in "$LLAMA_STAGE"/*; do
    [[ "$(basename "$f")" == .* ]] && continue
    cp "$f" "$STAGE/llama/"
done
rm -rf "$BIN"

cat > "$STAGE/runtime.json" <<JSON
{
  "server_version": "$SERVER_VERSION",
  "cli_version": "$CLI_VERSION",
  "llama_version": "$LLAMA_VERSION",
  "platform": "windows-amd64",
  "cuda": "$LLAMA_CUDA"
}
JSON

# --- 5. zip -----------------------------------------------------------------
# Forward-slash member names, no symlinks, no extra attributes. Info-ZIP's
# `zip -X` does all of that; CI and macOS have it. Git Bash does not, hence the
# fallbacks — each of them also writes '/' separators.
make_zip() { # make_zip <out.zip> <parent dir> <member dir>
    local out="$1" parent="$2" member="$3"
    rm -f "$out"
    if command -v zip >/dev/null 2>&1; then
        (cd "$parent" && zip -q -X -r "$out" "$member")
    elif command -v python3 >/dev/null 2>&1; then
        python3 - "$out" "$parent" "$member" <<'PY'
import os, sys, zipfile
out, parent, member = sys.argv[1:4]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk(os.path.join(parent, member)):
        dirs.sort()
        for f in sorted(files):
            full = os.path.join(root, f)
            z.write(full, os.path.relpath(full, parent).replace(os.sep, "/"))
PY
    elif tar --version 2>/dev/null | grep -qi bsdtar; then
        tar -a -cf "$out" -C "$parent" "$member"
    else
        echo "build-runtime: need zip, python3 or bsdtar to write the archive" >&2
        exit 1
    fi
}

echo "build-runtime: writing $ZIP"
mkdir -p "$OUT_DIR"
make_zip "$ZIP" "$OUT_DIR/runtime" "$RUNTIME_NAME"

# --- 6. checksum ------------------------------------------------------------
# Same format release-server.yml uses for the other assets (`sha256sum ./x`), so
# the launcher's checksum parser needs no second dialect.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}
(cd "$OUT_DIR" && sha256_of "./$ZIP_NAME" > checksums.txt)

# --- 7. structural check ----------------------------------------------------
# What can be proved without a Windows host. Whether the DLLs load and the GPU
# is used is win/scripts/verify-runtime.ps1 — run it on Windows before shipping.
if [[ "${SKIP_VERIFY:-0}" == "1" ]]; then
    echo "build-runtime: SKIP_VERIFY=1 — archive NOT checked"
else
    echo "build-runtime: checking the archive"
    CHECK_DIR="$(mktemp -d)"
    trap 'rm -rf "$CHECK_DIR"' EXIT

    if command -v unzip >/dev/null 2>&1; then
        unzip -q "$ZIP" -d "$CHECK_DIR"
        # `unzip -Z1` lists member names verbatim; a backslash would be a
        # Windows-tool artefact that breaks extraction on other platforms.
        if unzip -Z1 "$ZIP" | grep -q '\\'; then
            echo "build-runtime: archive has backslash member names" >&2
            exit 1
        fi
    else
        python3 -m zipfile -e "$ZIP" "$CHECK_DIR"
    fi
    X="$CHECK_DIR/$RUNTIME_NAME"

    top="$(cd "$CHECK_DIR" && ls -A)"
    [[ "$top" == "$RUNTIME_NAME" ]] || { echo "build-runtime: archive must hold exactly one top directory '$RUNTIME_NAME', found: $top" >&2; exit 1; }

    for f in cix-server.exe cix.exe runtime.json llama/llama-server.exe llama/ggml-cuda.dll \
             llama/cudart64_13.dll llama/cublas64_13.dll llama/cublasLt64_13.dll; do
        [[ -s "$X/$f" ]] || { echo "build-runtime: $f is missing or empty in the archive" >&2; exit 1; }
    done
    if [[ -n "$(find "$X" -type l)" ]]; then
        echo "build-runtime: the archive contains symlinks" >&2
        exit 1
    fi
    if find "$X" -name '._*' | grep -q .; then
        echo "build-runtime: AppleDouble members leaked into the archive" >&2
        exit 1
    fi

    # PE signature ("MZ"), so a Mach-O/ELF cross-build mistake cannot ship.
    for f in cix-server.exe cix.exe llama/llama-server.exe; do
        [[ "$(head -c2 "$X/$f")" == "MZ" ]] || { echo "build-runtime: $f is not a Windows executable" >&2; exit 1; }
    done

    # The label must match the payload. ldflags -X stores the string in the
    # binary, so a stale build shows up as a version that is simply not there.
    if ! grep -aqF "$SERVER_VERSION" "$X/cix-server.exe"; then
        echo "build-runtime: cix-server.exe does not contain version '$SERVER_VERSION' — the payload does not match its label" >&2
        exit 1
    fi
    if ! grep -aqF "$CLI_VERSION" "$X/cix.exe"; then
        echo "build-runtime: cix.exe does not contain version '$CLI_VERSION' — the payload does not match its label" >&2
        exit 1
    fi
    echo "build-runtime: structure ok (run win/scripts/verify-runtime.ps1 on Windows for the behavioural check)"
fi

echo "build-runtime: ready — $ZIP"
du -sh "$ZIP" | sed 's/^/build-runtime: size /'
cat "$OUT_DIR/checksums.txt" | sed 's/^/build-runtime: sha256 /'
