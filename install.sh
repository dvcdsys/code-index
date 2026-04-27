#!/usr/bin/env bash
set -euo pipefail

# cix installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
#   ./install.sh [--version cli/v0.4.0] [--bin-dir /usr/local/bin] [--force]
#
# Re-running upgrades to the latest CLI release. If the requested version
# is already installed the script exits early — pass --force to reinstall
# anyway.
#
# Tag scheme: CLI releases live under `cli/v*`, server releases under
# `server/v*`. Bare `v*` tags are the historical pre-split CLI line and
# are used as a fallback only when no `cli/v*` release exists yet.

REPO="dvcdsys/code-index"
BINARY_NAME="cix"
DEFAULT_BIN_DIR="/usr/local/bin"

# ── Parse args ────────────────────────────────────────────────────────────────

VERSION=""
BIN_DIR="$DEFAULT_BIN_DIR"
FORCE=0

usage() {
    cat <<EOF
Usage: install.sh [--version <tag>] [--bin-dir <path>] [--force]

Options:
  --version <tag>   Install a specific tag (e.g. cli/v0.4.0). Default: latest cli/v*.
  --bin-dir <path>  Install directory. Default: ${DEFAULT_BIN_DIR}.
  --force           Reinstall even if the same version is already present.
  -h, --help        Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --bin-dir) BIN_DIR="$2"; shift 2 ;;
        --force)   FORCE=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 1 ;;
    esac
done

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

# ── Resolve version ───────────────────────────────────────────────────────────

if [ -z "$VERSION" ]; then
    echo "Fetching latest CLI release..."
    RELEASES_JSON=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases?per_page=30")

    # 1) Prefer cli/v* tags (post-split scheme).
    VERSION=$(printf '%s' "$RELEASES_JSON" \
        | grep '"tag_name"' \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/' \
        | grep '^cli/v' \
        | head -1 || true)

    # 2) Fall back to bare v* (historical CLI line), explicitly excluding server/v*.
    if [ -z "$VERSION" ]; then
        VERSION=$(printf '%s' "$RELEASES_JSON" \
            | grep '"tag_name"' \
            | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/' \
            | grep '^v' \
            | head -1 || true)
    fi

    if [ -z "$VERSION" ]; then
        echo "Failed to find a CLI release. Specify with --version <tag>." >&2
        exit 1
    fi
fi

# Strip cli/ prefix for display and binary `--version` comparison.
CLEAN_VERSION="${VERSION#cli/}"

# ── Skip if already installed at the same version ─────────────────────────────

if [ "$FORCE" -ne 1 ] && command -v "$BINARY_NAME" >/dev/null 2>&1; then
    # New binaries print "cix v0.4.0 darwin/arm64";
    # historical binaries print "cix version v0.2.7".
    # Pick the first v-prefixed semver-looking token.
    CURRENT=$("$BINARY_NAME" --version 2>/dev/null \
        | head -1 \
        | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*' \
        | head -1 || true)
    if [ -n "$CURRENT" ] && [ "$CURRENT" = "$CLEAN_VERSION" ]; then
        echo "✓ cix ${CLEAN_VERSION} already installed at $(command -v "$BINARY_NAME")"
        echo "  Pass --force to reinstall."
        exit 0
    fi
    if [ -n "$CURRENT" ]; then
        echo "Upgrading cix ${CURRENT} → ${CLEAN_VERSION}..."
    else
        echo "Installing cix ${CLEAN_VERSION} (existing binary version unknown)..."
    fi
else
    echo "Installing cix ${CLEAN_VERSION} (${PLATFORM})..."
fi

# ── Download ──────────────────────────────────────────────────────────────────

ARCHIVE="${BINARY_NAME}-${PLATFORM}.tar.gz"
# GitHub release download URLs preserve slashes in tag names verbatim,
# so `cli/v0.4.0` becomes `.../releases/download/cli/v0.4.0/...`.
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${DOWNLOAD_URL}..."
if ! curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${ARCHIVE}"; then
    echo "Failed to download ${DOWNLOAD_URL}" >&2
    echo "Check that the release exists and contains ${ARCHIVE}." >&2
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
INSTALLED_VERSION=$("$INSTALLED_PATH" --version 2>/dev/null \
    | head -1 \
    | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*' \
    | head -1 || true)

echo ""
if [ -n "$INSTALLED_VERSION" ]; then
    echo "✓ cix ${INSTALLED_VERSION} installed at ${INSTALLED_PATH}"
else
    echo "✓ cix ${CLEAN_VERSION} installed at ${INSTALLED_PATH}"
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
echo "Next steps:"
echo "  cix config set api.url http://localhost:21847"
echo "  cix config set api.key <your-api-key>"
echo "  cix init /path/to/your/project"
