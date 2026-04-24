// Package chunker ports api/app/services/chunker.py to Go using gotreesitter.
// The public surface is ChunkFile, which returns ([]Chunk, []Reference, error).
// Sliding-window fallback is used when a language is not supported by the
// tree-sitter grammars bundle or when parsing fails.
package chunker

import (
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

// ---------------------------------------------------------------------------
// Language maps — ported 1:1 from chunker.py
// ---------------------------------------------------------------------------

// languageNodes maps language → kind → []node_type.
// Kind values: function|class|method|type.
var languageNodes = map[string]map[string][]string{
	"python": {
		"function": {"function_definition"},
		"class":    {"class_definition"},
	},
	"typescript": {
		"function": {"function_declaration", "arrow_function"},
		"class":    {"class_declaration"},
		"method":   {"method_definition"},
		"type":     {"interface_declaration", "type_alias_declaration"},
	},
	"javascript": {
		"function": {"function_declaration", "arrow_function"},
		"class":    {"class_declaration"},
		"method":   {"method_definition"},
	},
	"go": {
		"function": {"function_declaration"},
		"method":   {"method_declaration"},
		"type":     {"type_spec"},
	},
	"rust": {
		"function": {"function_item"},
		"class":    {"struct_item", "enum_item"},
		"type":     {"trait_item"},
	},
	"java": {
		"function": {"method_declaration"},
		"class":    {"class_declaration"},
		"type":     {"interface_declaration"},
	},
}

// identifierNodes maps language → set of identifier leaf-node types.
var identifierNodes = map[string]map[string]struct{}{
	"python":     {"identifier": {}},
	"typescript": {"identifier": {}, "type_identifier": {}, "property_identifier": {}},
	"javascript": {"identifier": {}, "property_identifier": {}},
	"go":         {"identifier": {}, "type_identifier": {}, "field_identifier": {}},
	"rust":       {"identifier": {}, "type_identifier": {}, "field_identifier": {}},
	"java":       {"identifier": {}, "type_identifier": {}},
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
// Language registry
// ---------------------------------------------------------------------------

// languageFunc is a factory for sitter.Language.
type languageFunc func() *sitter.Language

var languageRegistry = map[string]languageFunc{
	"python":     grammars.PythonLanguage,
	"go":         grammars.GoLanguage,
	"javascript": grammars.JavascriptLanguage,
	"typescript": grammars.TypescriptLanguage,
	"tsx":        grammars.TsxLanguage,
	"java":       grammars.JavaLanguage,
	"c":          grammars.CLanguage,
	"cpp":        grammars.CppLanguage,
	"rust":       grammars.RustLanguage,
	"ruby":       grammars.RubyLanguage,
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
		return chunkSlidingWindow(filePath, content, language), nil, nil
	}
	return chunks, refs, nil
}

// ---------------------------------------------------------------------------
// Tree-sitter path
// ---------------------------------------------------------------------------

func chunkWithTreesitter(filePath, content, language string, maxSize int) ([]Chunk, []Reference, error) {
	langFn, ok := languageRegistry[language]
	if !ok {
		return chunkSlidingWindow(filePath, content, language), nil, nil
	}
	lang := langFn()
	if lang == nil {
		return chunkSlidingWindow(filePath, content, language), nil, nil
	}

	nodeKinds, ok := languageNodes[language]
	if !ok {
		// Grammar exists but we don't have node definitions → sliding window.
		return chunkSlidingWindow(filePath, content, language), nil, nil
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
	tree, err := parser.Parse(src)
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

	// Extract references.
	refs := extractReferences(root, lang, src, targetTypes, filePath, language)

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
		return chunkSlidingWindow(filePath, content, language), nil, nil
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
func extractReferences(
	root *sitter.Node,
	lang *sitter.Language,
	src []byte,
	targetTypes map[string]string,
	filePath, language string,
) []Reference {
	idNodeTypes, ok := identifierNodes[language]
	if !ok {
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
func extractName(node *sitter.Node, lang *sitter.Language, src []byte) *string {
	nameTypes := map[string]struct{}{
		"identifier":          {},
		"name":                {},
		"property_identifier": {},
		"type_identifier":     {},
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
