#!/usr/bin/env bash
# build-app.sh — build the Windows tray launcher and, where Inno Setup exists,
# the installer around it.
#
# Cross-compiles: the launcher is pure Go (no cgo — Win32 and WebView2 are
# reached through x/sys/windows and a pure-Go wrapper), so this runs on macOS,
# Linux or Windows (Git Bash). Only the final step, compiling the installer,
# needs Windows and Inno Setup (`ISCC`).
#
#   WIN_VERSION=0.1.0 win/scripts/build-app.sh
#
# Output (win/dist/):
#   stage/cix-launcher.exe, stage/cix.ico   what the installer packages
#   cix-<version>-x64-setup.exe             the installer (only when ISCC is found)
#
# Variables:
#   WIN_VERSION   the app's version, from the win/vX.Y.Z tag (default 0.0.0-dev)
#   OUT_DIR       build directory (default win/dist)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$REPO_ROOT/win/dist}"
WIN_VERSION="${WIN_VERSION:-0.0.0-dev}"
STAGE="$OUT_DIR/stage"

rm -rf "$STAGE"
mkdir -p "$STAGE"

echo "build-app: launcher $WIN_VERSION"
# -H=windowsgui: a tray app must not open a console window.
( cd "$REPO_ROOT/cli" && \
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -H=windowsgui -X main.version=$WIN_VERSION" \
    -o "$STAGE/cix-launcher.exe" ./launcher )
cp "$REPO_ROOT/win/Resources/cix.ico" "$STAGE/cix.ico"

# Find ISCC: PATH first, then the per-user and per-machine default locations.
ISCC="$(command -v ISCC.exe || command -v iscc || true)"
if [[ -z "$ISCC" ]]; then
    for c in "${LOCALAPPDATA:-/nonexistent}/Programs/Inno Setup 6/ISCC.exe" \
             "/c/Program Files (x86)/Inno Setup 6/ISCC.exe" \
             "/c/Program Files/Inno Setup 6/ISCC.exe"; do
        [[ -x "$c" ]] && ISCC="$c" && break
    done
fi

if [[ -z "$ISCC" ]]; then
    echo "build-app: Inno Setup not found — staged the app in $STAGE but did not build the installer." >&2
    echo "           Install it (winget install JRSoftware.InnoSetup) and re-run on Windows." >&2
    exit 0
fi

echo "build-app: installer ($ISCC)"
"$ISCC" "/DAppVersion=$WIN_VERSION" "$(cygpath -w "$REPO_ROOT/win/installer/cix.iss" 2>/dev/null || echo "$REPO_ROOT/win/installer/cix.iss")"
ls -la "$OUT_DIR"/cix-*-x64-setup.exe
