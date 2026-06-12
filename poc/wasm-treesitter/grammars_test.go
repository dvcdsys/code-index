package wasmts

import (
	"context"
	"testing"
)

// minimal valid-ish snippet per base grammar — enough to exercise load + parse +
// per-language symbol-table resolution through the batched path. We assert the
// language loads, parsing does not trap, and produces nodes; per-language SYMBOL
// correctness (which kinds = function/class/...) is the next phase.
var smoke = []struct {
	id  string
	src string
}{
	{"python", "def f():\n    pass\n"},
	{"typescript", "function f(){}\n"},
	{"tsx", "const x = 1;\n"},
	{"javascript", "function f(){}\n"},
	{"go", "package m\nfunc F(){}\n"},
	{"rust", "fn f(){}\n"},
	{"java", "class C{}\n"},
	{"c", "int f(){return 0;}\n"},
	{"cpp", "int f(){return 0;}\n"},
	{"ruby", "def f\nend\n"},
	{"c_sharp", "class C{}\n"},
	{"php", "<?php function f(){}\n"},
	{"swift", "func f(){}\n"},
	{"kotlin", "fun f(){}\n"},
	{"scala", "object O{}\n"},
	{"bash", "f(){ echo hi; }\n"},
	{"lua", "function f() end\n"},
	{"dart", "void f(){}\n"},
	{"r", "f <- function() {}\n"},
	{"objc", "int f(){return 0;}\n"},
	{"html", "<div></div>\n"},
	{"css", "a{color:red}\n"},
	{"scss", "a{color:red}\n"},
	{"sql", "SELECT 1;\n"},
	{"markdown", "# Title\n"},
	{"zig", "fn f() void {}\n"},
	{"julia", "function f() end\n"},
	{"fortran", "program p\nend program p\n"},
	{"haskell", "main = return ()\n"},
	{"ocaml", "let f () = ()\n"},
	{"solidity", "contract C {}\n"},
}

func TestAllGrammarsLoad(t *testing.T) {
	eng, err := New(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if len(smoke) != 31 {
		t.Fatalf("expected 31 grammars in smoke set, got %d", len(smoke))
	}

	for _, c := range smoke {
		t.Run(c.id, func(t *testing.T) {
			nodes, err := eng.ParseNodes("tree_sitter_"+c.id, []byte(c.src))
			if err != nil {
				t.Fatalf("parse trapped: %v", err)
			}
			if len(nodes) == 0 {
				t.Fatal("no nodes produced")
			}
			// the root node must carry a resolved kind name (symbol table works)
			if nodes[0].Kind == "" {
				t.Errorf("root kind unresolved (symbol table empty?)")
			}
		})
	}
}
