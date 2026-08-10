# shellcheck shell=bash
# common.sh — shared ground for the mac build scripts. Sourced, never executed.
#
# build-app.sh and build-runtime.sh produce two halves of one release: they are
# published under the same mac/vX.Y.Z tag and the launcher assumes they agree
# about which server, CLI and llama went into them. Deriving those versions
# twice, in two scripts, is how they would quietly stop agreeing — so it happens
# here, once.

# --- host check -------------------------------------------------------------
# Upstream llama.cpp publishes one macOS asset, macos-arm64, and
# server/scripts/fetch-llama.sh hard-refuses anything else. Building the Go
# binaries for x86_64 and pairing them with an arm64 llama-server produces a
# tree that assembles cleanly, signs cleanly, and dies at the first embedding —
# so refuse up front, where the message can say why.
require_apple_silicon() {
    local who="${1:-build}"
    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "$who: macOS only (got $(uname -s))" >&2
        exit 1
    fi
    if [[ "$(uname -m)" != "arm64" ]]; then
        echo "$who: Apple Silicon only — upstream llama.cpp ships no macOS x86_64 asset (got $(uname -m))" >&2
        exit 1
    fi
}

# --- versions ---------------------------------------------------------------
# Versions are passed in by CI rather than derived here because `git describe`
# cannot be trusted on this repo: the tag streams interleave, and server/v0.12.8
# sits on a commit reachable from no branch. These fallbacks exist for local
# builds, where being obviously a development build is the correct answer.
describe_or_dev() {
    local pattern="$1" prefix="$2" v
    v="$(git describe --tags --match "$pattern" 2>/dev/null | sed "s|^$prefix||")" || true
    printf '%s' "${v:-0.0.0-dev}"
}

# The llama version is not a git tag — it is pinned in the Makefile that fetches
# the upstream release, which is the only place that knows what was downloaded.
read_llama_version() {
    local repo_root="$1" v
    v="$(sed -n 's/^LLAMA_VERSION[[:space:]]*?=[[:space:]]*//p' "$repo_root/server/Makefile" | head -n1)"
    if [[ -z "$v" ]]; then
        echo "could not read LLAMA_VERSION from $repo_root/server/Makefile" >&2
        exit 1
    fi
    printf '%s' "$v"
}

# --- signing ----------------------------------------------------------------
# Ad-hoc (`--sign -`) is not a Gatekeeper credential and never will be without a
# paid Developer ID. It is still mandatory: on Apple Silicon the kernel refuses
# to run an executable carrying no signature at all.
sign_adhoc() {
    local target="$1"
    [[ -e "$target" ]] || { echo "sign: missing signing target: $target" >&2; exit 1; }
    codesign --force --sign - "$target"
}

# Sign a directory of Mach-O code bottom-up: libraries first, then the
# executables that load them.
#
# llama-server links its dylibs by @rpath. If a dylib's signature is stale
# relative to the executable loading it, the load fails at dyld time — after
# codesign has already reported success on the executable itself. Signing in
# this order is what prevents that, and it is why --deep (deprecated, and
# unreliable for loose executables outside nested bundles) is not used anywhere
# in this pipeline.
sign_llama_dir() {
    local dir="$1" lib
    local -a dylibs
    shopt -s nullglob
    dylibs=("$dir"/*.dylib)
    shopt -u nullglob
    if [[ ${#dylibs[@]} -eq 0 ]]; then
        echo "sign: no dylibs found under $dir — the tree is incomplete" >&2
        exit 1
    fi
    echo "sign: signing ${#dylibs[@]} dylib(s) in $(basename "$dir")"
    for lib in "${dylibs[@]}"; do
        sign_adhoc "$lib"
    done
    sign_adhoc "$dir/llama-server"
}

# --- dependency check -------------------------------------------------------
# Every @rpath dependency of llama-server must sit beside it. A missing one only
# fails at dyld load time, on the user's machine, with an abort — which is how
# the b10238 library-layout change was found.
check_llama_rpath_deps() {
    local dir="$1" dep missing=0
    for dep in $(otool -L "$dir/llama-server" | awk '/@rpath\//{sub(/^.*@rpath\//,"",$1); print $1}'); do
        if [[ ! -e "$dir/$dep" ]]; then
            echo "llama-server dependency not present: $dep" >&2
            missing=1
        fi
    done
    return "$missing"
}
