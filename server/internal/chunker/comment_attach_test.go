package chunker

import (
	"strings"
	"testing"
)

// TestLeadingCommentAttachment verifies that a declaration's doc comment is
// pulled INTO the declaration's chunk instead of being stranded in the gap
// between symbols (where the gap filler used to emit it as a standalone
// micro "module" chunk — openapi.gen.go produced 377 such 60-byte chunks).
func TestLeadingCommentAttachment(t *testing.T) {
	src := `package m

// Foo does foo things.
// Second doc line.
func Foo() int { return 1 }

// Bar is a bar. (sibling of type_declaration, two levels above type_spec)
type Bar struct{ X int }

// Standalone banner — blank line below breaks adjacency.

func Baz() int { return 2 }
`
	chunks, _, err := ChunkFile("/p/a.go", src, "go", 0)
	if err != nil {
		t.Fatal(err)
	}

	find := func(name string) *Chunk {
		for i := range chunks {
			if chunks[i].SymbolName != nil && *chunks[i].SymbolName == name {
				return &chunks[i]
			}
		}
		t.Fatalf("symbol %q not found in chunks", name)
		return nil
	}

	foo := find("Foo")
	if !strings.HasPrefix(foo.Content, "// Foo does foo things.") {
		t.Errorf("Foo chunk must start with its doc comment, got %q", foo.Content)
	}
	if foo.SymbolSignature == nil || !strings.HasPrefix(*foo.SymbolSignature, "func Foo") {
		t.Errorf("signature must stay the declaration line, got %v", foo.SymbolSignature)
	}

	// Doc comment of a type reaches the chunk through the type_declaration
	// wrapper (the comment is NOT a direct sibling of type_spec).
	bar := find("Bar")
	if !strings.Contains(bar.Content, "// Bar is a bar.") {
		t.Errorf("Bar chunk must contain its doc comment, got %q", bar.Content)
	}

	// A blank line between comment and declaration breaks the chain: the
	// banner stays OUT of Baz's chunk (it remains module-gap content).
	baz := find("Baz")
	if strings.Contains(baz.Content, "Standalone banner") {
		t.Errorf("banner comment separated by a blank line must not attach, got %q", baz.Content)
	}

	// And no gap chunk should consist of Foo's doc comment alone.
	for _, c := range chunks {
		if c.ChunkType == "module" && strings.TrimSpace(c.Content) == "// Foo does foo things.\n// Second doc line." {
			t.Errorf("doc comment leaked into a standalone module chunk")
		}
	}
}

// TestLeadingCommentAttachment_Languages verifies the attachment works across
// grammars — the mechanism is language-agnostic (comments carry tree-sitter's
// "extra" flag in every grammar; declaration wrappers are climbed by
// same-row), but each language nests declarations differently, so each gets a
// smoke case: the doc comment must land inside a NON-module chunk.
func TestLeadingCommentAttachment_Languages(t *testing.T) {
	cases := []struct {
		lang, path, src, marker string
	}{
		{
			"typescript", "/p/a.ts",
			"/** Greets the user. */\nexport function greet(name: string): string {\n  return \"hi \" + name;\n}\n",
			"/** Greets the user. */",
		},
		{
			// C line + block comments; function_definition is a direct
			// sibling of the comment.
			"c", "/p/a.c",
			"/* frobnicates the widget */\nint frob(void) { return 1; }\n",
			"/* frobnicates the widget */",
		},
		{
			// C struct behind a typedef: type_definition wraps
			// struct_specifier — exercises the wrapper climb.
			"c", "/p/b.c",
			"/** Doxygen: widget state. */\ntypedef struct {\n  int x;\n} widget_t;\n",
			"/** Doxygen: widget state. */",
		},
		{
			"python", "/p/a.py",
			"# helper used by the frobnicator\ndef helper():\n    return 1\n",
			"# helper used by the frobnicator",
		},
		{
			"rust", "/p/a.rs",
			"/// Does the foo thing.\nfn foo() -> i32 { 1 }\n",
			"/// Does the foo thing.",
		},
		{
			"java", "/p/A.java",
			"/** Java doc. */\npublic class A {\n  void m() {}\n}\n",
			"/** Java doc. */",
		},
	}
	for _, tc := range cases {
		chunks, _, err := ChunkFile(tc.path, tc.src, tc.lang, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.lang, err)
		}
		attached := false
		for _, c := range chunks {
			if c.ChunkType != "module" && c.ChunkType != "block" && strings.Contains(c.Content, tc.marker) {
				attached = true
			}
		}
		if !attached {
			t.Errorf("%s (%s): doc comment %q not attached to a symbol chunk; chunks: %+v",
				tc.lang, tc.path, tc.marker, chunks)
		}
	}
}
