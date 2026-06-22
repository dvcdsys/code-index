// Package wasmts is a pure-Go tree-sitter backend: the official tree-sitter C
// runtime + N grammars + our host_extra.c (batched ts_dump_tree walk), compiled
// to a standalone wasm32-wasi reactor module (see build.sh) and driven from Go
// via wazero — no cgo, no JS.
//
// Production path: Parse the source, then ts_dump_tree walks the WHOLE tree
// inside the guest and writes a flat pre-order []NodeRec into linear memory; the
// host does ONE Memory.Read and decodes it. kind_id (TSSymbol) is resolved to a
// kind name once per language via ts_language_symbol_name (cached). This replaces
// the naive ~3-wazero-calls-per-node walk that made the PoC ~2x slower than cgo.
package wasmts

import (
	"context"
	"encoding/binary"
	_ "embed"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed ts-core.wasm
var wasmBinary []byte

// recSize must match sizeof(NodeRec) in host_extra.c (9 × uint32). Asserted
// against ts_dump_rec_size() at New().
const recSize = 36

// Node is a decoded tree-sitter node from the batched dump.
type Node struct {
	KindID                       uint32
	Kind                         string // resolved from KindID via the per-language symbol table
	StartByte, EndByte           uint32
	StartRow, StartCol           uint32
	EndRow, EndCol               uint32
	Depth                        uint32
	Named, Error, Missing, Extra bool
}

// Engine is a single wazero instance hosting the tree-sitter runtime + grammars.
// NOT safe for concurrent use — one Engine per worker (see plan §8).
type Engine struct {
	ctx context.Context
	rt  wazero.Runtime
	mod api.Module
	mem api.Memory

	malloc, free                          api.Function
	parserNew, parserDelete, parserReset  api.Function
	setLang, parse, treeDelete            api.Function
	dumpTree                              api.Function
	langSymCount, langSymName             api.Function

	langPtr map[string]uint32            // langExport -> TSLanguage*
	symName map[string]map[uint32]string // langExport -> (symbol id -> kind name)
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
		malloc:       mod.ExportedFunction("malloc"),
		free:         mod.ExportedFunction("free"),
		parserNew:    mod.ExportedFunction("ts_parser_new"),
		parserDelete: mod.ExportedFunction("ts_parser_delete"),
		parserReset:  mod.ExportedFunction("ts_parser_reset"),
		setLang:      mod.ExportedFunction("ts_parser_set_language"),
		parse:        mod.ExportedFunction("ts_parser_parse_string"),
		treeDelete:   mod.ExportedFunction("ts_tree_delete"),
		dumpTree:     mod.ExportedFunction("ts_dump_tree"),
		langSymCount: mod.ExportedFunction("ts_language_symbol_count"),
		langSymName:  mod.ExportedFunction("ts_language_symbol_name"),
		langPtr:      map[string]uint32{},
		symName:      map[string]map[uint32]string{},
	}
	if rs := e.call(mod.ExportedFunction("ts_dump_rec_size")); rs != recSize {
		rt.Close(ctx)
		return nil, fmt.Errorf("NodeRec size mismatch: guest=%d host=%d", rs, recSize)
	}
	return e, nil
}

func (e *Engine) Close() { e.rt.Close(e.ctx) }

// call invokes a wasm export, surfacing a guest trap as a Go panic so the
// caller's recover() can contain it.
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

// language resolves (and caches) the TSLanguage* for a grammar export.
func (e *Engine) language(export string) uint32 {
	if p, ok := e.langPtr[export]; ok {
		return p
	}
	p := uint32(e.call(e.mod.ExportedFunction(export)))
	e.langPtr[export] = p
	return p
}

// symbolNames builds (once per language) the symbol-id -> kind-name table so the
// per-node kind lookup happens in pure Go, never across the wazero boundary.
func (e *Engine) symbolNames(export string, lang uint32) map[uint32]string {
	if m, ok := e.symName[export]; ok {
		return m
	}
	count := uint32(e.call(e.langSymCount, uint64(lang)))
	m := make(map[uint32]string, count)
	for id := range count {
		ptr := uint32(e.call(e.langSymName, uint64(lang), uint64(id)))
		m[id] = e.readCStr(ptr)
	}
	e.symName[export] = m
	return m
}

// ParseNodes parses src under the given grammar export and returns the whole tree
// as a flat pre-order slice (batched via ts_dump_tree). A guest-side trap is
// returned as an error; the Engine and host process stay alive.
func (e *Engine) ParseNodes(langExport string, src []byte) (nodes []Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wasm trap (contained): %v", r)
		}
	}()

	lang := e.language(langExport)
	parser := e.call(e.parserNew)
	defer e.call(e.parserDelete, parser)
	e.call(e.setLang, parser, uint64(lang))

	sp := uint32(e.call(e.malloc, uint64(len(src)+1)))
	e.mem.Write(sp, src)
	e.mem.WriteByte(sp+uint32(len(src)), 0)
	defer e.call(e.free, uint64(sp))

	tree := e.call(e.parse, parser, 0, uint64(sp), uint64(len(src)))
	if tree == 0 {
		return nil, fmt.Errorf("parse returned null tree")
	}
	defer e.call(e.treeDelete, tree)

	// Pass 1: count nodes (no writes). Pass 2: dump into an exact buffer.
	n := uint32(e.call(e.dumpTree, tree, 0, 0))
	if n == 0 {
		return nil, nil
	}
	buf := uint32(e.call(e.malloc, uint64(n)*recSize))
	defer e.call(e.free, uint64(buf))
	got := uint32(e.call(e.dumpTree, tree, uint64(buf), uint64(n)))
	if got != n {
		return nil, fmt.Errorf("dump count changed between passes: %d vs %d", n, got)
	}

	raw, ok := e.mem.Read(buf, n*recSize)
	if !ok {
		return nil, fmt.Errorf("read dump buffer failed (ptr=%d len=%d)", buf, n*recSize)
	}
	names := e.symbolNames(langExport, lang)

	nodes = make([]Node, n)
	for i := range n {
		o := i * recSize
		kindID := binary.LittleEndian.Uint32(raw[o:])
		flags := binary.LittleEndian.Uint32(raw[o+32:])
		nodes[i] = Node{
			KindID:    kindID,
			Kind:      names[kindID],
			StartByte: binary.LittleEndian.Uint32(raw[o+4:]),
			EndByte:   binary.LittleEndian.Uint32(raw[o+8:]),
			StartRow:  binary.LittleEndian.Uint32(raw[o+12:]),
			StartCol:  binary.LittleEndian.Uint32(raw[o+16:]),
			EndRow:    binary.LittleEndian.Uint32(raw[o+20:]),
			EndCol:    binary.LittleEndian.Uint32(raw[o+24:]),
			Depth:     binary.LittleEndian.Uint32(raw[o+28:]),
			Named:     flags&1 != 0,
			Error:     flags&2 != 0,
			Missing:   flags&4 != 0,
			Extra:     flags&8 != 0,
		}
	}
	return nodes, nil
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

// ParseResult holds summary counts from a parse (back-compat for cmd/bench and
// cmd/stability).
type ParseResult struct {
	HasError bool
	Nodes    int
	Errors   int
}

// Parse is a thin summary wrapper over ParseNodes.
func (e *Engine) Parse(langExport string, src []byte) (ParseResult, error) {
	nodes, err := e.ParseNodes(langExport, src)
	if err != nil {
		return ParseResult{}, err
	}
	res := ParseResult{Nodes: len(nodes)}
	for _, n := range nodes {
		if n.Error {
			res.Errors++
		}
		if n.Error || n.Missing {
			res.HasError = true
		}
	}
	return res, nil
}
