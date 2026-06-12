// host_extra.c — custom exports compiled INTO the wasm module alongside the
// official tree-sitter runtime. The whole point is `ts_dump_tree`: it walks the
// parsed tree ENTIRELY inside the guest and writes a flat pre-order array of
// fixed-size records into linear memory in ONE shot. The host then does a single
// Memory.Read and runs the chunker's node matching in pure Go — turning the
// ~3 wazero calls-per-node of the naive walk into ~1 call per parse.
//
// See docs/wasm-treesitter-implementation-plan.md §7.2 / §7.3.
#include "tree_sitter/api.h"
#include <stdint.h>

// One record per node. ALL fields uint32 so the Go side reads 9 little-endian
// uint32s with zero packing ambiguity (recSize = 36). kind_id is the TSSymbol
// (fits in 16 bits; widened for clean alignment). The host resolves kind_id ->
// kind-name once per language via ts_language_symbol_name (see below), so the
// per-node string lookup never crosses the boundary.
typedef struct {
  uint32_t kind_id;     // ts_node_symbol
  uint32_t start_byte;
  uint32_t end_byte;
  uint32_t start_row;   // ts_node_start_point().row  (0-based line)
  uint32_t start_col;   // ts_node_start_point().column
  uint32_t end_row;     // ts_node_end_point().row
  uint32_t end_col;     // ts_node_end_point().column
  uint32_t depth;       // pre-order depth (parent reconstruction via depth stack)
  uint32_t flags;       // bit0 named, bit1 error, bit2 missing, bit3 extra
} NodeRec;

static void emit(NodeRec *out, uint32_t i, TSNode n, uint32_t depth) {
  out[i].kind_id    = ts_node_symbol(n);
  out[i].start_byte = ts_node_start_byte(n);
  out[i].end_byte   = ts_node_end_byte(n);
  TSPoint sp = ts_node_start_point(n);
  out[i].start_row  = sp.row;
  out[i].start_col  = sp.column;
  TSPoint ep = ts_node_end_point(n);
  out[i].end_row    = ep.row;
  out[i].end_col    = ep.column;
  out[i].depth      = depth;
  uint32_t f = 0;
  if (ts_node_is_named(n))    f |= 1u;
  if (ts_node_is_error(n))    f |= 2u;
  if (ts_node_is_missing(n))  f |= 4u;
  if (ts_node_is_extra(n))    f |= 8u;
  out[i].flags = f;
}

// ts_dump_tree writes up to `cap` records and returns the TRUE node count. If the
// return value > cap the buffer was too small: the host re-mallocs to the
// returned count and calls again. Iterative pre-order DFS via a tree cursor — no
// recursion, no per-node malloc, no boundary crossings.
uint32_t ts_dump_tree(const TSTree *tree, NodeRec *out, uint32_t cap) {
  if (tree == 0) return 0;
  TSTreeCursor cur = ts_tree_cursor_new(ts_tree_root_node(tree));
  uint32_t count = 0;
  uint32_t depth = 0;
  for (;;) {
    if (count < cap) {
      emit(out, count, ts_tree_cursor_current_node(&cur), depth);
    }
    count++;
    if (ts_tree_cursor_goto_first_child(&cur)) { depth++; continue; }
    for (;;) {
      if (ts_tree_cursor_goto_next_sibling(&cur)) break;
      if (!ts_tree_cursor_goto_parent(&cur)) {
        ts_tree_cursor_delete(&cur);
        return count;
      }
      depth--;
    }
  }
}

// recSize lets the host assert its struct layout matches the guest's.
uint32_t ts_dump_rec_size(void) { return (uint32_t)sizeof(NodeRec); }
