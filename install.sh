#!/usr/bin/env bash
set -euo pipefail

# cix installer
# Usage: curl -fsSL https://raw.githubusercontent.com/<owner>/cix/main/install.sh | bash
#   or:  ./install.sh [--version v1.0.0] [--bin-dir /usr/local/bin]

REPO="<owner>/cix"
BINARY_NAME="cix"
DEFAULT_BIN_DIR="/usr/local/bin"

# ── Parse args ────────────────────────────────────────────────────────────────

VERSION=""
BIN_DIR="$DEFAULT_BIN_DIR"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --bin-dir) BIN_DIR="$2"; shift 2 ;;
        *) echo "Unknown argument: $1"; exit 1 ;;
    esac
done

# ── Detect platform ───────────────────────────────────────────────────────────

OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    *)
        echo "Unsupported OS: $OS (supported: macOS, Linux)"
        exit 1
        ;;
esac

case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        echo "Unsupported architecture: $ARCH (supported: x86_64, arm64)"
        exit 1
        ;;
esac

PLATFORM="${OS}-${ARCH}"

# ── Resolve version ───────────────────────────────────────────────────────────

if [ -z "$VERSION" ]; then
    echo "Fetching latest release..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        echo "Failed to fetch latest version. Specify with --version."
        exit 1
    fi
fi

echo "Installing cix ${VERSION} (${PLATFORM})..."

# ── Download ──────────────────────────────────────────────────────────────────

ARCHIVE="${BINARY_NAME}-${PLATFORM}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${DOWNLOAD_URL}..."
curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${ARCHIVE}"

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"

# ── Install ───────────────────────────────────────────────────────────────────

BINARY="${TMP_DIR}/${BINARY_NAME}"
if [ ! -f "$BINARY" ]; then
    echo "Binary not found in archive: ${BINARY_NAME}"
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

if command -v "$BINARY_NAME" &>/dev/null; then
    INSTALLED_VERSION=$("$BINARY_NAME" --version 2>&1 | head -1)
    echo ""
    echo "✓ cix installed: ${INSTALLED_VERSION}"
    echo "  Location: $(command -v $BINARY_NAME)"
    echo ""
    echo "Next steps:"
    echo "  cix config set api.url http://localhost:21847"
    echo "  cix config set api.key <your-api-key>"
    echo "  cix init /path/to/your/project"
else
    echo ""
    echo "✓ cix installed to ${BIN_DIR}/${BINARY_NAME}"
    echo "  Add ${BIN_DIR} to your PATH if needed."
fi