#!/usr/bin/env bash
# build-runtime.sh — package the cix runtime: the server, the CLI, and the
# Metal llama-server they need.
#
# Usage:  mac/scripts/build-runtime.sh
#
# Environment (all optional; CI sets the versions explicitly):
#   SERVER_VERSION     the runtime's version    (default: nearest server/v* tag)
#   CLI_VERSION        stamped into cix         (default: nearest cli/v* tag)
#   OUT_DIR            build directory          (default: mac/dist)
#   SKIP_SERVER_BUILD  "1" reuses an existing server/dist bundle
#   SKIP_VERIFY        "1" skips the extract-and-check round trip (not advised)
#
# The runtime IS the server, so it carries the server's version and ships from
# the server's tag stream — the same `server/vX.Y.Z` tag that publishes the
# Docker images, built by the same workflow run. A Mac install and a container
# on the same version are the same server. The app has its own `mac/v*` stream
# and its own version, because it is a different thing that changes for
# different reasons.
#
# Why this is not inside the .app
# -------------------------------
# It used to be. An app carrying all four binaries meant every server update
# replaced a 102 MB application through a swap trampoline that had to quit the
# launcher, move the bundle aside, move the new one in, and reopen itself. The
# runtime is 90% of that weight and nearly all of the churn. Split out, it is a
# payload the launcher installs into ~/.cix/runtime/<version>/ and swaps by
# renaming a symlink, without the app going anywhere.
#
# Why the CLI travels here and not in the .app
# --------------------------------------------
# It speaks a specific server's API, so pinning the two together is the point.
# /usr/local/bin/cix is a symlink into ~/.cix/runtime/current/, which means it
# follows updates without anyone touching /usr/local.
#
# Why llama travels with the server
# ---------------------------------
# cix-server resolves its sidecar at filepath.Dir(os.Executable())/llama. Ship
# them as siblings and CIX_LLAMA_BIN_DIR never has to be set — the same
# invariant server/Makefile's bundle target already relies on. Separating them
# would mean carrying that environment variable forever.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=mac/scripts/common.sh
source "$REPO_ROOT/mac/scripts/common.sh"
cd "$REPO_ROOT"

require_apple_silicon "build-runtime"

OUT_DIR="${OUT_DIR:-$REPO_ROOT/mac/dist}"
SERVER_BUNDLE="$REPO_ROOT/server/dist/cix-darwin-arm64"

SERVER_VERSION="${SERVER_VERSION:-$(describe_or_dev 'server/v*' 'server/v')}"
CLI_VERSION="${CLI_VERSION:-$(describe_or_dev 'cli/v*' 'cli/v')}"
LLAMA_VERSION="$(read_llama_version "$REPO_ROOT")"

# The directory name inside the tarball is also the name the launcher strips on
# extraction, and the version in it becomes the directory under
# ~/.cix/runtime/. Both are part of the format, not cosmetic.
RUNTIME_NAME="cix-runtime-$SERVER_VERSION"
STAGE="$OUT_DIR/runtime/$RUNTIME_NAME"
TARBALL="$OUT_DIR/$RUNTIME_NAME-darwin-arm64.tar.gz"

echo "build-runtime: server=$SERVER_VERSION cli=$CLI_VERSION llama=$LLAMA_VERSION"

# --- 1. server + bundled llama ---------------------------------------------
# LLAMA_STRICT=1: a release must not accept an unpinned upstream asset. See the
# strict-mode notes in server/scripts/fetch-llama.sh.
if [[ "${SKIP_SERVER_BUILD:-0}" == "1" ]]; then
    echo "build-runtime: SKIP_SERVER_BUILD=1 — reusing $SERVER_BUNDLE"
    [[ -x "$SERVER_BUNDLE/cix-server" ]] || { echo "build-runtime: no prebuilt server at $SERVER_BUNDLE" >&2; exit 1; }
else
    # `make bundle` → `build` → `dashboard-build` runs npm scripts but never
    # installs. On a clean checkout (which is every CI run) that surfaces as a
    # missing-binary error from npm rather than "you need to install deps".
    if [[ ! -d server/dashboard/node_modules ]]; then
        echo "build-runtime: installing dashboard dependencies"
        make -C server dashboard-deps
    fi
    make -C server bundle SERVER_VERSION="$SERVER_VERSION" LLAMA_STRICT=1
fi

# --- 2. cix CLI -------------------------------------------------------------
# -w without -s: govulncheck -mode=binary needs the Go symbol table. Same
# reasoning, and same flags, as .github/workflows/release-cli.yml.
echo "build-runtime: building cix CLI"
mkdir -p "$OUT_DIR/stage"
(cd cli && go build \
    -trimpath \
    -ldflags "-w -X 'github.com/dvcdsys/code-index/cli/cmd.Version=${CLI_VERSION}'" \
    -o "$OUT_DIR/stage/cix" .)

# --- 3. assemble ------------------------------------------------------------
# Rebuild from scratch. `cp` merges into an existing tree, so an incremental
# assembly accumulates artefacts from every previous build — the exact failure
# server/Makefile's `bundle` target had to fix for llama/.
echo "build-runtime: assembling $STAGE"
rm -rf "$OUT_DIR/runtime"
mkdir -p "$STAGE"

cp "$SERVER_BUNDLE/cix-server" "$STAGE/cix-server"
cp -R "$SERVER_BUNDLE/llama" "$STAGE/llama"
cp "$OUT_DIR/stage/cix" "$STAGE/cix"
rm -rf "$OUT_DIR/stage"

# The manifest is what the menu reads to show what is installed. The alternative
# — exec'ing each binary with -v — costs three process spawns per menu open and
# still cannot report the llama version, which no binary here prints in a form
# worth parsing.
#
# Checked at the source rather than linted afterwards: every value below is
# interpolated straight into JSON, so the thing worth rejecting is a version
# string carrying a quote or a backslash, not a malformed file. `git describe`
# output and the Makefile pin are the only inputs, and neither should ever
# contain anything outside this set.
for v in "$SERVER_VERSION" "$CLI_VERSION" "$LLAMA_VERSION"; do
    if [[ ! "$v" =~ ^[A-Za-z0-9._+-]+$ ]]; then
        echo "build-runtime: version string is not safe to embed in JSON: '$v'" >&2
        exit 1
    fi
done

cat > "$STAGE/runtime.json" <<JSON
{
  "server_version": "$SERVER_VERSION",
  "cli_version": "$CLI_VERSION",
  "llama_version": "$LLAMA_VERSION",
  "platform": "darwin-arm64"
}
JSON

# --- 4. sign ----------------------------------------------------------------
# Extended attributes first. server/Makefile records the failure this prevents:
# on macOS 26, amfid SIGKILLs an ad-hoc-signed binary whose linked dylibs carry
# a stale signature or a com.apple.provenance xattr, and it does so with EMPTY
# STDERR — the process just dies. Every `cp` into the staging tree recreates
# those conditions, so the strip happens on every build.
echo "build-runtime: stripping extended attributes"
xattr -cr "$STAGE"

sign_llama_dir "$STAGE/llama"
for bin in cix cix-server; do
    echo "build-runtime: signing $bin"
    sign_adhoc "$STAGE/$bin"
done

# --- 5. tar -----------------------------------------------------------------
# COPYFILE_DISABLE=1 keeps Apple's tar from serialising extended attributes and
# resource forks into AppleDouble `._name` members. Those are noise here — the
# code signature of a Mach-O lives inside the file, in an LC_CODE_SIGNATURE load
# command, not in an xattr, so it travels on its own. Shipping the `._` members
# would only reintroduce the provenance attributes just stripped.
echo "build-runtime: writing $TARBALL"
rm -f "$TARBALL"
COPYFILE_DISABLE=1 tar -czf "$TARBALL" -C "$OUT_DIR/runtime" "$RUNTIME_NAME"

if tar -tzf "$TARBALL" | grep -q '/\._'; then
    echo "build-runtime: AppleDouble members leaked into the tarball" >&2
    exit 1
fi

# --- 6. round trip ----------------------------------------------------------
# The riskiest step in this whole pipeline is the one that looks like it cannot
# fail: does an ad-hoc signature survive tar → extract? If it does not, nothing
# says so — the binary is SIGKILLed on exec with empty stderr, indistinguishable
# from a crash. So unpack what was just written and check it as a stranger would.
if [[ "${SKIP_VERIFY:-0}" == "1" ]]; then
    echo "build-runtime: SKIP_VERIFY=1 — tarball NOT round-tripped"
else
    echo "build-runtime: verifying the tarball round trip"
    CHECK_DIR="$(mktemp -d)"
    trap 'rm -rf "$CHECK_DIR"' EXIT

    tar -xzf "$TARBALL" -C "$CHECK_DIR"
    EXTRACTED="$CHECK_DIR/$RUNTIME_NAME"

    for target in cix-server cix llama/llama-server; do
        codesign --verify --strict --verbose=2 "$EXTRACTED/$target"
    done
    for lib in "$EXTRACTED"/llama/*.dylib; do
        codesign --verify --strict "$lib"
    done

    check_llama_rpath_deps "$EXTRACTED/llama"

    # Actually run one. codesign --verify checks the signature is well-formed;
    # only exec proves the kernel agrees, which is the thing that fails silently.
    got="$("$EXTRACTED/cix-server" -v)"
    echo "build-runtime: extracted server reports: $got"
    case "$got" in
        *"$SERVER_VERSION"*) ;;
        *)
            echo "build-runtime: extracted server reports '$got', expected $SERVER_VERSION" >&2
            echo "build-runtime: the payload does not match its label — $SERVER_BUNDLE is stale (a SKIP_SERVER_BUILD=1 build against an older commit?). Rebuild it." >&2
            exit 1
            ;;
    esac

    "$EXTRACTED/cix" --version >/dev/null
fi

echo "build-runtime: ready — $TARBALL"
du -sh "$TARBALL" | sed 's/^/build-runtime: size /'
