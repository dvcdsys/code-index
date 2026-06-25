#!/usr/bin/env bash
# Build a .mcpb desktop-extension bundle for cix.
#
# A .mcpb (MCP Bundle, formerly DXT) is a ZIP archive containing manifest.json
# at its root plus the bundled `cix` binary. Installing it in Claude Desktop /
# Cowork (Customize > Connectors > install extension) registers cix's semantic
# search as MCP tools, with the API key stored in the OS keychain.
#
# The binary is platform-specific, so one bundle is produced per GOOS/GOARCH.
# Defaults to the host platform; override with GOOS/GOARCH for cross-builds.
#
# Usage:
#   ./build.sh                              # host platform, version dev
#   VERSION=cli/v0.6.0 ./build.sh           # stamp a version
#   GOOS=darwin GOARCH=arm64 ./build.sh     # explicit target
#
# Output: dist/cix-<os>-<arch>.mcpb (relative to the cli/ module root)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

VERSION="${VERSION:-dev}"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# MCPB platform names differ from GOOS for Windows.
case "$GOOS" in
  darwin)  MCPB_PLATFORM="darwin" ; BIN_NAME="cix" ;;
  linux)   MCPB_PLATFORM="linux"  ; BIN_NAME="cix" ;;
  windows) MCPB_PLATFORM="win32"  ; BIN_NAME="cix.exe" ;;
  *) echo "unsupported GOOS: $GOOS" >&2 ; exit 1 ;;
esac

DIST_DIR="$CLI_DIR/dist"
STAGE="$DIST_DIR/mcpb-$GOOS-$GOARCH"
OUT="$DIST_DIR/cix-$GOOS-$GOARCH.mcpb"

echo "Building cix ($GOOS/$GOARCH, version $VERSION)..."
rm -rf "$STAGE" "$OUT"
mkdir -p "$STAGE"

GOOS="$GOOS" GOARCH="$GOARCH" go build \
  -ldflags="-X 'github.com/anthropics/code-index/cli/cmd.Version=$VERSION'" \
  -o "$STAGE/$BIN_NAME" "$CLI_DIR"

echo "Staging manifest (version=$VERSION, platform=$MCPB_PLATFORM, bin=$BIN_NAME)..."
python3 - "$SCRIPT_DIR/manifest.json" "$STAGE/manifest.json" \
         "$VERSION" "$MCPB_PLATFORM" "$BIN_NAME" <<'PY'
import json, sys
src, dst, version, platform, binname = sys.argv[1:6]
with open(src) as f:
    m = json.load(f)
m["version"] = version.replace("cli/v", "").replace("v", "") or "0.0.0"
# Pin this single-platform bundle so the host won't offer it on a mismatched OS.
m.setdefault("compatibility", {})["platforms"] = [platform]
# entry_point / command must point at the actual binary file name.
m["server"]["entry_point"] = binname
m["server"]["mcp_config"]["command"] = "${__dirname}/" + binname
with open(dst, "w") as f:
    json.dump(m, f, indent=2)
    f.write("\n")
PY

echo "Packing $OUT..."
( cd "$STAGE" && zip -q -X -r "$OUT" manifest.json "$BIN_NAME" )
rm -rf "$STAGE"

echo ""
echo "Built: $OUT"
echo "Install: Claude Desktop > Customize > Connectors > '+' > install this .mcpb file."
echo "Then set the cix server URL and API key in the extension's settings"
echo "(or leave blank to use ~/.cix/config.yaml)."
