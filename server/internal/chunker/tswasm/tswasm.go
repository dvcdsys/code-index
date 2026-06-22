// Package tswasm is the pure-Go tree-sitter backend for the chunker: the
// official tree-sitter C runtime + the base grammars + a batched-walk export
// (csrc/host_extra.c), compiled to one standalone wasm32-wasi module
// (ts-core.wasm via build.sh) and driven through wazero — no cgo, no JS.
//
// Parse the source, then ts_dump_tree walks the WHOLE tree inside the guest and
// writes a flat pre-order []NodeRec into linear memory; the host does ONE
// Memory.Read and decodes it. kind_id (TSSymbol) is resolved to a kind name once
// per language via ts_language_symbol_name (cached). This avoids the
// ~3-wazero-calls-per-node of a naive walk.
//
// Concurrency: a wazero module instance shares one linear memory and is NOT safe
// for concurrent calls, so each parse borrows an *engine from a pool (one wazero
// instance, its own memory) and returns it. The compiled module is shared.
package tswasm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// ts-core.wasm.br is the brotli-compressed module (~3 MB committed vs ~56 MB raw;
// the raw .wasm is a gitignored build intermediate). Decompressed once at startup.
//
//go:embed ts-core.wasm.br
var wasmBrotli []byte

// recSize must match sizeof(NodeRec) in csrc/host_extra.c (9 × uint32). Asserted
// against ts_dump_rec_size() when the first engine is created.
const recSize = 36

// Memory/pool knobs. wasm linear memory only GROWS within an instance (never
// shrinks), so the strategy is: cap each instance, recycle (close) instances
// whose memory grew past a threshold, and bound how many idle instances we
// retain. Set these BEFORE the first ParseNodes call (the server wires CIX_*
// env vars to them at startup).
// IMPORTANT: each instance loads ALL 31 grammar tables into its OWN linear
// memory, so the BASELINE is ~69 MiB per instance — the dominant memory cost,
// multiplied by how many instances are LIVE concurrently. (Unlike gotreesitter,
// where the tables were shared Go data.) The fix is MaxConcurrentInstances: a
// hard semaphore that DECOUPLES chunker parallelism from the indexer's embedding
// concurrency, so peak chunker memory ≈ MaxConcurrentInstances × ~69 MiB no
// matter how many files the indexer embeds in parallel. (A future per-grammar-
// module split, plan §10.5, would cut the baseline to a few MiB.)
var (
	// MaxConcurrentInstances is the hard cap on engine instances in use at once —
	// the chunker's OWN concurrency, independent of CIX_MAX_EMBEDDING_CONCURRENCY.
	// ParseNodes blocks (briefly — a parse is ~ms) until a slot frees. This is
	// what stops a high embedding concurrency from spawning N×69 MiB chunker
	// instances and OOM-ing. Bounds peak chunker memory ≈ this × per-instance.
	// Set BEFORE the first ParseNodes call. 0 → 3.
	MaxConcurrentInstances = 3

	// MemLimitPages caps each instance's linear memory (64 KiB pages); an
	// over-limit parse traps → sliding-window fallback, never a host OOM. Must be
	// well above the ~69 MiB baseline. 4096 pages = 256 MiB: the indexer caps
	// file content at 512 KiB and the worst measured instance (500 KiB
	// pathological Go, 358k nodes) reached 130 MiB total, so 256 MiB is ~2×
	// headroom; it is also the size of the virtual region the mmap allocator
	// reserves per instance. 0 = wazero default (4 GiB).
	MemLimitPages uint32 = 4096

	// RecycleGrowthBytes: a released instance whose memory grew more than this
	// ABOVE the baseline (i.e. it parsed a huge file) is CLOSED instead of pooled,
	// so one pathological file doesn't permanently inflate a pooled instance.
	// A fresh instance returns to the ~69 MiB baseline. 0 disables.
	RecycleGrowthBytes uint64 = 128 << 20 // 128 MiB over baseline

	// MaxIdleInstances bounds idle instances kept in the pool. The indexer
	// chunks files SEQUENTIALLY within a session, so steady state is one
	// instance; keeping just 1 idle avoids 2 more ~69-130 MiB instances
	// surviving a brief concurrency burst forever. Trade-off: under SUSTAINED
	// concurrent sessions (several projects indexing in parallel for long
	// stretches) the over-cap engines churn close/create on each release —
	// bump CIX_CHUNK_MAX_IDLE if that's your profile. 0 → matches
	// MaxConcurrentInstances.
	MaxIdleInstances = 1
)

// concLimiter is a RESIZABLE concurrency limiter (a fixed channel can't be
// resized, and we want live tuning from the dashboard). acquire blocks until
// inUse < max; setMax changes the ceiling and wakes waiters to re-check.
type concLimiter struct {
	mu    sync.Mutex
	cond  *sync.Cond
	inUse int
	max   int
}

func newConcLimiter(max int) *concLimiter {
	if max < 1 {
		max = 1
	}
	l := &concLimiter{max: max}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *concLimiter) acquire() {
	l.mu.Lock()
	for l.inUse >= l.max {
		l.cond.Wait()
	}
	l.inUse++
	l.mu.Unlock()
}

func (l *concLimiter) release() {
	l.mu.Lock()
	l.inUse--
	l.cond.Signal()
	l.mu.Unlock()
}

func (l *concLimiter) setMax(n int) {
	if n < 1 {
		n = 1
	}
	l.mu.Lock()
	l.max = n
	l.mu.Unlock()
	l.cond.Broadcast() // newly-allowed slots: wake all waiters to re-check
}

// instLim caps concurrent in-use engine instances (MaxConcurrentInstances),
// created once at initRuntime and resizable live via SetMaxConcurrent.
var instLim *concLimiter

// SetMaxConcurrent sets the chunker's instance-concurrency ceiling. Safe to call
// before init (records the value for initRuntime) or live afterwards (resizes the
// limiter without a restart — this is what the dashboard's chunk_max_concurrent
// override calls).
func SetMaxConcurrent(n int) {
	if n < 1 {
		n = 1
	}
	MaxConcurrentInstances = n
	if instLim != nil {
		instLim.setMax(n)
	}
}

// baselineBytes is the linear-memory size of a fresh instance, captured once.
var baselineBytes atomic.Uint64

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

// ---------------------------------------------------------------------------
// Shared runtime (compiled once) + engine pool
// ---------------------------------------------------------------------------

type runtime struct {
	ctx context.Context
	wz  wazero.Runtime
	cm  wazero.CompiledModule
}

var (
	rtOnce sync.Once
	rt     *runtime
	rtErr  error
	gpool  enginePool
	instN  atomic.Int64
)

func initRuntime() {
	// On unix the engines' linear memory is mmap-backed (see mmap_unix.go):
	// grow without realloc-copy, and munmap-on-close returns a recycled
	// instance's memory to the OS immediately instead of parking it in the Go
	// heap. The allocator rides the context, which wazero reads at
	// InstantiateModule time.
	ctx := context.Background()
	if a := linearMemoryAllocator(); a != nil {
		ctx = experimental.WithMemoryAllocator(ctx, a)
	}
	n := MaxConcurrentInstances
	if n <= 0 {
		n = 3
	}
	instLim = newConcLimiter(n)
	cfg := wazero.NewRuntimeConfigCompiler()
	if MemLimitPages > 0 {
		cfg = cfg.WithMemoryLimitPages(MemLimitPages)
	}
	wz := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wz); err != nil {
		rtErr = fmt.Errorf("wasi: %w", err)
		return
	}
	raw, err := io.ReadAll(brotli.NewReader(bytes.NewReader(wasmBrotli)))
	if err != nil {
		rtErr = fmt.Errorf("decompress ts-core.wasm.br: %w", err)
		return
	}
	cm, err := wz.CompileModule(ctx, raw)
	if err != nil {
		rtErr = fmt.Errorf("compile ts-core.wasm: %w", err)
		return
	}
	rt = &runtime{ctx: ctx, wz: wz, cm: cm}
}

// engine is one instantiated wasm module (own linear memory).
type engine struct {
	ctx context.Context
	mod api.Module
	mem api.Memory

	malloc, free                         api.Function
	parserNew, parserDelete, parserReset api.Function
	setLang, parse, treeDelete           api.Function
	dumpTree                             api.Function
	langSymCount, langSymName            api.Function

	langPtr map[string]uint32 // per-instance: TSLanguage* lives in this instance's memory
}

// symNameCache maps langExport → (symbol id → kind name). The id→name mapping is
// a property of the compiled grammar, identical across ALL instances, so it is
// cached GLOBALLY and built once per language — not once per instance. (Building
// it costs ~1 s: one wazero call per grammar symbol, ~2000 for TypeScript. The
// old per-instance cache meant every recycled/fresh instance paid that ~1 s on
// its first file of a language — the "indexing hangs on this file" symptom.)
var (
	symNameMu    sync.Mutex
	symNameCache = map[string]map[uint32]string{}
)

func newEngine() (*engine, error) {
	rtOnce.Do(initRuntime)
	if rtErr != nil {
		return nil, rtErr
	}
	name := fmt.Sprintf("ts-%d", instN.Add(1))
	mod, err := rt.wz.InstantiateModule(rt.ctx, rt.cm,
		wazero.NewModuleConfig().WithName(name).WithStartFunctions("_initialize"))
	if err != nil {
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	e := &engine{
		ctx: rt.ctx, mod: mod, mem: mod.Memory(),
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
	}
	if rs := e.call(mod.ExportedFunction("ts_dump_rec_size")); rs != recSize {
		mod.Close(rt.ctx)
		return nil, fmt.Errorf("NodeRec size mismatch: guest=%d host=%d", rs, recSize)
	}
	baselineBytes.CompareAndSwap(0, e.memSize())
	return e, nil
}

func (e *engine) close() { e.mod.Close(e.ctx) }

// memSize is the engine's current linear-memory size in bytes.
func (e *engine) memSize() uint64 { return uint64(e.mem.Size()) }

// enginePool is a bounded pool with high-water-mark recycling. Because wasm
// linear memory only grows, an instance that ballooned parsing one huge file is
// CLOSED on release (RecycleBytes) instead of kept holding that memory; idle
// instances are capped (MaxIdleInstances). The atomic counters feed PoolStats()
// so the operator can watch memory under concurrent sync.
type enginePool struct {
	mu   sync.Mutex
	free []*engine

	created  atomic.Int64
	closed   atomic.Int64
	recycled atomic.Int64
}

func (p *enginePool) maxIdle() int {
	if MaxIdleInstances > 0 {
		return MaxIdleInstances
	}
	if MaxConcurrentInstances > 0 {
		return MaxConcurrentInstances
	}
	return 3
}

// acquire takes a concurrency slot (blocking until one frees — this is the hard
// cap that decouples chunker parallelism from embedding concurrency) then returns
// a pooled-or-fresh engine. The slot is released by release().
func (p *enginePool) acquire() (*engine, error) {
	// Ensure the runtime (and instLim) exist before taking a slot — newEngine
	// also triggers this, but instLim.acquire() runs first.
	rtOnce.Do(initRuntime)
	if rtErr != nil {
		return nil, rtErr
	}
	instLim.acquire()
	p.mu.Lock()
	if n := len(p.free); n > 0 {
		e := p.free[n-1]
		p.free[n-1] = nil
		p.free = p.free[:n-1]
		p.mu.Unlock()
		return e, nil
	}
	p.mu.Unlock()
	e, err := newEngine()
	if err != nil {
		instLim.release() // failed to create → give the slot back
		return nil, err
	}
	p.created.Add(1)
	return e, nil
}

// release returns an engine to the pool, or closes it when: tainted (a trap may
// have left guest allocations un-freed), it grew past RecycleGrowthBytes, or the
// idle pool is full. It always frees the concurrency slot taken by acquire().
func (p *enginePool) release(e *engine, tainted bool) {
	defer instLim.release()
	switch {
	case tainted:
		p.discard(e)
	case RecycleGrowthBytes > 0 && e.memSize() > baselineBytes.Load()+RecycleGrowthBytes:
		p.recycled.Add(1)
		p.discard(e)
	default:
		p.mu.Lock()
		if len(p.free) >= p.maxIdle() {
			p.mu.Unlock()
			p.discard(e)
			return
		}
		p.free = append(p.free, e)
		p.mu.Unlock()
	}
}

func (p *enginePool) discard(e *engine) {
	e.close()
	p.closed.Add(1)
}

// Stats is a snapshot of the engine pool for memory observability.
type Stats struct {
	Created      int64  // instances ever created
	Closed       int64  // instances closed (recycled + over-idle + tainted)
	Recycled     int64  // subset of Closed: closed due to the RecycleBytes high-water-mark
	LiveOpen     int64  // Created-Closed (idle + checked-out)
	IdlePooled   int    // instances currently idle in the pool
	IdleMemBytes uint64 // sum of idle instances' current linear memory
}

// PoolStats returns a live snapshot for /health or a debug endpoint.
func PoolStats() Stats {
	gpool.mu.Lock()
	idle := len(gpool.free)
	var mem uint64
	for _, e := range gpool.free {
		mem += e.memSize()
	}
	gpool.mu.Unlock()
	created, closed := gpool.created.Load(), gpool.closed.Load()
	return Stats{
		Created:      created,
		Closed:       closed,
		Recycled:     gpool.recycled.Load(),
		LiveOpen:     created - closed,
		IdlePooled:   idle,
		IdleMemBytes: mem,
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// HasLanguage reports whether the module exports the given grammar (e.g.
// "tree_sitter_go"). Cheap; instantiates the pool lazily.
func HasLanguage(langExport string) bool {
	e, err := gpool.acquire()
	if err != nil {
		return false
	}
	defer gpool.release(e, false)
	return e.mod.ExportedFunction(langExport) != nil
}

// ParseNodes parses src under the grammar export and returns the whole tree as a
// flat pre-order slice (batched via ts_dump_tree). A guest trap is returned as an
// error and the tainted instance is discarded; the host process stays alive.
func ParseNodes(langExport string, src []byte) ([]Node, error) {
	e, err := gpool.acquire()
	if err != nil {
		return nil, err
	}
	nodes, perr := e.parseNodes(langExport, src)
	// On error the instance may be tainted (a trap can leave guest allocations
	// un-freed) → release() closes it rather than pooling it.
	gpool.release(e, perr != nil)
	if perr != nil {
		return nil, perr
	}
	return nodes, nil
}

func (e *engine) call(f api.Function, args ...uint64) uint64 {
	r, err := f.Call(e.ctx, args...)
	if err != nil {
		panic(err)
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

func (e *engine) language(export string) (uint32, bool) {
	if p, ok := e.langPtr[export]; ok {
		return p, p != 0
	}
	fn := e.mod.ExportedFunction(export)
	if fn == nil {
		e.langPtr[export] = 0
		return 0, false
	}
	p := uint32(e.call(fn))
	e.langPtr[export] = p
	return p, p != 0
}

func (e *engine) symbolNames(export string, lang uint32) map[uint32]string {
	symNameMu.Lock()
	m, ok := symNameCache[export]
	symNameMu.Unlock()
	if ok {
		return m
	}
	// Build outside the lock (the ~2000 wazero calls are slow). The result is
	// module-global, so a rare concurrent double-build is harmless — both produce
	// the identical map and last-writer-wins.
	count := uint32(e.call(e.langSymCount, uint64(lang)))
	m = make(map[uint32]string, count)
	for id := range count {
		ptr := uint32(e.call(e.langSymName, uint64(lang), uint64(id)))
		m[id] = e.readCStr(ptr)
	}
	symNameMu.Lock()
	symNameCache[export] = m
	symNameMu.Unlock()
	return m
}

func (e *engine) parseNodes(langExport string, src []byte) (nodes []Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wasm trap (contained): %v", r)
		}
	}()

	lang, ok := e.language(langExport)
	if !ok {
		return nil, fmt.Errorf("unknown grammar export %q", langExport)
	}
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

	raw, ok2 := e.mem.Read(buf, n*recSize)
	if !ok2 {
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

func (e *engine) readCStr(ptr uint32) string {
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
