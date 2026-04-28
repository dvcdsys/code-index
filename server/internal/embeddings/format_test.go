package embeddings

import (
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/chunker"
)

func ptr(s string) *string { return &s }

func TestFormatChunkForEmbedding_Disabled(t *testing.T) {
	c := chunker.Chunk{
		Content:   "func main() {}",
		ChunkType: "function",
		Language:  "go",
	}
	got := FormatChunkForEmbedding(c, "cmd/main.go", false)
	want := "function: func main() {}"
	if got != want {
		t.Errorf("includePath=false: got %q, want %q", got, want)
	}
}

func TestFormatChunkForEmbedding_EmptyRelPath(t *testing.T) {
	c := chunker.Chunk{
		Content:   "x := 1",
		ChunkType: "module",
		Language:  "go",
	}
	got := FormatChunkForEmbedding(c, "", true)
	want := "module: x := 1"
	if got != want {
		t.Errorf("empty relPath: got %q, want %q", got, want)
	}
}

func TestFormatChunkForEmbedding_FunctionWithSymbol(t *testing.T) {
	c := chunker.Chunk{
		Content:    "func semanticSearchHandler() {}",
		ChunkType:  "function",
		Language:   "go",
		SymbolName: ptr("semanticSearchHandler"),
	}
	got := FormatChunkForEmbedding(c, "server/internal/httpapi/search.go", true)
	wantContains := []string{
		"File: server/internal/httpapi/search.go",
		"Language: go",
		"function: semanticSearchHandler",
		"func semanticSearchHandler() {}",
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\nfull output:\n%s", w, got)
		}
	}
}

func TestFormatChunkForEmbedding_ModuleChunkOmitsSymbol(t *testing.T) {
	// Module chunks have no symbol and SymbolName is nil; ensure we don't
	// emit a "module: " line. Even if a symbol leaks in, module/block kinds
	// must not produce a symbol preamble line (would add path-correlated
	// noise to gap-filler chunks).
	c := chunker.Chunk{
		Content:    "import \"fmt\"",
		ChunkType:  "module",
		Language:   "go",
		SymbolName: ptr("Anything"),
	}
	got := FormatChunkForEmbedding(c, "main.go", true)
	if strings.Contains(got, "module:") {
		t.Errorf("module chunk should not produce 'module:' preamble, got:\n%s", got)
	}
	if !strings.Contains(got, "File: main.go") {
		t.Errorf("expected File: line, got:\n%s", got)
	}
}

func TestFormatChunkForEmbedding_OmitsLangWhenEmpty(t *testing.T) {
	c := chunker.Chunk{
		Content:   "raw text",
		ChunkType: "module",
	}
	got := FormatChunkForEmbedding(c, "README", true)
	if strings.Contains(got, "Language:") {
		t.Errorf("empty Language should not emit Language: line, got:\n%s", got)
	}
}

func TestFormatChunkForEmbedding_PreservesContentBytes(t *testing.T) {
	// The raw chunk content must appear in the output unchanged — the
	// preamble is additive, never lossy.
	c := chunker.Chunk{
		Content:   "line1\nline2\n  indented\n",
		ChunkType: "function",
		Language:  "go",
	}
	got := FormatChunkForEmbedding(c, "x.go", true)
	if !strings.HasSuffix(got, c.Content) {
		t.Errorf("output must end with raw content; got:\n%s", got)
	}
}
