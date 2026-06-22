package chunker

import (
	"strings"
	"testing"
)

func TestLooksMinified(t *testing.T) {
	longLine := strings.Repeat("var a=1;", 600) // ~4800 chars, one line
	cases := []struct {
		name     string
		path     string
		content  string
		language string
		want     bool
	}{
		{"min.js by name", "/p/vendor/jquery.min.js", "var a=1;\n", "javascript", true},
		{"min.css by name", "/p/static/site.min.css", "body{}\n", "css", true},
		{"bundle.js by name", "/p/dist/app.bundle.js", "var a=1;\n", "javascript", true},
		{"long single line js", "/p/dist/out.js", longLine, "javascript", true},
		{"long line ts no trailing newline", "/p/gen/types.ts", "const x = 1;\n" + longLine, "typescript", true},
		{"normal ts", "/p/src/app.ts", "export function f(): number { return 1; }\n", "typescript", false},
		{"long line in go ignored", "/p/gen/openapi.gen.go", longLine, "go", false},
		{"normal css", "/p/src/site.css", ".a { color: red; }\n", "css", false},
	}
	for _, tc := range cases {
		if got := looksMinified(tc.path, tc.content, tc.language); got != tc.want {
			t.Errorf("%s: looksMinified = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestChunkFile_MinifiedFallsBack verifies minified input bypasses the AST path
// entirely and is still indexed via sliding-window "block" chunks.
func TestChunkFile_MinifiedFallsBack(t *testing.T) {
	content := strings.Repeat("function f(){return 1};", 300) // one ~7 KB line
	chunks, refs, err := ChunkFile("/p/dist/app.min.js", content, "javascript", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("expected no references for minified file, got %d", len(refs))
	}
	if len(chunks) == 0 {
		t.Fatal("minified file must still produce sliding-window chunks")
	}
	for _, c := range chunks {
		if c.ChunkType != "block" {
			t.Errorf("expected only block chunks, got %q", c.ChunkType)
		}
	}
}
