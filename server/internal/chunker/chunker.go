// Package chunker ports api/app/services/chunker.py to Go using gotreesitter.
// The public surface is ChunkFile, which returns ([]Chunk, []Reference, error).
// Sliding-window fallback is used when a language is not supported by the
// tree-sitter grammars bundle or when parsing fails.
//
// The set of active languages is built from a baked-in default registry
// (see defaultRegistry) and may be filtered at startup via Configure(). The
// CIX_LANGUAGES env var feeds Configure with a comma-separated whitelist;
// empty/nil keeps all defaults.
package chunker

import (
	"github.com/dvcdsys/code-index/server/internal/tokenizer"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dvcdsys/code-index/server/internal/chunker/tswasm"
)

// maxChunkSize is the default maximum chunk size in bytes (chars).
// Python uses max_chunk_tokens * 4 (prose heuristic), but code tokenizers are
// denser (~3 chars/token vs 4 for prose). Using *3 keeps chunks under 1500
// tokens for typical source code, avoiding ubatch overflow on the embedder.
const maxChunkSize = 1500 * 3 // 4500 chars

// windowSize and overlap for the sliding-window fallback, matching Python.
const (
	windowSize = 4000
	overlap    = 500
)

// minRefNameLength mirrors MIN_REF_NAME_LENGTH in chunker.py.
const minRefNameLength = 2

// NOTE: the official tree-sitter (via tswasm) does not have gotreesitter's
// catastrophic-backtracking pathology, so the old SetTimeoutMicros +
// cancellation-flag wall-clock guard is gone. A watchdog-that-recycles-the-
// instance on a hard deadline is the Phase-4 equivalent (plan §7.4); until then
// a guest trap (e.g. memory cap) is caught by tswasm and falls back to the
// sliding window.

// ---------------------------------------------------------------------------
// Language registry — built from defaultRegistry() at init() and reduced by
// Configure() if the operator set CIX_LANGUAGES. The three exported maps
// stay package-private; the engine reads them directly.
// ---------------------------------------------------------------------------

// languageEntry bundles the per-language chunker state. The grammar itself lives
// in the tswasm module, addressed by the export name "tree_sitter_<id>" derived
// from the registry key — so no factory field is needed.
type languageEntry struct {
	nodes       map[string][]string // function|class|method|type → AST node types
	identifiers map[string]struct{} // identifier leaf-node types for ref extraction
}

var (
	registryMu       sync.RWMutex
	languageRegistry map[string]string // lang id → tswasm export ("tree_sitter_<id>")
	languageNodes    map[string]map[string][]string
	identifierNodes  map[string]map[string]struct{}
)

func init() {
	// Populate full defaults so direct ChunkFile usage (and tests) works
	// without a Configure() call. Server startup later may filter via
	// Configure(cfg.Languages).
	Configure(nil)
}

// Configure (re)builds the active language registry from the baked-in
// defaultRegistry, optionally filtered to the IDs in `enabled`. Empty or nil
// `enabled` activates all defaults. Unknown IDs are logged and ignored.
// Idempotent and safe to call multiple times; concurrent ChunkFile callers
// see a consistent snapshot via registryMu.
func Configure(enabled []string) {
	defaults := defaultRegistry()

	wantAll := len(enabled) == 0
	wanted := make(map[string]struct{}, len(enabled))
	if !wantAll {
		for _, raw := range enabled {
			id := strings.ToLower(strings.TrimSpace(raw))
			if id == "" {
				continue
			}
			wanted[id] = struct{}{}
		}
	}

	reg := make(map[string]string, len(defaults))
	nodes := make(map[string]map[string][]string, len(defaults))
	idents := make(map[string]map[string]struct{}, len(defaults))

	for lang, entry := range defaults {
		if !wantAll {
			if _, ok := wanted[lang]; !ok {
				continue
			}
		}
		reg[lang] = "tree_sitter_" + lang
		if entry.nodes != nil {
			nodes[lang] = entry.nodes
		}
		if entry.identifiers != nil {
			idents[lang] = entry.identifiers
		}
	}

	if !wantAll {
		for id := range wanted {
			if _, ok := defaults[id]; !ok {
				slog.Warn("chunker: unknown language in CIX_LANGUAGES, ignored", "lang", id)
			}
		}
	}

	registryMu.Lock()
	languageRegistry = reg
	languageNodes = nodes
	identifierNodes = idents
	registryMu.Unlock()
}

// SupportedLanguages returns a snapshot of currently-active language IDs.
// Useful for /health, debug endpoints, and test assertions.
func SupportedLanguages() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(languageRegistry))
	for k := range languageRegistry {
		out = append(out, k)
	}
	return out
}

// defaultRegistry returns the baked-in language entries. Adding a language is
// a single new map entry — no other code changes are needed because the
// chunker engine is data-driven.
func defaultRegistry() map[string]languageEntry {
	idID := func(extra ...string) map[string]struct{} {
		m := map[string]struct{}{"identifier": {}}
		for _, e := range extra {
			m[e] = struct{}{}
		}
		return m
	}

	return map[string]languageEntry{
		// --- Tier 1: original 6, kept as-is for parity with legacy Python ---
		"python": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_definition"},
			},
			identifiers: idID(),
		},
		"typescript": {
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
				"type":     {"interface_declaration", "type_alias_declaration"},
			},
			identifiers: idID("type_identifier", "property_identifier"),
		},
		"javascript": {
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
			},
			identifiers: idID("property_identifier"),
		},
		"go": {
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"method":   {"method_declaration"},
				"type":     {"type_spec"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"rust": {
			nodes: map[string][]string{
				"function": {"function_item"},
				"class":    {"struct_item", "enum_item"},
				"type":     {"trait_item"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"java": {
			nodes: map[string][]string{
				"function": {"method_declaration"},
				"class":    {"class_declaration"},
				"type":     {"interface_declaration"},
			},
			identifiers: idID("type_identifier"),
		},

		// --- Tier 2: bug-fix — grammars were registered, node maps were not ---
		"tsx": {
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
				"type":     {"interface_declaration", "type_alias_declaration"},
			},
			identifiers: idID("type_identifier", "property_identifier"),
		},
		"c": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"struct_specifier"},
				"type":     {"enum_specifier", "union_specifier", "type_definition"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"cpp": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_specifier", "struct_specifier"},
				"type":     {"enum_specifier", "union_specifier", "type_definition", "namespace_definition"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"ruby": {
			nodes: map[string][]string{
				"function": {"method", "singleton_method"},
				"class":    {"class", "module"},
			},
			identifiers: idID("constant"),
		},

		// --- Tier 3: mainstream additions, high confidence in node names ---
		"c_sharp": {
			nodes: map[string][]string{
				"function": {"local_function_statement"},
				"class":    {"class_declaration"},
				"method":   {"method_declaration"},
				"type":     {"interface_declaration", "struct_declaration", "enum_declaration", "record_declaration"},
			},
			identifiers: idID("type_identifier"),
		},
		"php": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_declaration"},
				"method":   {"method_declaration"},
				"type":     {"interface_declaration", "trait_declaration"},
			},
			identifiers: idID("name", "variable_name"),
		},
		"swift": {
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"class_declaration"},
				"type":     {"protocol_declaration"},
			},
			identifiers: idID("simple_identifier", "type_identifier"),
		},
		"kotlin": {
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"class_declaration", "object_declaration"},
			},
			identifiers: idID("type_identifier", "simple_identifier"),
		},
		"scala": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_definition", "object_definition"},
				"type":     {"trait_definition"},
			},
			identifiers: idID("type_identifier"),
		},
		"bash": {
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID("variable_name", "word"),
		},
		"lua": {
			nodes: map[string][]string{
				"function": {"function_declaration", "function_definition"},
			},
			identifiers: idID(),
		},
		"dart": {
			nodes: map[string][]string{
				"function": {"function_signature"},
				"class":    {"class_definition"},
				"method":   {"method_signature"},
				"type":     {"mixin_declaration", "extension_declaration"},
			},
			identifiers: idID("type_identifier"),
		},
		"r": {
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID(),
		},
		"objc": {
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_interface", "class_implementation"},
				"method":   {"method_definition"},
				"type":     {"protocol_declaration"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},

		// --- Tier 4: markup / data / config with structural nodes ---
		"html": {
			nodes: map[string][]string{
				"type": {"doctype"},
			},
			identifiers: nil,
		},
		"css": {
			nodes: map[string][]string{
				"class": {"rule_set"},
			},
			identifiers: nil,
		},
		"scss": {
			nodes: map[string][]string{
				"function": {"mixin_statement"},
				"class":    {"rule_set"},
			},
			identifiers: nil,
		},
		"sql": {
			// DerekStride/tree-sitter-sql node names (verified against the
			// grammar; differ from the gotreesitter bundle's *_statement names).
			nodes: map[string][]string{
				"function": {"create_function"},
				"type":     {"create_table", "create_view", "create_type", "create_index", "create_materialized_view"},
			},
			identifiers: nil,
		},
		"markdown": {
			nodes: map[string][]string{
				// `section` already wraps the heading + body in
				// tree-sitter-markdown — adding `atx_heading` would emit
				// duplicate one-line chunks for every `### foo` line.
				"type": {"section"},
			},
			identifiers: nil,
		},

		// --- Tier 5: medium-confidence additions ---
		"zig": {
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"struct_declaration"},
			},
			identifiers: idID(),
		},
		"julia": {
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID(),
		},
		"fortran": {
			nodes: map[string][]string{
				"function": {"subroutine", "function"},
				"class":    {"module"},
			},
			identifiers: idID(),
		},
		"haskell": {
			nodes: map[string][]string{
				// `function` = untyped top-level def; `bind` = typed binding
				// (signature + match together); `signature` is loose stand-alone
				// type signatures.
				"function": {"function", "bind", "signature"},
				"type":     {"data_type", "newtype"},
			},
			identifiers: map[string]struct{}{
				"variable": {}, "constructor": {}, "name": {},
			},
		},
		"ocaml": {
			nodes: map[string][]string{
				"function": {"value_definition"},
				"class":    {"module_definition"},
				"type":     {"type_definition"},
			},
			identifiers: idID("type_identifier"),
		},
		"solidity": {
			nodes: map[string][]string{
				"function": {"function_definition", "modifier_definition", "constructor_definition", "fallback_receive_definition"},
				"class":    {"contract_declaration", "library_declaration"},
				"type":     {"interface_declaration", "struct_declaration", "enum_declaration", "event_definition"},
			},
			identifiers: idID(),
		},
	}
}

// skipNames mirrors SKIP_NAMES in chunker.py.
var skipNames = map[string]struct{}{
	// Python
	"self": {}, "cls": {}, "None": {}, "True": {}, "False": {}, "print": {},
	"len": {}, "range": {}, "type": {}, "list": {}, "dict": {}, "set": {},
	"tuple": {}, "int": {}, "str": {}, "float": {}, "bool": {}, "bytes": {},
	"object": {}, "Exception": {}, "isinstance": {}, "hasattr": {}, "getattr": {},
	"setattr": {},
	// JS/TS
	"undefined": {}, "null": {}, "true": {}, "false": {}, "console": {},
	"window": {}, "document": {}, "Array": {}, "Object": {}, "String": {},
	"Number": {}, "Boolean": {}, "Promise": {}, "Map": {}, "Set": {},
	// Go
	"nil": {}, "fmt": {}, "err": {}, "ctx": {},
	// Rust
	"Ok": {}, "Err": {}, "Some": {},
	// Common
	"this": {}, "super": {}, "void": {},
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// Chunk is a single code chunk extracted from a file.
// Field names and semantics match Python CodeChunk.
type Chunk struct {
	Content         string
	ChunkType       string // function|class|method|type|module|block
	FilePath        string
	StartLine       int // 1-based
	EndLine         int // 1-based
	Language        string
	SymbolName      *string
	SymbolSignature *string
	ParentName      *string
}

// Reference is an identifier usage found during AST walk.
// Mirrors Python ReferenceInfo.
type Reference struct {
	Name     string
	FilePath string
	Line     int // 1-based
	Col      int // 0-based
	Language string
}

// ---------------------------------------------------------------------------
// ChunkFile — main entry point
// ---------------------------------------------------------------------------

// ChunkFile chunks content using tree-sitter when a grammar is available, and
// falls back to sliding-window chunking for unsupported languages. The maxSize
// parameter controls per-chunk character limit; pass 0 to use the default.
func ChunkFile(filePath, content, language string, maxSize int) ([]Chunk, []Reference, error) {
	return ChunkFileTokens(filePath, content, language, maxSize, nil, 0)
}

// ChunkFileTokens is ChunkFile with a token budget.
//
// maxSize (bytes) has always been a stand-in for a token limit: the default
// 4500 is "1500 tokens x 3 bytes", a ratio that holds for dense ASCII code and
// falls apart everywhere else — Cyrillic or CJK comments cost two to three
// bytes per character, so a byte-sized chunk carries far fewer tokens than
// intended, while minified JavaScript packs far more.
//
// When budget is non-nil and reports exact counts, the size decision is made
// in tokens instead, and an over-budget chunk is cut on real token boundaries
// (via Budget.SplitPoints) rather than on a byte count. maxTokens <= 0 uses
// defaultMaxChunkTokens. A nil or estimating budget keeps the byte path
// unchanged, so nothing about existing behaviour depends on a provider
// having a tokenizer.
func ChunkFileTokens(filePath, content, language string, maxSize int, budget tokenizer.Budget, maxTokens int) ([]Chunk, []Reference, error) {
	if maxSize <= 0 {
		maxSize = maxChunkSize
	}
	if budget != nil && !budget.ExactCounts() {
		// An estimate is what the byte path already is; do not pretend
		// otherwise by routing through the token splitter.
		budget = nil
	}
	if budget != nil {
		if maxTokens <= 0 {
			maxTokens = DefaultMaxChunkTokens
		}
		if lim := budget.MaxInputTokens(); lim > 0 && maxTokens > lim {
			maxTokens = lim
		}
	}
	// With a token budget the byte cap must not fire first: it is the very
	// bias being removed (a byte limit cuts Cyrillic or CJK three times
	// sooner than ASCII for the same token cost). Let the inner path emit
	// whole semantic units and bound them in tokens afterwards.
	innerMax := maxSize
	if budget != nil {
		innerMax = math.MaxInt32
	}

	chunks, refs, err := chunkWithTreesitter(filePath, content, language, innerMax)
	if err != nil {
		// Fallback: sliding window, no references.
		return chunkFallbackTokens(filePath, content, language, budget, maxTokens), nil, nil
	}
	if len(chunks) == 0 {
		return chunkFallbackTokens(filePath, content, language, budget, maxTokens), nil, nil
	}
	return boundTokens(chunks, budget, maxTokens), refs, nil
}

// boundTokens enforces the token budget over chunks from ANY path — the
// tree-sitter one, the bash regex extractor, or the sliding-window fallback.
// Applying it here rather than inside the tree-sitter path is deliberate:
// minified JavaScript and files with no grammar are exactly the inputs that
// reach the fallback, and they are also the ones most likely to blow the
// model's input limit. A budget that only covered the happy path would miss
// them.
func boundTokens(chunks []Chunk, budget tokenizer.Budget, maxTokens int) []Chunk {
	if budget == nil || maxTokens <= 0 {
		return chunks
	}
	out := make([]Chunk, 0, len(chunks))
	for _, c := range chunks {
		// A byte-level BPE token can never cover less than one byte, so a
		// chunk shorter than the budget provably fits and needs no counting
		// at all. Most chunks are, so this skips the tokenizer on the hot
		// path rather than paying for an answer already known.
		if len(c.Content) <= maxTokens {
			out = append(out, c)
			continue
		}
		if budget.CountTokens(c.Content) > maxTokens {
			out = append(out, splitChunkTokens(c, budget, maxTokens)...)
			continue
		}
		out = append(out, c)
	}
	return out
}

// chunkFallback returns reasonable chunks for content that the tree-sitter
// path could not handle (parser timeout, no grammar, malformed input, …).
//
// For languages where a regex-based extractor exists (currently only bash),
// we try that first — it produces real `function` chunks instead of generic
// `block` ones, which is much more useful for semantic search. If the
// extractor returns nil (no symbols found), we fall through to the universal
// sliding-window strategy so the file content is still indexed.
func chunkFallback(filePath, content, language string) []Chunk {
	if language == "bash" {
		if c := bashRegexChunks(filePath, content); len(c) > 0 {
			return c
		}
	}
	return chunkSlidingWindow(filePath, content, language)
}

// chunkFallbackTokens is chunkFallback with a token budget: the sliding window
// walks token boundaries instead of a fixed byte count.
//
// The byte window is 4000 bytes regardless of what those bytes contain, so a
// file of Cyrillic or CJK prose — two to three bytes per character — produced
// windows worth a third of the intended tokens, and this is the path such
// files take, because "no grammar" and "not ASCII" go together often enough to
// matter. boundTokens alone could not fix it: it splits chunks that are too
// large and has no way to grow ones that are too small.
func chunkFallbackTokens(filePath, content, language string, budget tokenizer.Budget, maxTokens int) []Chunk {
	if budget == nil || maxTokens <= 0 {
		return chunkFallback(filePath, content, language)
	}
	if language == "bash" {
		if c := bashRegexChunks(filePath, content); len(c) > 0 {
			return boundTokens(c, budget, maxTokens)
		}
	}
	if len(content) == 0 {
		return nil
	}
	// One chunk, then let the token splitter cut it — same code path, same
	// guarantees (pieces are substrings, none over budget, line numbers
	// tracked) as every other over-budget chunk in this package.
	whole := Chunk{
		Content:   content,
		ChunkType: "block",
		FilePath:  filePath,
		StartLine: 1,
		EndLine:   countNewlines(content) + 1,
		Language:  language,
	}
	if budget.CountTokens(content) <= maxTokens {
		return []Chunk{whole}
	}
	return splitChunkTokens(whole, budget, maxTokens)
}

// ---------------------------------------------------------------------------
// Tree-sitter path
// ---------------------------------------------------------------------------

// minifiedMaxLineLen flags a file as minified when any single line exceeds it.
// Hand-written code essentially never has 2 KB lines; minified/bundled JS and
// CSS routinely pack the whole file into one. Applied only to the web-asset
// languages where minification exists — long lines in other languages (e.g.
// generated Go with embedded literals) still parse fine and stay on the AST path.
const minifiedMaxLineLen = 2048

// looksMinified reports whether a JS/TS/CSS-family file is minified or bundled
// output: a ".min."-style name, or any line longer than minifiedMaxLineLen.
func looksMinified(path, content, language string) bool {
	switch language {
	case "javascript", "typescript", "tsx", "css", "scss":
	default:
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, ".min.") || strings.HasSuffix(base, ".bundle.js") {
		return true
	}
	lineStart := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			if i-lineStart > minifiedMaxLineLen {
				return true
			}
			lineStart = i + 1
		}
	}
	return len(content)-lineStart > minifiedMaxLineLen
}

func chunkWithTreesitter(filePath, content, language string, maxSize int) ([]Chunk, []Reference, error) {
	// Snapshot under RLock so a concurrent Configure() call does not race the read.
	registryMu.RLock()
	export, ok := languageRegistry[language]
	nodeKinds := languageNodes[language]
	idTypes := identifierNodes[language]
	registryMu.RUnlock()

	if !ok {
		return chunkFallback(filePath, content, language), nil, nil
	}
	if nodeKinds == nil {
		// Grammar exists but we don't have node definitions → sliding window.
		return chunkFallback(filePath, content, language), nil, nil
	}
	if looksMinified(filePath, content, language) {
		// Minified/bundled sources are the parser's pathological case: a
		// 500 KB single-line bundle yields a huge tree, balloons the wasm
		// instance to its memory cap, and forces a pool recycle — all to
		// produce AST chunks with near-zero semantic-search value. Skip
		// straight to the sliding window.
		return chunkFallback(filePath, content, language), nil, nil
	}

	// Build flat target → kind map.
	targetTypes := map[string]string{}
	for kind, types := range nodeKinds {
		for _, t := range types {
			targetTypes[t] = kind
		}
	}

	src := []byte(content)
	nodes, err := tswasm.ParseNodes(export, src)
	if err != nil {
		// Unknown grammar export or a contained guest trap (e.g. memory cap) →
		// fall back to sliding window so the file is still indexed.
		slog.Warn("chunker: wasm parse failed, falling back to sliding window",
			"path", filePath, "language", language, "err", err)
		return chunkFallback(filePath, content, language), nil, nil
	}
	if len(nodes) == 0 {
		return chunkFallback(filePath, content, language), nil, nil
	}
	tree := buildFlatTree(nodes)

	lines := splitLines(content)
	var chunks []Chunk
	var coveredRanges [][2]int

	extractNodes(tree, 0, src, targetTypes, lines, filePath, language, &chunks, &coveredRanges, nil)

	// Extract references using the snapshotted identifier set.
	refs := extractReferences(tree, src, targetTypes, idTypes, filePath, language)

	// Fill gaps between extracted symbol nodes with "module" chunks.
	sortRanges(coveredRanges)
	gaps := findGaps(coveredRanges, len(lines))
	for _, g := range gaps {
		start, end := g[0], g[1]
		gapContent := joinLines(lines[start : end+1])
		if trimSpace(gapContent) != "" {
			chunks = append(chunks, Chunk{
				Content:   gapContent,
				ChunkType: "module",
				FilePath:  filePath,
				StartLine: start + 1,
				EndLine:   end + 1,
				Language:  language,
			})
		}
	}

	// Split oversized chunks.
	var finalChunks []Chunk
	for _, c := range chunks {
		if len(c.Content) > maxSize {
			finalChunks = append(finalChunks, splitChunk(c, maxSize)...)
		} else {
			finalChunks = append(finalChunks, c)
		}
	}

	if len(finalChunks) == 0 {
		return chunkFallback(filePath, content, language), nil, nil
	}
	return finalChunks, refs, nil
}

// flatTree is a lightweight tree over the flat pre-order []tswasm.Node from the
// batched walk. children/parents are reconstructed once from each node's depth
// so the extraction below can navigate like the old *sitter.Node tree.
type flatTree struct {
	nodes    []tswasm.Node
	children [][]int32
	parents  []int32
}

func buildFlatTree(nodes []tswasm.Node) *flatTree {
	ft := &flatTree{
		nodes:    nodes,
		children: make([][]int32, len(nodes)),
		parents:  make([]int32, len(nodes)),
	}
	stack := make([]int32, 0, 32) // current ancestor chain (pre-order DFS)
	for i := range nodes {
		d := nodes[i].Depth
		for len(stack) > 0 && ft.nodes[stack[len(stack)-1]].Depth >= d {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			p := stack[len(stack)-1]
			ft.children[p] = append(ft.children[p], int32(i))
			ft.parents[i] = p
		} else {
			ft.parents[i] = -1
		}
		stack = append(stack, int32(i))
	}
	return ft
}

// extractNodes walks the AST and appends symbol chunks.
func extractNodes(
	ft *flatTree,
	idx int,
	src []byte,
	targetTypes map[string]string,
	lines []string,
	filePath, language string,
	chunks *[]Chunk,
	coveredRanges *[][2]int,
	parentName *string,
) {
	node := ft.nodes[idx]

	if kind, ok := targetTypes[node.Kind]; ok {
		// Pull the declaration's doc comment into its chunk. Without this,
		// the comment lands in the gap BETWEEN symbol chunks and the gap
		// filler emits it as a standalone micro "module" chunk — a generated
		// file like openapi.gen.go produced 377 comment-only chunks of ~60 B
		// each. Attached, the comment both disappears as noise and improves
		// the chunk's embedding (the "what is this" prose sits with its code).
		startLine := int(leadingCommentStart(ft, idx))
		declLine := int(node.StartRow)
		endLine := int(node.EndRow)

		content := joinLines(lines[startLine : endLine+1])

		// Promote function→method when inside a class.
		actualKind := kind
		if kind == "function" && parentName != nil {
			actualKind = "method"
		}

		symName := extractName(ft, idx, src)
		var sig *string
		if declLine < len(lines) {
			// Signature is the DECLARATION line, not the comment the chunk
			// may now start with.
			s := trimSpace(lines[declLine])
			sig = &s
		}

		*chunks = append(*chunks, Chunk{
			Content:         content,
			ChunkType:       actualKind,
			FilePath:        filePath,
			StartLine:       startLine + 1,
			EndLine:         endLine + 1,
			Language:        language,
			SymbolName:      symName,
			SymbolSignature: sig,
			ParentName:      parentName,
		})
		*coveredRanges = append(*coveredRanges, [2]int{startLine, endLine})

		// For class nodes recurse children with class name as parent.
		if kind == "class" {
			currentParent := symName
			if currentParent == nil {
				currentParent = parentName
			}
			for _, c := range ft.children[idx] {
				extractNodes(ft, int(c), src, targetTypes, lines, filePath, language, chunks, coveredRanges, currentParent)
			}
			return
		}
	}

	for _, c := range ft.children[idx] {
		extractNodes(ft, int(c), src, targetTypes, lines, filePath, language, chunks, coveredRanges, parentName)
	}
}

// leadingCommentStart returns the chunk start row for the node at idx,
// extended upward over any directly-adjacent leading comment siblings — the
// doc comment(s) of the declaration. Tree-sitter marks comments as "extra"
// nodes (the Extra flag survives the flat dump), so this is language-agnostic:
// Go doc comments, JSDoc blocks, Rust ///, Python # headers all qualify.
//
// Adjacency is strict: each comment must END on the line directly above
// where the chunk currently starts. A blank line between the comment and the
// declaration breaks the chain — that comment is free-standing prose and
// stays in the surrounding module gap (e.g. a section banner).
// The comment usually neighbours a WRAPPER of the target node rather than the
// node itself — Go's type_spec sits inside type_declaration, and the doc
// comment is the declaration's sibling. So when no comment is found among the
// node's own siblings, climb through ancestors that start on the same row
// (such single-row wrappers are pure syntax shells) and look again.
func leadingCommentStart(ft *flatTree, idx int) uint32 {
	declRow := ft.nodes[idx].StartRow
	start := declRow
	n := idx
	for {
		p := ft.parents[n]
		if p < 0 {
			return start
		}
		sibs := ft.children[p]
		pos := -1
		for i, c := range sibs {
			if int(c) == n {
				pos = i
				break
			}
		}
		for i := pos - 1; i >= 0; i-- {
			s := ft.nodes[sibs[i]]
			// Adjacent = the comment ends on the line directly above the
			// current start, OR on the start line itself — some grammars
			// (tree-sitter-rust line_comment) consume the trailing newline,
			// so a `/// doc` above `fn foo` ends ON foo's row.
			if !s.Extra || (s.EndRow+1 != start && s.EndRow != start) {
				break
			}
			start = s.StartRow
		}
		if start != declRow {
			return start // found the doc comment chain at this level
		}
		if ft.nodes[p].StartRow != declRow {
			return start // parent is not a same-row wrapper — stop climbing
		}
		n = int(p)
	}
}

// extractReferences walks the tree collecting identifier usages (not
// definitions). idNodeTypes is passed in (rather than read from the global map)
// so callers can snapshot once and stay consistent if Configure() is called
// concurrently.
func extractReferences(
	ft *flatTree,
	src []byte,
	targetTypes map[string]string,
	idNodeTypes map[string]struct{},
	filePath, language string,
) []Reference {
	if len(idNodeTypes) == 0 {
		return nil
	}

	var refs []Reference
	seen := map[[3]any]struct{}{}

	for i := range ft.nodes {
		n := ft.nodes[i]
		if _, isID := idNodeTypes[n.Kind]; !isID {
			continue
		}
		name := string(src[n.StartByte:n.EndByte])
		if len(name) < minRefNameLength {
			continue
		}
		if _, skip := skipNames[name]; skip {
			continue
		}
		// Skip if this identifier is the name child of a definition node — i.e.
		// the FIRST identifier child of a target (definition) parent.
		if p := ft.parents[i]; p >= 0 {
			if _, isTarget := targetTypes[ft.nodes[p].Kind]; isTarget {
				isDefName := false
				for _, c := range ft.children[p] {
					if _, childIsID := idNodeTypes[ft.nodes[c].Kind]; childIsID {
						isDefName = int(c) == i
						break
					}
				}
				if isDefName {
					continue
				}
			}
		}

		line := int(n.StartRow) + 1
		col := int(n.StartCol)
		key := [3]any{name, line, col}
		if _, dup := seen[key]; !dup {
			seen[key] = struct{}{}
			refs = append(refs, Reference{
				Name:     name,
				FilePath: filePath,
				Line:     line,
				Col:      col,
				Language: language,
			})
		}
	}
	return refs
}

// extractName returns the first identifier-like child's text, or nil.
//
// The set of "identifier-like" node types covers the main grammars in the
// default registry. Notable additions beyond the obvious `identifier`:
//   - `field_identifier` — Go method names (`func (b *Bar) Foo()` → "Foo")
//   - `word` — bash function names (`hello() { ... }` → "hello")
//   - `simple_identifier` — Swift / Kotlin function names
//   - `constant` — Ruby class/module names (which start with uppercase)
//
// Without these, the symbol_name field on the resulting chunk was nil and
// the CLI's `cix summary` rendered weird placeholders (`[method] bool`,
// `[function] <nil>`).
func extractName(ft *flatTree, idx int, src []byte) *string {
	nameTypes := map[string]struct{}{
		"identifier":          {},
		"name":                {},
		"property_identifier": {},
		"type_identifier":     {},
		"field_identifier":    {},
		"word":                {},
		"simple_identifier":   {},
		"constant":            {},
	}
	for _, c := range ft.children[idx] {
		child := ft.nodes[c]
		if _, ok := nameTypes[child.Kind]; ok {
			s := string(src[child.StartByte:child.EndByte])
			return &s
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sliding-window fallback
// ---------------------------------------------------------------------------

func chunkSlidingWindow(filePath, content, language string) []Chunk {
	if len(content) == 0 {
		return nil
	}

	var chunks []Chunk
	currentPos := 0

	for currentPos < len(content) {
		endPos := currentPos + windowSize
		if endPos > len(content) {
			endPos = len(content)
		}
		chunkContent := content[currentPos:endPos]

		startLine := countNewlines(content[:currentPos]) + 1
		endLine := countNewlines(content[:endPos]) + 1

		chunks = append(chunks, Chunk{
			Content:   chunkContent,
			ChunkType: "block",
			FilePath:  filePath,
			StartLine: startLine,
			EndLine:   endLine,
			Language:  language,
		})

		if endPos >= len(content) {
			break
		}
		currentPos = endPos - overlap
	}
	return chunks
}

// ---------------------------------------------------------------------------
// Chunk splitting
// ---------------------------------------------------------------------------

// splitChunk cuts an oversized chunk into pieces of <= maxSize chars.
//
// Only the FIRST piece keeps the original SymbolName/SymbolSignature/
// ChunkType — subsequent pieces become anonymous `block` chunks. Without
// this, splitting a long function would create N rows in the symbol index
// all claiming to be `func run()`, making `cix def run` return N
// duplicates pointing at different line ranges of the same symbol.
//
// The full text of the symbol is still indexed (both for FTS and embed
// search) — just attributed to the symbol only via its first chunk.
func splitChunk(chunk Chunk, maxSize int) []Chunk {
	lines := splitLines(chunk.Content)
	var subChunks []Chunk
	var currentLines []string
	currentStart := chunk.StartLine

	emit := func(content string, startLine, endLine int, isFirst bool) {
		c := Chunk{
			Content:    content,
			FilePath:   chunk.FilePath,
			StartLine:  startLine,
			EndLine:    endLine,
			Language:   chunk.Language,
			ParentName: chunk.ParentName,
		}
		if isFirst {
			c.ChunkType = chunk.ChunkType
			c.SymbolName = chunk.SymbolName
			c.SymbolSignature = chunk.SymbolSignature
		} else {
			c.ChunkType = "block"
		}
		subChunks = append(subChunks, c)
	}

	for _, line := range lines {
		currentLines = append(currentLines, line)
		currentContent := joinLines(currentLines)
		if len(currentContent) >= maxSize && len(currentLines) > 1 {
			splitContent := joinLines(currentLines[:len(currentLines)-1])
			emit(splitContent,
				currentStart,
				currentStart+len(currentLines)-2,
				len(subChunks) == 0)
			currentStart = currentStart + len(currentLines) - 1
			currentLines = []string{line}
		}
	}

	if len(currentLines) > 0 {
		emit(joinLines(currentLines),
			currentStart,
			chunk.EndLine,
			len(subChunks) == 0)
	}
	return subChunks
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	b := make([]byte, 0, total)
	for i, l := range lines {
		b = append(b, l...)
		if i < len(lines)-1 {
			b = append(b, '\n')
		}
	}
	return string(b)
}

func countNewlines(s string) int {
	n := 0
	for _, c := range []byte(s) {
		if c == '\n' {
			n++
		}
	}
	return n
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func findGaps(covered [][2]int, totalLines int) [][2]int {
	if totalLines == 0 {
		return nil
	}
	if len(covered) == 0 {
		return [][2]int{{0, totalLines - 1}}
	}
	var gaps [][2]int
	prevEnd := -1
	for _, r := range covered {
		start, end := r[0], r[1]
		if start > prevEnd+1 {
			gaps = append(gaps, [2]int{prevEnd + 1, start - 1})
		}
		if end > prevEnd {
			prevEnd = end
		}
	}
	if prevEnd < totalLines-1 {
		gaps = append(gaps, [2]int{prevEnd + 1, totalLines - 1})
	}
	return gaps
}

func sortRanges(ranges [][2]int) {
	// insertion sort — typically small slices
	for i := 1; i < len(ranges); i++ {
		j := i
		for j > 0 && ranges[j][0] < ranges[j-1][0] {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
			j--
		}
	}
}

// DefaultMaxChunkTokens is the token equivalent of maxChunkSize. The byte
// default was written as 1500*3 — a 1500-token target at three bytes each —
// so the token target is that same 1500, now expressed in the unit that
// actually matters.
//
// Exported so config.go can use it as the CIX_MAX_CHUNK_TOKENS default rather
// than repeating the number: two copies of a chunk-size default drifting apart
// is the exact failure this change removes for maxChunkSize.
const DefaultMaxChunkTokens = 1500

// splitChunkTokens cuts an over-budget chunk into pieces of <= maxTokens.
//
// It keeps splitChunk's attribution rule: only the first piece inherits
// SymbolName/ChunkType, the rest become `block`, so one long function does not
// produce N symbol rows all claiming to be it.
//
// The cut positions come from Budget.SplitPoints over the WHOLE content rather
// than from summing per-line counts. Per-line summing is off by the separators
// — joining lines reinserts newlines, and a newline plus the next line's
// indentation forms its own pre-token — so a budget of 1500 produced chunks of
// up to 1546 tokens on real files, roughly one extra token per line boundary.
// SplitPoints is exact by construction, so the bound actually holds.
//
// Each exact cut is then pulled BACK to the nearest line start, because a chunk
// that begins mid-line reads badly in search results and its line range lies.
// Moving a cut backwards only ever shrinks the piece before it, so the budget
// survives the adjustment. A line longer than the whole budget has no earlier
// boundary to snap to; there the exact cut stands and the line is split
// internally — which is the case the byte splitter could not handle at all.
func splitChunkTokens(chunk Chunk, budget tokenizer.Budget, maxTokens int) []Chunk {
	content := chunk.Content
	var out []Chunk
	pos, line := 0, chunk.StartLine
	var cuts []int // absolute offsets into content; nil means "recompute"

	for pos < len(content) {
		if cuts == nil {
			rel, _ := budget.SplitPoints(content[pos:], maxTokens)
			if len(rel) == 0 {
				out = append(out, mkPiece(chunk, content[pos:], line, len(out) == 0))
				break
			}
			cuts = make([]int, 0, len(rel))
			for _, r := range rel {
				cuts = append(cuts, pos+r)
			}
		}

		at := cuts[0]
		// Pull the cut back to a line start so a piece never begins mid-line:
		// search results and the stored line range both lie otherwise. Moving
		// backwards only shrinks this piece, so it stays inside the budget. A
		// line wider than the whole budget has no earlier boundary — there the
		// exact cut stands and the line is split internally, which is the case
		// the byte splitter could not handle at all.
		snapped := at
		if nl := strings.LastIndexByte(content[pos:at], '\n'); nl >= 0 {
			snapped = pos + nl + 1
		}
		if snapped <= pos {
			snapped = at
		}

		piece := content[pos:snapped]
		out = append(out, mkPiece(chunk, piece, line, len(out) == 0))
		line += strings.Count(piece, "\n")
		pos = snapped

		if snapped == at {
			// The boundary landed where the tokenizer put it, so the cuts
			// after it are still valid and can be consumed without another
			// pass. This is what keeps a 66 KB single line — where the
			// newline snap never fires — from costing one full scan per
			// piece.
			cuts = cuts[1:]
			if len(cuts) == 0 {
				cuts = nil
			}
			continue
		}
		// Snapping moved the boundary: every later cut was measured from a
		// start that no longer exists, and reusing them would hand the next
		// piece the tokens this one gave up. Recompute.
		cuts = nil
	}
	return out
}

// mkPiece builds one output chunk, preserving splitChunk's attribution rule:
// only the first piece keeps SymbolName/ChunkType, so a long function does not
// produce N symbol rows all claiming to be it.
func mkPiece(src Chunk, content string, startLine int, first bool) Chunk {
	c := Chunk{
		Content:    content,
		FilePath:   src.FilePath,
		StartLine:  startLine,
		EndLine:    startLine + strings.Count(strings.TrimSuffix(content, "\n"), "\n"),
		Language:   src.Language,
		ParentName: src.ParentName,
	}
	if first {
		c.ChunkType = src.ChunkType
		c.SymbolName = src.SymbolName
		c.SymbolSignature = src.SymbolSignature
	} else {
		c.ChunkType = "block"
	}
	return c
}
