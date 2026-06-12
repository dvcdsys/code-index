#!/usr/bin/env bash
# Builds ts-core.wasm: the OFFICIAL tree-sitter C runtime + the base grammars +
# our host_extra.c (the batched ts_dump_tree walk), compiled to ONE standalone
# wasm32-wasi reactor module via `zig cc`. No emscripten, no JS glue.
#
# Requires: zig (clang + wasi-libc cross-compile), git, and tree-sitter CLI (only
# for grammars whose repo ships no committed parser.c — gen=1 rows).
#
# For wasm we compile each grammar IN PLACE from a full clone, so relative
# includes (e.g. typescript's ../../common/scanner.h) and src-root headers (html
# tag.h, haskell unicode.h) resolve naturally — none of the vendor.sh copy/rewrite
# dance is needed. Quirks that remain: SHA pins (dart), `tree-sitter generate`
# (sql), and a 2nd grammar from one repo (tsx). See plan §6.1.
set -euo pipefail
cd "$(dirname "$0")"

TS_VERSION="${TS_VERSION:-v0.25.10}"
OUT="${OUT:-ts-core.wasm}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# id  repo  ref  srcsubdir  [gen]
GRAMMARS=(
  "python      tree-sitter/tree-sitter-python      v0.25.0 src"
  "typescript  tree-sitter/tree-sitter-typescript  v0.23.2 typescript/src"
  "tsx         tree-sitter/tree-sitter-typescript  v0.23.2 tsx/src"
  "javascript  tree-sitter/tree-sitter-javascript  v0.25.0 src"
  "go          tree-sitter/tree-sitter-go          v0.25.0 src"
  "rust        tree-sitter/tree-sitter-rust        v0.24.2 src"
  "java        tree-sitter/tree-sitter-java        v0.23.5 src"
  "c           tree-sitter/tree-sitter-c           v0.24.2 src"
  "cpp         tree-sitter/tree-sitter-cpp         v0.23.4 src"
  "ruby        tree-sitter/tree-sitter-ruby        v0.23.1 src"
  "c_sharp     tree-sitter/tree-sitter-c-sharp     v0.23.5 src"
  "php         tree-sitter/tree-sitter-php         v0.24.2 php/src"
  "swift       alex-pinkus/tree-sitter-swift       0.7.3-with-generated-files src"
  "kotlin      tree-sitter-grammars/tree-sitter-kotlin v1.1.0 src"
  "scala       tree-sitter/tree-sitter-scala       v0.26.0 src"
  "bash        tree-sitter/tree-sitter-bash        v0.25.1 src"
  "lua         tree-sitter-grammars/tree-sitter-lua v0.5.0 src"
  "dart        UserNobody14/tree-sitter-dart       a9bdfa3 src"
  "r           r-lib/tree-sitter-r                 v1.2.0 src"
  "objc        tree-sitter-grammars/tree-sitter-objc v3.0.2 src"
  "html        tree-sitter/tree-sitter-html        v0.23.2 src"
  "css         tree-sitter/tree-sitter-css         v0.25.0 src"
  "scss        tree-sitter-grammars/tree-sitter-scss v1.0.0 src"
  "sql         DerekStride/tree-sitter-sql         v0.3.11 src 1"
  "markdown    tree-sitter-grammars/tree-sitter-markdown v0.5.3 tree-sitter-markdown/src"
  "zig         tree-sitter-grammars/tree-sitter-zig v1.1.2 src"
  "julia       tree-sitter/tree-sitter-julia       v0.25.0 src"
  "fortran     stadelmanma/tree-sitter-fortran     v0.6.0 src"
  "haskell     tree-sitter/tree-sitter-haskell     v0.23.1 src"
  "ocaml       tree-sitter/tree-sitter-ocaml       v0.25.0 grammars/ocaml/src"
  "solidity    JoranHonig/tree-sitter-solidity     v1.2.13 src"
)

clone() { # repo ref dest  — tag/branch fast path, SHA fallback
  local repo="$1" ref="$2" dest="$3"
  git clone --depth 1 --branch "$ref" "https://github.com/$repo" "$dest" >/dev/null 2>&1 && return 0
  git clone "https://github.com/$repo" "$dest" >/dev/null 2>&1 || return 1
  git -C "$dest" checkout "$ref" >/dev/null 2>&1
}

echo "→ tree-sitter runtime $TS_VERSION"
git clone --depth 1 --branch "$TS_VERSION" https://github.com/tree-sitter/tree-sitter "$WORK/tree-sitter" 2>/dev/null

SRCS=( "$WORK/tree-sitter/lib/src/lib.c" "csrc/host_extra.c" )
INCS=( -I "$WORK/tree-sitter/lib/include" -I "$WORK/tree-sitter/lib/src" )
EXPORTS=()
BUILT=() ; FAILED=()

for row in "${GRAMMARS[@]}"; do
  read -r id repo ref sub gen <<<"$row"
  printf '  %-12s %s@%s ' "$id" "$repo" "$ref"
  if ! clone "$repo" "$ref" "$WORK/$id"; then echo "CLONE FAIL"; FAILED+=("$id"); continue; fi
  gsrc="$WORK/$id/$sub"
  if [ "${gen:-0}" = "1" ] && [ ! -f "$gsrc/parser.c" ]; then
    ( cd "$WORK/$id" && tree-sitter generate >/dev/null 2>&1 ) || true
  fi
  if [ ! -f "$gsrc/parser.c" ]; then echo "NO parser.c"; FAILED+=("$id"); continue; fi
  SRCS+=( "$gsrc/parser.c" )
  [ -f "$gsrc/scanner.c" ]  && SRCS+=( "$gsrc/scanner.c" )
  [ -f "$gsrc/scanner.cc" ] && SRCS+=( "$gsrc/scanner.cc" )
  INCS+=( -I "$gsrc" )
  EXPORTS+=( -Wl,--export=tree_sitter_$id )
  BUILT+=("$id")
  echo "ok"
done

echo "→ compiling ${#SRCS[@]} sources, ${#BUILT[@]} grammars → $OUT"
zig cc --target=wasm32-wasi-musl -mexec-model=reactor \
  "${INCS[@]}" "${SRCS[@]}" \
  -o "$OUT" -Oz -fPIC -Wl,--no-entry -Wl,--strip-debug \
  -Wl,--export=malloc -Wl,--export=free \
  -Wl,--export=ts_parser_new -Wl,--export=ts_parser_delete \
  -Wl,--export=ts_parser_set_language -Wl,--export=ts_parser_parse_string \
  -Wl,--export=ts_parser_reset \
  -Wl,--export=ts_tree_delete -Wl,--export=ts_tree_root_node \
  -Wl,--export=ts_node_child_count -Wl,--export=ts_node_child \
  -Wl,--export=ts_node_type -Wl,--export=ts_node_start_byte \
  -Wl,--export=ts_node_end_byte -Wl,--export=ts_node_has_error \
  -Wl,--export=ts_dump_tree -Wl,--export=ts_dump_rec_size \
  -Wl,--export=ts_language_symbol_count -Wl,--export=ts_language_symbol_name \
  "${EXPORTS[@]}"

echo "built $OUT ($(du -h "$OUT" | cut -f1)) — runtime $TS_VERSION, ${#BUILT[@]} grammars"
[ ${#FAILED[@]} -gt 0 ] && echo "FAILED: ${FAILED[*]}"
echo "grammars: ${BUILT[*]}"

# Brotli-compress to the committed artifact the package embeds. The raw .wasm is
# a gitignored intermediate; only ts-core.wasm.br (~3 MB) goes into git.
echo "→ brotli-compressing → ${OUT}.br"
go run compress.go
