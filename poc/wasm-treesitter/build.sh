#!/usr/bin/env bash
# Builds ts-ts.wasm: the OFFICIAL tree-sitter C runtime + the official
# TypeScript grammar, compiled to a standalone wasm32-wasi reactor module.
# No emscripten, no JS glue, no third-party Go host — just official C sources
# driven from Go via wazero (see wasmts.go).
#
# Requires: zig (provides clang + wasi-libc cross-compilation), git.
#   brew install zig
#
# Key point: the only wasmtime-dependent part of the runtime (wasm_store.c) is
# guarded by `#ifdef TREE_SITTER_FEATURE_WASM`, which we do NOT define — so the
# stock amalgamation (lib/src/lib.c) compiles to wasi cleanly with no stubs.
set -euo pipefail
cd "$(dirname "$0")"

TS_VERSION="${TS_VERSION:-v0.25.10}"          # tree-sitter runtime
TS_TS_VERSION="${TS_TS_VERSION:-v0.23.2}"     # tree-sitter-typescript grammar
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git clone --depth 1 --branch "$TS_VERSION"    https://github.com/tree-sitter/tree-sitter            "$WORK/tree-sitter"
git clone --depth 1 --branch "$TS_TS_VERSION" https://github.com/tree-sitter/tree-sitter-typescript "$WORK/ts-typescript"

zig cc --target=wasm32-wasi-musl -mexec-model=reactor \
  -I "$WORK/tree-sitter/lib/include" -I "$WORK/tree-sitter/lib/src" \
  -I "$WORK/ts-typescript/typescript/src" \
  "$WORK/tree-sitter/lib/src/lib.c" \
  "$WORK/ts-typescript/typescript/src/parser.c" \
  "$WORK/ts-typescript/typescript/src/scanner.c" \
  -o ts-ts.wasm -Oz -fPIC -Wl,--no-entry -Wl,--strip-debug \
  -Wl,--export=malloc -Wl,--export=free \
  -Wl,--export=ts_parser_new -Wl,--export=ts_parser_delete \
  -Wl,--export=ts_parser_set_language -Wl,--export=ts_parser_parse_string \
  -Wl,--export=ts_tree_root_node -Wl,--export=ts_tree_delete \
  -Wl,--export=ts_node_child_count -Wl,--export=ts_node_child \
  -Wl,--export=ts_node_type -Wl,--export=ts_node_start_byte \
  -Wl,--export=ts_node_end_byte -Wl,--export=ts_node_has_error \
  -Wl,--export=tree_sitter_typescript

echo "built ts-ts.wasm ($(du -h ts-ts.wasm | cut -f1)) from tree-sitter $TS_VERSION + tree-sitter-typescript $TS_TS_VERSION"
