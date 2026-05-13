#!/usr/bin/env bash
set -euo pipefail

# cix installer — DEVELOP channel
#
# Installs the latest CLI build from the `develop` branch via the floating
# `cli/develop` GitHub release. This tag is force-updated on every PR merged
# into develop that touches `cli/**`, so re-running this script always pulls
# the freshest build.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install-develop.sh | bash
#   ./install-develop.sh [--bin-dir /usr/local/bin]
#
# For stable releases use `install.sh` instead — that one tracks `cli/v*`
# tags and is the right choice for production. This script is for testing
# unreleased server features against an in-progress CLI.

REPO="dvcdsys/code-index"
BINARY_NAME="cix"
VERSION="cli/develop"
DEFAULT_BIN_DIR="/usr/local/bin"

# ── Parse args ────────────────────────────────────────────────────────────────

BIN_DIR="$DEFAULT_BIN_DIR"

usage() {
    cat <<EOF
Usage: install-develop.sh [--bin-dir <path>]

Installs the latest CLI from the develop channel (floating tag cli/develop).
Always overwrites the existing binary — the dev tag moves on every merge,
so skip-if-installed checks don't apply.

Options:
  --bin-dir <path>  Install directory. Default: ${DEFAULT_BIN_DIR}.
  -h, --help        Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bin-dir) BIN_DIR="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 1 ;;
    esac
done

# ── Banner ────────────────────────────────────────────────────────────────────

cat <<'EOF'
⚠  Installing cix from the DEVELOP channel.

   The cli/develop tag is force-updated on every merge to the develop
   branch. Builds here are unreleased and may break. Use install.sh for
   stable.

EOF

# ── Detect platform ───────────────────────────────────────────────────────────

OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    *)
        echo "Unsupported OS: $OS (supported: macOS, Linux)" >&2
        exit 1
        ;;
esac

case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        echo "Unsupported architecture: $ARCH (supported: x86_64, arm64)" >&2
        exit 1
        ;;
esac

PLATFORM="${OS}-${ARCH}"

echo "Installing cix from develop channel (${PLATFORM})..."

# ── Download ──────────────────────────────────────────────────────────────────

ARCHIVE="${BINARY_NAME}-${PLATFORM}.tar.gz"
# GitHub release download URLs preserve slashes in tag names verbatim,
# so `cli/develop` becomes `.../releases/download/cli/develop/...`.
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${DOWNLOAD_URL}..."
if ! curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${ARCHIVE}"; then
    echo "Failed to download ${DOWNLOAD_URL}" >&2
    echo "The cli/develop release may not have been built yet — wait for the" >&2
    echo "next merge to develop that touches cli/, or check Actions." >&2
    exit 1
fi

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"

# ── Install ───────────────────────────────────────────────────────────────────

BINARY="${TMP_DIR}/${BINARY_NAME}"
if [ ! -f "$BINARY" ]; then
    echo "Binary not found in archive: ${BINARY_NAME}" >&2
    exit 1
fi

chmod +x "$BINARY"

if [ -w "$BIN_DIR" ]; then
    mv "$BINARY" "${BIN_DIR}/${BINARY_NAME}"
else
    echo "Installing to ${BIN_DIR} (requires sudo)..."
    sudo mv "$BINARY" "${BIN_DIR}/${BINARY_NAME}"
fi

# ── Verify ────────────────────────────────────────────────────────────────────

INSTALLED_PATH="${BIN_DIR}/${BINARY_NAME}"
INSTALLED_VERSION_LINE=$("$INSTALLED_PATH" --version 2>/dev/null | head -1 || true)

echo ""
if [ -n "$INSTALLED_VERSION_LINE" ]; then
    echo "✓ ${INSTALLED_VERSION_LINE}"
    echo "  installed at ${INSTALLED_PATH}"
else
    echo "✓ cix (develop) installed at ${INSTALLED_PATH}"
fi

# Warn if a different cix is shadowing this one on PATH.
PATH_BIN=$(command -v "$BINARY_NAME" 2>/dev/null || true)
if [ -n "$PATH_BIN" ] && [ "$PATH_BIN" != "$INSTALLED_PATH" ]; then
    echo ""
    echo "⚠  Another cix is first on PATH: ${PATH_BIN}"
    echo "   Add ${BIN_DIR} earlier in PATH or remove the other binary."
elif [ -z "$PATH_BIN" ]; then
    echo "   Add ${BIN_DIR} to your PATH if needed."
fi

echo ""
echo "Re-run this script any time to pick up the latest develop build."
