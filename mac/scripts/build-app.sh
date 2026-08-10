#!/usr/bin/env bash
# build-app.sh — assemble mac/dist/cix.app from the three Go binaries plus the
# bundled llama-server.
#
# Usage:  mac/scripts/build-app.sh
#
# Environment (all optional; CI sets the versions explicitly):
#   MAC_VERSION        version of the .app itself, from the mac/vX.Y.Z tag
#   SERVER_VERSION     stamped into cix-server   (default: nearest server/v* tag)
#   CLI_VERSION        stamped into cix          (default: nearest cli/v* tag)
#   OUT_DIR            build directory           (default: mac/dist)
#   SKIP_SERVER_BUILD  "1" reuses an existing server/dist bundle
#   SKIP_SIGN          "1" skips codesign (leaves an unrunnable bundle; debug only)
#
# Versions are passed in rather than derived here because `git describe` cannot
# be trusted on this repo: the tag streams interleave, and server/v0.12.8 sits on
# a commit reachable from no branch. A release must state what it is building.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

OUT_DIR="${OUT_DIR:-$REPO_ROOT/mac/dist}"
APP="$OUT_DIR/cix.app"
SERVER_BUNDLE="$REPO_ROOT/server/dist/cix-darwin-arm64"

# --- host check -------------------------------------------------------------
# Upstream llama.cpp publishes one macOS asset, macos-arm64, and
# server/scripts/fetch-llama.sh hard-refuses anything else. Building the Go
# binaries for x86_64 and pairing them with an arm64 llama-server produces a
# bundle that assembles cleanly, signs cleanly, and dies at first embedding —
# so refuse here instead, where the message can say why.
if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "build-app: macOS only (got $(uname -s))" >&2
    exit 1
fi
if [[ "$(uname -m)" != "arm64" ]]; then
    echo "build-app: Apple Silicon only — upstream llama.cpp ships no macOS x86_64 asset (got $(uname -m))" >&2
    exit 1
fi

# --- versions ---------------------------------------------------------------
describe_or_dev() {
    local pattern="$1" prefix="$2" v
    v="$(git describe --tags --match "$pattern" 2>/dev/null | sed "s|^$prefix||")" || true
    printf '%s' "${v:-0.0.0-dev}"
}

MAC_VERSION="${MAC_VERSION:-dev}"
SERVER_VERSION="${SERVER_VERSION:-$(describe_or_dev 'server/v*' 'server/v')}"
CLI_VERSION="${CLI_VERSION:-$(describe_or_dev 'cli/v*' 'cli/v')}"
LLAMA_VERSION="$(sed -n 's/^LLAMA_VERSION[[:space:]]*?=[[:space:]]*//p' server/Makefile | head -n1)"
if [[ -z "$LLAMA_VERSION" ]]; then
    echo "build-app: could not read LLAMA_VERSION from server/Makefile" >&2
    exit 1
fi

echo "build-app: app=$MAC_VERSION server=$SERVER_VERSION cli=$CLI_VERSION llama=$LLAMA_VERSION"

# --- 1. server + bundled llama ---------------------------------------------
# LLAMA_STRICT=1: a release must not accept an unpinned upstream asset. See the
# strict-mode notes in server/scripts/fetch-llama.sh.
if [[ "${SKIP_SERVER_BUILD:-0}" == "1" ]]; then
    echo "build-app: SKIP_SERVER_BUILD=1 — reusing $SERVER_BUNDLE"
    [[ -x "$SERVER_BUNDLE/cix-server" ]] || { echo "build-app: no prebuilt server at $SERVER_BUNDLE" >&2; exit 1; }
else
    # `make bundle` → `build` → `dashboard-build` runs npm scripts but never
    # installs. On a clean checkout (which is every CI run) that surfaces as a
    # missing-binary error from npm rather than "you need to install deps".
    if [[ ! -d server/dashboard/node_modules ]]; then
        echo "build-app: installing dashboard dependencies"
        make -C server dashboard-deps
    fi
    make -C server bundle SERVER_VERSION="$SERVER_VERSION" LLAMA_STRICT=1
fi

# --- 2. cix CLI -------------------------------------------------------------
# -w without -s: govulncheck -mode=binary needs the Go symbol table. Same
# reasoning, and same flags, as .github/workflows/release-cli.yml.
echo "build-app: building cix CLI"
(cd cli && go build \
    -trimpath \
    -ldflags "-w -X 'github.com/dvcdsys/code-index/cli/cmd.Version=${CLI_VERSION}'" \
    -o "$OUT_DIR/stage/cix" .)

# --- 3. launcher ------------------------------------------------------------
echo "build-app: building cix-launcher"
(cd cli && go build \
    -trimpath \
    -ldflags "-w -X 'main.version=${MAC_VERSION}'" \
    -o "$OUT_DIR/stage/cix-launcher" ./launcher)

# --- 4. assemble ------------------------------------------------------------
# Rebuild the bundle from scratch. `cp` merges into an existing tree, so an
# incremental assembly accumulates artefacts from every previous build — the
# exact failure server/Makefile's `bundle` target had to fix for llama/.
echo "build-app: assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# Everything executable lives in Contents/MacOS, including llama/. Two reasons:
# codesign --verify --strict rejects executable code under Resources/, and
# cix-server resolves llama-server at filepath.Dir(os.Executable())/llama — so
# keeping them siblings means CIX_LLAMA_BIN_DIR never has to be set.
cp "$SERVER_BUNDLE/cix-server" "$APP/Contents/MacOS/cix-server"
cp -R "$SERVER_BUNDLE/llama" "$APP/Contents/MacOS/llama"
cp "$OUT_DIR/stage/cix" "$APP/Contents/MacOS/cix"
cp "$OUT_DIR/stage/cix-launcher" "$APP/Contents/MacOS/cix-launcher"

# The app icon is built from the iconset rather than committed as a binary, so
# the PNGs stay the single source of truth. CFBundleIconFile in Info.plist.in
# names this file without its extension, as macOS expects.
iconutil -c icns mac/Resources/cix.iconset -o "$APP/Contents/Resources/cix.icns"

# Menu-bar glyphs: 1x and 2x of the 18 px status item. These must stay template
# images — pure black plus alpha — because macOS recolours them for dark mode
# and for the pressed state and ignores everything but the alpha channel.
cp mac/Resources/menubar/cixTemplate-18.png "$APP/Contents/Resources/cixTemplate.png"
cp mac/Resources/menubar/cixTemplate-36.png "$APP/Contents/Resources/cixTemplate@2x.png"

sed \
    -e "s|@SHORT_VERSION@|${MAC_VERSION}|g" \
    -e "s|@BUNDLE_VERSION@|${MAC_VERSION}|g" \
    -e "s|@SERVER_VERSION@|${SERVER_VERSION}|g" \
    -e "s|@CLI_VERSION@|${CLI_VERSION}|g" \
    -e "s|@LLAMA_VERSION@|${LLAMA_VERSION}|g" \
    mac/Info.plist.in > "$APP/Contents/Info.plist"

# Catch an unsubstituted token before it ships as a literal @TOKEN@ version.
if grep -q '@[A-Z_]*@' "$APP/Contents/Info.plist"; then
    echo "build-app: unsubstituted token left in Info.plist:" >&2
    grep -n '@[A-Z_]*@' "$APP/Contents/Info.plist" >&2
    exit 1
fi
plutil -lint "$APP/Contents/Info.plist"

# Classic-era metadata. LaunchServices no longer requires PkgInfo, but codesign
# and some archive tooling still expect it beside Info.plist, and it costs 8 bytes.
printf 'APPL????' > "$APP/Contents/PkgInfo"

rm -rf "$OUT_DIR/stage"

# --- 5. sign ----------------------------------------------------------------
if [[ "${SKIP_SIGN:-0}" == "1" ]]; then
    echo "build-app: SKIP_SIGN=1 — bundle is UNSIGNED and will be killed on launch"
else
    mac/scripts/sign-app.sh "$APP"
fi

echo "build-app: ready — $APP"
du -sh "$APP" | sed 's/^/build-app: size /'
