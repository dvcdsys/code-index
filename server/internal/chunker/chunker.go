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
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
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

// parseBudget caps wall-clock time spent in tree-sitter for a single file.
// Some grammars (notably bash) have catastrophic-backtracking pathologies on
// specific inputs — install.sh in this very repo took 31s to parse before
// this guard. The parser's own SetTimeoutMicros checkpoint is best-effort
// and overshoots by 3-4×, so we set the hint generously and rely on the
// post-parse wall-clock check to decide whether to keep the tree.
//
// On overshoot we fall back to sliding-window chunks. We accept the wasted
// CPU (parser keeps running until its next checkpoint) because killing a
// pure-Go parse from outside is not safe — the only practical levers are
// SetTimeoutMicros and the cancellation flag, both with the same overshoot
// characteristic.
const (
	parseBudget = 2 * time.Second
	parseHint   = uint64(parseBudget / time.Microsecond)
)

// ---------------------------------------------------------------------------
// Language registry — built from defaultRegistry() at init() and reduced by
// Configure() if the operator set CIX_LANGUAGES. The three exported maps
// stay package-private; the engine reads them directly.
// ---------------------------------------------------------------------------

// languageEntry bundles the three pieces of state a language needs.
type languageEntry struct {
	factory     languageFunc
	nodes       map[string][]string  // function|class|method|type → AST node types
	identifiers map[string]struct{}  // identifier leaf-node types for ref extraction
}

// languageFunc is a factory for sitter.Language.
type languageFunc func() *sitter.Language

var (
	registryMu       sync.RWMutex
	languageRegistry map[string]languageFunc
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

	reg := make(map[string]languageFunc, len(defaults))
	nodes := make(map[string]map[string][]string, len(defaults))
	idents := make(map[string]map[string]struct{}, len(defaults))

	for lang, entry := range defaults {
		if !wantAll {
			if _, ok := wanted[lang]; !ok {
				continue
			}
		}
		reg[lang] = entry.factory
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
			factory: grammars.PythonLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_definition"},
			},
			identifiers: idID(),
		},
		"typescript": {
			factory: grammars.TypescriptLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
				"type":     {"interface_declaration", "type_alias_declaration"},
			},
			identifiers: idID("type_identifier", "property_identifier"),
		},
		"javascript": {
			factory: grammars.JavascriptLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
			},
			identifiers: idID("property_identifier"),
		},
		"go": {
			factory: grammars.GoLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"method":   {"method_declaration"},
				"type":     {"type_spec"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"rust": {
			factory: grammars.RustLanguage,
			nodes: map[string][]string{
				"function": {"function_item"},
				"class":    {"struct_item", "enum_item"},
				"type":     {"trait_item"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"java": {
			factory: grammars.JavaLanguage,
			nodes: map[string][]string{
				"function": {"method_declaration"},
				"class":    {"class_declaration"},
				"type":     {"interface_declaration"},
			},
			identifiers: idID("type_identifier"),
		},

		// --- Tier 2: bug-fix — grammars were registered, node maps were not ---
		"tsx": {
			factory: grammars.TsxLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration", "arrow_function"},
				"class":    {"class_declaration"},
				"method":   {"method_definition"},
				"type":     {"interface_declaration", "type_alias_declaration"},
			},
			identifiers: idID("type_identifier", "property_identifier"),
		},
		"c": {
			factory: grammars.CLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"struct_specifier"},
				"type":     {"enum_specifier", "union_specifier", "type_definition"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"cpp": {
			factory: grammars.CppLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_specifier", "struct_specifier"},
				"type":     {"enum_specifier", "union_specifier", "type_definition", "namespace_definition"},
			},
			identifiers: idID("type_identifier", "field_identifier"),
		},
		"ruby": {
			factory: grammars.RubyLanguage,
			nodes: map[string][]string{
				"function": {"method", "singleton_method"},
				"class":    {"class", "module"},
			},
			identifiers: idID("constant"),
		},

		// --- Tier 3: mainstream additions, high confidence in node names ---
		"c_sharp": {
			factory: grammars.CSharpLanguage,
			nodes: map[string][]string{
				"function": {"local_function_statement"},
				"class":    {"class_declaration"},
				"method":   {"method_declaration"},
				"type":     {"interface_declaration", "struct_declaration", "enum_declaration", "record_declaration"},
			},
			identifiers: idID("type_identifier"),
		},
		"php": {
			factory: grammars.PhpLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_declaration"},
				"method":   {"method_declaration"},
				"type":     {"interface_declaration", "trait_declaration"},
			},
			identifiers: idID("name", "variable_name"),
		},
		"swift": {
			factory: grammars.SwiftLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"class_declaration"},
				"type":     {"protocol_declaration"},
			},
			identifiers: idID("simple_identifier", "type_identifier"),
		},
		"kotlin": {
			factory: grammars.KotlinLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"class_declaration", "object_declaration"},
			},
			identifiers: idID("type_identifier", "simple_identifier"),
		},
		"scala": {
			factory: grammars.ScalaLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
				"class":    {"class_definition", "object_definition"},
				"type":     {"trait_definition"},
			},
			identifiers: idID("type_identifier"),
		},
		"bash": {
			factory: grammars.BashLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID("variable_name", "word"),
		},
		"lua": {
			factory: grammars.LuaLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration", "function_definition"},
			},
			identifiers: idID(),
		},
		"dart": {
			factory: grammars.DartLanguage,
			nodes: map[string][]string{
				"function": {"function_signature"},
				"class":    {"class_definition"},
				"method":   {"method_signature"},
				"type":     {"mixin_declaration", "extension_declaration"},
			},
			identifiers: idID("type_identifier"),
		},
		"r": {
			factory: grammars.RLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID(),
		},
		"objc": {
			factory: grammars.ObjcLanguage,
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
			factory: grammars.HtmlLanguage,
			nodes: map[string][]string{
				"type": {"doctype"},
			},
			identifiers: nil,
		},
		"css": {
			factory: grammars.CssLanguage,
			nodes: map[string][]string{
				"class": {"rule_set"},
			},
			identifiers: nil,
		},
		"scss": {
			factory: grammars.ScssLanguage,
			nodes: map[string][]string{
				"function": {"mixin_statement"},
				"class":    {"rule_set"},
			},
			identifiers: nil,
		},
		"sql": {
			factory: grammars.SqlLanguage,
			nodes: map[string][]string{
				"function": {"create_function_statement"},
				"type":     {"create_table_statement"},
			},
			identifiers: nil,
		},
		"markdown": {
			factory: grammars.MarkdownLanguage,
			nodes: map[string][]string{
				"type": {"section", "atx_heading"},
			},
			identifiers: nil,
		},

		// --- Tier 5: medium-confidence additions ---
		"zig": {
			factory: grammars.ZigLanguage,
			nodes: map[string][]string{
				"function": {"function_declaration"},
				"class":    {"struct_declaration"},
			},
			identifiers: idID(),
		},
		"julia": {
			factory: grammars.JuliaLanguage,
			nodes: map[string][]string{
				"function": {"function_definition"},
			},
			identifiers: idID(),
		},
		"fortran": {
			factory: grammars.FortranLanguage,
			nodes: map[string][]string{
				"function": {"subroutine", "function"},
				"class":    {"module"},
			},
			identifiers: idID(),
		},
		"haskell": {
			factory: grammars.HaskellLanguage,
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
			factory: grammars.OcamlLanguage,
			nodes: map[string][]string{
				"function": {"value_definition"},
				"class":    {"module_definition"},
				"type":     {"type_definition"},
			},
			identifiers: idID("type_identifier"),
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
	if maxSize <= 0 {
		maxSize = maxChunkSize
	}
	chunks, refs, err := chunkWithTreesitter(filePath, content, language, maxSize)
	if err != nil {
		// Fallback: sliding window, no references.
		return chunkFallback(filePath, content, language), nil, nil
	}
	return chunks, refs, nil
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

// ---------------------------------------------------------------------------
// Tree-sitter path
// ---------------------------------------------------------------------------

func chunkWithTreesitter(filePath, content, language string, maxSize int) ([]Chunk, []Reference, error) {
	// Snapshot under RLock so a concurrent Configure() call does not race the read.
	registryMu.RLock()
	langFn, ok := languageRegistry[language]
	nodeKinds := languageNodes[language]
	idTypes := identifierNodes[language]
	registryMu.RUnlock()

	if !ok {
		return chunkFallback(filePath, content, language), nil, nil
	}
	lang := langFn()
	if lang == nil {
		return chunkFallback(filePath, content, language), nil, nil
	}

	if nodeKinds == nil {
		// Grammar exists but we don't have node definitions → sliding window.
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
	parser := sitter.NewParser(lang)

	// Twin guards: SetTimeoutMicros is the parser's own checkpoint-based
	// budget; the cancellation flag is set by an external timer when the
	// wall-clock deadline expires. The parser checks both at the same
	// granularity, so they overshoot together — we still rely on the
	// post-parse wall-clock check below to decide whether the tree is
	// trustworthy.
	parser.SetTimeoutMicros(parseHint)
	var cancelFlag uint32
	parser.SetCancellationFlag(&cancelFlag)
	deadline := time.AfterFunc(parseBudget, func() {
		atomic.StoreUint32(&cancelFlag, 1)
	})

	parseStart := time.Now()
	tree, err := parser.Parse(src)
	parseElapsed := time.Since(parseStart)
	deadline.Stop()

	// Hard wall-clock check — even if parser claims success, a tree that
	// took >2× the budget is the result of a backtracking pathology and
	// the structure is not trustworthy enough to chunk on. Falling back to
	// sliding window keeps the indexer responsive.
	if parseElapsed > 2*parseBudget {
		slog.Warn("chunker: parse exceeded budget, falling back to sliding window",
			"path", filePath, "language", language, "elapsed", parseElapsed,
			"budget", parseBudget)
		return chunkFallback(filePath, content, language), nil, nil
	}
	if atomic.LoadUint32(&cancelFlag) == 1 {
		slog.Warn("chunker: parse cancelled by deadline, falling back to sliding window",
			"path", filePath, "language", language, "elapsed", parseElapsed)
		return chunkFallback(filePath, content, language), nil, nil
	}

	if err != nil {
		return nil, nil, err
	}
	root := tree.RootNode()
	if root == nil {
		return nil, nil, nil
	}

	lines := splitLines(content)
	var chunks []Chunk
	var coveredRanges [][2]int

	extractNodes(root, lang, src, targetTypes, lines, filePath, language, &chunks, &coveredRanges, nil)

	// Extract references using the snapshotted identifier set.
	refs := extractReferences(root, lang, src, targetTypes, idTypes, filePath, language)

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

// extractNodes walks the AST and appends symbol chunks.
func extractNodes(
	node *sitter.Node,
	lang *sitter.Language,
	src []byte,
	targetTypes map[string]string,
	lines []string,
	filePath, language string,
	chunks *[]Chunk,
	coveredRanges *[][2]int,
	parentName *string,
) {
	if node == nil {
		return
	}
	nodeType := node.Type(lang)

	if kind, ok := targetTypes[nodeType]; ok {
		startLine := int(node.StartPoint().Row)
		endLine := int(node.EndPoint().Row)

		content := joinLines(lines[startLine : endLine+1])

		// Promote function→method when inside a class.
		actualKind := kind
		if kind == "function" && parentName != nil {
			actualKind = "method"
		}

		symName := extractName(node, lang, src)
		var sig *string
		if startLine < len(lines) {
			s := trimSpace(lines[startLine])
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
			cnt := node.ChildCount()
			for i := 0; i < cnt; i++ {
				extractNodes(node.Child(i), lang, src, targetTypes, lines, filePath, language, chunks, coveredRanges, currentParent)
			}
			return
		}
	}

	cnt := node.ChildCount()
	for i := 0; i < cnt; i++ {
		extractNodes(node.Child(i), lang, src, targetTypes, lines, filePath, language, chunks, coveredRanges, parentName)
	}
}

// extractReferences walks AST collecting identifier usages (not definitions).
// idNodeTypes is passed in (rather than read from the global map) so callers
// can snapshot once and stay consistent if Configure() is called concurrently.
func extractReferences(
	root *sitter.Node,
	lang *sitter.Language,
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

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		nt := n.Type(lang)
		if _, isID := idNodeTypes[nt]; isID {
			name := n.Text(src)
			if len(name) >= minRefNameLength {
				if _, skip := skipNames[name]; !skip {
					// Skip if this identifier is the name child of a definition node.
					parent := n.Parent()
					if parent != nil {
						if _, isTarget := targetTypes[parent.Type(lang)]; isTarget {
							// Check if this is the first identifier child.
							// We use StartByte as a stable node identity (within one parse).
							nStart := n.StartByte()
							cnt := parent.ChildCount()
							for i := 0; i < cnt; i++ {
								child := parent.Child(i)
								if child == nil {
									continue
								}
								if _, childIsID := idNodeTypes[child.Type(lang)]; childIsID {
									if child.StartByte() == nStart {
										return // skip — it's a definition name
									}
									break
								}
							}
						}
					}

					line := int(n.StartPoint().Row) + 1
					col := int(n.StartPoint().Column)
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
			}
			return // leaf — no children to recurse
		}

		cnt := n.ChildCount()
		for i := 0; i < cnt; i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
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
func extractName(node *sitter.Node, lang *sitter.Language, src []byte) *string {
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
	cnt := node.ChildCount()
	for i := 0; i < cnt; i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if _, ok := nameTypes[child.Type(lang)]; ok {
			s := child.Text(src)
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

func splitChunk(chunk Chunk, maxSize int) []Chunk {
	lines := splitLines(chunk.Content)
	var subChunks []Chunk
	var currentLines []string
	currentStart := chunk.StartLine

	for _, line := range lines {
		currentLines = append(currentLines, line)
		currentContent := joinLines(currentLines)
		if len(currentContent) >= maxSize && len(currentLines) > 1 {
			splitContent := joinLines(currentLines[:len(currentLines)-1])
			subChunks = append(subChunks, Chunk{
				Content:         splitContent,
				ChunkType:       chunk.ChunkType,
				FilePath:        chunk.FilePath,
				StartLine:       currentStart,
				EndLine:         currentStart + len(currentLines) - 2,
				Language:        chunk.Language,
				SymbolName:      chunk.SymbolName,
				SymbolSignature: chunk.SymbolSignature,
				ParentName:      chunk.ParentName,
			})
			currentStart = currentStart + len(currentLines) - 1
			currentLines = []string{line}
		}
	}

	if len(currentLines) > 0 {
		subChunks = append(subChunks, Chunk{
			Content:         joinLines(currentLines),
			ChunkType:       chunk.ChunkType,
			FilePath:        chunk.FilePath,
			StartLine:       currentStart,
			EndLine:         chunk.EndLine,
			Language:        chunk.Language,
			SymbolName:      chunk.SymbolName,
			SymbolSignature: chunk.SymbolSignature,
			ParentName:      chunk.ParentName,
		})
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
