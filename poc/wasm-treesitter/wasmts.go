// Package wasmts is a proof-of-concept pure-Go tree-sitter backend: the official
// tree-sitter C runtime + a grammar, compiled to a standalone wasm32-wasi
// reactor module (see build.sh) and driven from Go via wazero — no cgo, no JS.
//
// It exists to compare the WASM approach against the cgo backend on
// feat/chunker-cgo-treesitter (speed + crash-isolation). It is NOT wired into
// the chunker; see README.md for the benchmark/stability results.
//
// The wasm module exports the tree-sitter C API. TSNode is a 24-byte struct
// passed/returned by value; over the wasm C ABI clang lowers a by-value struct
// return to a hidden "sret" pointer first argument, and a by-value struct
// argument to a pointer into linear memory. So every node is a 24-byte slot in
// guest memory and we pass/receive pointers to those slots.
package wasmts

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed ts-ts.wasm
var wasmBinary []byte

const nodeSize = 24 // sizeof(TSNode) on wasm32: uint32[4] + ptr + ptr

// Engine is a single wazero instance hosting the tree-sitter runtime + grammars.
type Engine struct {
	ctx context.Context
	rt  wazero.Runtime
	mod api.Module
	mem api.Memory

	malloc, free                          api.Function
	parserNew, setLang, parse, treeDelete api.Function
	rootNode, childCount, child           api.Function
	nodeType, hasError                    api.Function

	pool []uint32 // reused node slots, indexed by tree depth
}

// New compiles and instantiates the wasm module. memLimitPages caps guest linear
// memory (0 = wazero default); a runaway/oversized parse then traps and is
// returned as a Go error instead of taking the process down.
func New(ctx context.Context, memLimitPages uint32) (*Engine, error) {
	cfg := wazero.NewRuntimeConfigCompiler()
	if memLimitPages > 0 {
		cfg = cfg.WithMemoryLimitPages(memLimitPages)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	mod, err := rt.InstantiateWithConfig(ctx, wasmBinary,
		wazero.NewModuleConfig().WithName("ts").WithStartFunctions("_initialize"))
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	e := &Engine{
		ctx: ctx, rt: rt, mod: mod, mem: mod.Memory(),
		malloc:     mod.ExportedFunction("malloc"),
		free:       mod.ExportedFunction("free"),
		parserNew:  mod.ExportedFunction("ts_parser_new"),
		setLang:    mod.ExportedFunction("ts_parser_set_language"),
		parse:      mod.ExportedFunction("ts_parser_parse_string"),
		treeDelete: mod.ExportedFunction("ts_tree_delete"),
		rootNode:   mod.ExportedFunction("ts_tree_root_node"),
		childCount: mod.ExportedFunction("ts_node_child_count"),
		child:      mod.ExportedFunction("ts_node_child"),
		nodeType:   mod.ExportedFunction("ts_node_type"),
		hasError:   mod.ExportedFunction("ts_node_has_error"),
	}
	return e, nil
}

func (e *Engine) Close() { e.rt.Close(e.ctx) }

// call invokes a wasm export, surfacing a guest trap as a Go error (panic) so
// the caller's recover() can contain it.
func (e *Engine) call(f api.Function, args ...uint64) uint64 {
	r, err := f.Call(e.ctx, args...)
	if err != nil {
		panic(err)
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

// Language returns the TSLanguage pointer for an exported grammar (e.g.
// "tree_sitter_typescript").
func (e *Engine) Language(export string) uint32 {
	return uint32(e.call(e.mod.ExportedFunction(export)))
}

// ParseResult holds the counts from a parse + full tree walk.
type ParseResult struct {
	HasError bool
	Nodes    int
	Errors   int
}

// Parse parses src under the given grammar export and walks the whole tree,
// counting nodes and ERROR nodes. A guest-side trap is returned as an error;
// the Engine (and the host process) stay alive.
func (e *Engine) Parse(langExport string, src []byte) (res ParseResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wasm trap (contained): %v", r)
		}
	}()

	parser := e.call(e.parserNew)
	lang := e.call(e.mod.ExportedFunction(langExport))
	e.call(e.setLang, parser, lang)
	defer e.call(e.mod.ExportedFunction("ts_parser_delete"), parser)

	sp := uint32(e.call(e.malloc, uint64(len(src)+1)))
	e.mem.Write(sp, src)
	e.mem.WriteByte(sp+uint32(len(src)), 0)
	defer e.call(e.free, uint64(sp))

	tree := e.call(e.parse, parser, 0, uint64(sp), uint64(len(src)))
	defer e.call(e.treeDelete, tree)

	root := uint32(e.call(e.malloc, nodeSize))
	defer e.call(e.free, uint64(root))
	e.call(e.rootNode, uint64(root), tree)

	res.HasError = e.call(e.hasError, uint64(root)) != 0
	e.walk(root, 0, &res)
	return res, nil
}

// walk recurses the tree, reusing one node slot per depth (no per-node malloc).
// Each node costs ~3 host<->guest calls (type, child_count, child) — the
// dominant WASM overhead vs cgo.
func (e *Engine) walk(nodePtr uint32, depth int, res *ParseResult) {
	res.Nodes++
	if e.readCStr(uint32(e.call(e.nodeType, uint64(nodePtr)))) == "ERROR" {
		res.Errors++
	}
	n := uint32(e.call(e.childCount, uint64(nodePtr)))
	if n == 0 {
		return
	}
	for len(e.pool) <= depth {
		e.pool = append(e.pool, uint32(e.call(e.malloc, nodeSize)))
	}
	slot := e.pool[depth]
	for i := uint32(0); i < n; i++ {
		e.call(e.child, uint64(slot), uint64(nodePtr), uint64(i))
		e.walk(slot, depth+1, res)
	}
}

func (e *Engine) readCStr(ptr uint32) string {
	if ptr == 0 {
		return ""
	}
	var b []byte
	for off := ptr; ; off++ {
		c, ok := e.mem.ReadByte(off)
		if !ok || c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
