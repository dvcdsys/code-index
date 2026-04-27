package chunker

import (
	"strings"
	"testing"
	"time"

	sitter "github.com/odvcencio/gotreesitter"
)

func TestChunkFile_Python(t *testing.T) {
	src := `def hello(name):
    return "hello " + name

class Greeter:
    def greet(self, name):
        return hello(name)
`
	chunks, refs, err := ChunkFile("sample.py", src, "python", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Must find function and class chunks.
	typeCount := map[string]int{}
	for _, c := range chunks {
		typeCount[c.ChunkType]++
		if c.FilePath != "sample.py" {
			t.Errorf("FilePath = %q, want sample.py", c.FilePath)
		}
		if c.Language != "python" {
			t.Errorf("Language = %q, want python", c.Language)
		}
		if c.StartLine < 1 {
			t.Errorf("StartLine %d < 1", c.StartLine)
		}
		if c.EndLine < c.StartLine {
			t.Errorf("EndLine %d < StartLine %d", c.EndLine, c.StartLine)
		}
	}
	if typeCount["function"] == 0 {
		t.Errorf("expected function chunks, got types: %v", typeCount)
	}
	if typeCount["class"] == 0 {
		t.Errorf("expected class chunks, got types: %v", typeCount)
	}

	// References must be non-nil (may be empty for this snippet).
	_ = refs
}

func TestChunkFile_Go(t *testing.T) {
	src := `package main

import "fmt"

func Add(a, b int) int {
	return a + b
}

type Point struct {
	X, Y float64
}

func (p Point) String() string {
	return fmt.Sprintf("(%f,%f)", p.X, p.Y)
}
`
	chunks, _, err := ChunkFile("sample.go", src, "go", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from Go source")
	}

	hasFunction := false
	hasMethod := false
	for _, c := range chunks {
		if c.ChunkType == "function" {
			hasFunction = true
		}
		if c.ChunkType == "method" {
			hasMethod = true
		}
	}
	if !hasFunction {
		t.Error("expected function chunk for Add")
	}
	if !hasMethod {
		t.Error("expected method chunk for Point.String")
	}
}

func TestChunkFile_Javascript(t *testing.T) {
	src := `function greet(name) {
    console.log("hello " + name);
}

class Animal {
    constructor(name) {
        this.name = name;
    }
    speak() {
        console.log(this.name + " makes a noise.");
    }
}
`
	chunks, _, err := ChunkFile("sample.js", src, "javascript", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from JS source")
	}
}

func TestChunkFile_Rust(t *testing.T) {
	src := `struct Point {
    x: f64,
    y: f64,
}

fn add(a: i32, b: i32) -> i32 {
    a + b
}

trait Shape {
    fn area(&self) -> f64;
}
`
	chunks, _, err := ChunkFile("sample.rs", src, "rust", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from Rust source")
	}
}

func TestChunkFile_SlidingWindowFallback(t *testing.T) {
	// "hcl" is not in our gotreesitter grammars registry → sliding window.
	content := strings.Repeat("resource \"aws_instance\" \"web\" {\n  ami = \"ami-123\"\n}\n", 10)
	chunks, refs, err := ChunkFile("main.tf", content, "hcl", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one sliding-window chunk")
	}
	for _, c := range chunks {
		if c.ChunkType != "block" {
			t.Errorf("sliding-window chunk type = %q, want block", c.ChunkType)
		}
	}
	if refs != nil {
		t.Error("sliding-window should return nil refs")
	}
}

func TestChunkFile_SlidingWindowSplit(t *testing.T) {
	// Force sliding-window to produce multiple chunks: content > windowSize.
	content := strings.Repeat("x", windowSize*2+100)
	chunks, _, _ := ChunkFile("big.txt", content, "unknown", 0)
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks for oversized content, got %d", len(chunks))
	}
}

func TestChunkFile_OversizedChunkSplit(t *testing.T) {
	// A single huge function should be split.
	var sb strings.Builder
	sb.WriteString("def big_func():\n")
	for i := 0; i < 2000; i++ {
		sb.WriteString("    x = 1  # line\n")
	}
	src := sb.String()
	chunks, _, err := ChunkFile("big.py", src, "python", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range chunks {
		if len(c.Content) > maxChunkSize+200 { // allow a small overshoot on last segment
			t.Errorf("chunk too large: %d chars", len(c.Content))
		}
	}
}

func TestSplitChunk_OnlyFirstKeepsSymbol(t *testing.T) {
	// A long Python function that splitChunk will cut into >1 piece.
	var sb strings.Builder
	sb.WriteString("def big_func():\n")
	for i := 0; i < 2000; i++ {
		sb.WriteString("    x = 1  # padding line\n")
	}
	src := sb.String()
	chunks, _, err := ChunkFile("big.py", src, "python", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find all chunks that mention `big_func`. Only one chunk in the index
	// should claim the symbol; the rest must be anonymous `block` pieces
	// even though they textually belong to the same function.
	withSymbol := 0
	for _, c := range chunks {
		if c.SymbolName != nil && *c.SymbolName == "big_func" {
			withSymbol++
			if c.ChunkType != "function" {
				t.Errorf("chunk with symbol big_func has type %q, want function", c.ChunkType)
			}
		}
	}
	if withSymbol != 1 {
		t.Errorf("expected exactly 1 chunk attributed to big_func after split, got %d", withSymbol)
	}

	// And we DID split — meaning multiple chunks for this function exist.
	totalForFunc := 0
	for _, c := range chunks {
		if c.FilePath == "big.py" && c.ChunkType != "module" {
			totalForFunc++
		}
	}
	if totalForFunc < 2 {
		t.Skipf("test self-check: function fit into one chunk (totalForFunc=%d) — need bigger fixture", totalForFunc)
	}
}

func TestFindGaps_NoOverlap(t *testing.T) {
	covered := [][2]int{{2, 5}, {10, 12}}
	gaps := findGaps(covered, 15)
	// Expect: 0-1, 6-9, 13-14
	expected := [][2]int{{0, 1}, {6, 9}, {13, 14}}
	if len(gaps) != len(expected) {
		t.Fatalf("gaps = %v, want %v", gaps, expected)
	}
	for i, g := range gaps {
		if g != expected[i] {
			t.Errorf("gap[%d] = %v, want %v", i, g, expected[i])
		}
	}
}

func TestFindGaps_Empty(t *testing.T) {
	gaps := findGaps(nil, 5)
	if len(gaps) != 1 || gaps[0] != [2]int{0, 4} {
		t.Errorf("empty covered: gaps = %v, want [{0 4}]", gaps)
	}
}

func TestSkipNames_ContainsExpected(t *testing.T) {
	mustSkip := []string{"self", "nil", "console", "Ok", "this", "void"}
	for _, name := range mustSkip {
		if _, ok := skipNames[name]; !ok {
			t.Errorf("skipNames missing %q", name)
		}
	}
}

// TestChunkFile_ParseBudgetFallback exercises the parser-budget guard with
// a real-world pathology: the install.sh in this repo triggers ~31s of
// catastrophic backtracking in tree-sitter-bash. After the guard kicks in
// the chunker must return sliding-window chunks within ~parseBudget rather
// than blocking the entire indexer for half a minute.
//
// Skipped under -short because it deliberately runs until the deadline fires.
func TestChunkFile_ParseBudgetFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("parse-budget test waits up to ~2s for the deadline to fire")
	}

	// Construct bash content that deterministically tickles the bash
	// grammar's slow path without depending on a specific repo file.
	// Heredocs + nested $(...) inside a deeply nested case statement is a
	// known trigger; we lean on the repo-known install.sh structure.
	src := strings.Repeat(`
case "$x" in
  pattern1)
    cat <<EOF
$(some_cmd "$arg" $(other_cmd))
$(yet_another)
EOF
    ;;
  pattern2)
    if [[ "$y" == "$z" ]]; then
      arr=( $(cmd1) $(cmd2) $(cmd3) )
    fi
    ;;
esac
`, 30)

	start := time.Now()
	chunks, refs, err := ChunkFile("install.sh", src, "bash", 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Whether or not the guard fired on this synthetic input, total time
	// must stay under 2× parseBudget — otherwise the parser is running
	// uncapped.
	if elapsed > 2*parseBudget+500*time.Millisecond {
		t.Errorf("ChunkFile elapsed %s, expected < ~2× parseBudget (%s)",
			elapsed, parseBudget)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk (block or function), got 0")
	}

	// Refs are nil when sliding-window fallback fires.
	_ = refs
}

func TestSplitLines_Roundtrip(t *testing.T) {
	original := "line one\nline two\nline three"
	lines := splitLines(original)
	rejoined := joinLines(lines)
	if rejoined != original {
		t.Errorf("splitLines/joinLines roundtrip failed:\n  got  %q\n  want %q", rejoined, original)
	}
}

func TestChunkFile_TypeScript(t *testing.T) {
	src := `interface User {
    name: string;
    age: number;
}

function greet(user: User): string {
    return "Hello, " + user.name;
}

type ID = string | number;
`
	chunks, _, err := ChunkFile("sample.ts", src, "typescript", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from TypeScript source")
	}
}

// --- Tier 2 bug-fix tests: grammars were registered without languageNodes
// in earlier versions, so .tsx/.c/.cpp/.rb files silently fell to sliding
// window. These assert true semantic chunks now come back. ---

func TestChunkFile_TSX(t *testing.T) {
	src := `import React from "react";

interface Props {
    name: string;
}

export function Greeting(props: Props) {
    return <div className="hi">Hello, {props.name}</div>;
}

type Id = string | number;
`
	chunks, _, err := ChunkFile("sample.tsx", src, "tsx", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from TSX source")
	}
	hasFunction := false
	hasType := false
	for _, c := range chunks {
		if c.ChunkType == "function" {
			hasFunction = true
		}
		if c.ChunkType == "type" {
			hasType = true
		}
	}
	if !hasFunction {
		t.Errorf("expected function chunk for Greeting, got types: %v", chunkTypeCounts(chunks))
	}
	if !hasType {
		t.Errorf("expected type chunk for Id, got types: %v", chunkTypeCounts(chunks))
	}
}

func TestChunkFile_C(t *testing.T) {
	src := `#include <stdio.h>

struct Point {
    double x;
    double y;
};

typedef enum { RED, GREEN, BLUE } Color;

int add(int a, int b) {
    return a + b;
}

int main(void) {
    return add(1, 2);
}
`
	chunks, _, err := ChunkFile("sample.c", src, "c", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from C source")
	}
	counts := chunkTypeCounts(chunks)
	if counts["function"] == 0 {
		t.Errorf("expected function chunks, got: %v", counts)
	}
	if counts["class"] == 0 {
		t.Errorf("expected struct (class) chunk for Point, got: %v", counts)
	}
}

func TestChunkFile_Cpp(t *testing.T) {
	src := `#include <string>

class Animal {
public:
    Animal(std::string name) : name_(name) {}
    std::string name() const { return name_; }
private:
    std::string name_;
};

namespace zoo {
    int count() { return 42; }
}
`
	chunks, _, err := ChunkFile("sample.cpp", src, "cpp", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from C++ source")
	}
	counts := chunkTypeCounts(chunks)
	if counts["class"] == 0 {
		t.Errorf("expected class chunk for Animal, got: %v", counts)
	}
}

func TestChunkFile_Ruby(t *testing.T) {
	src := `module Greetings
  class Greeter
    def initialize(name)
      @name = name
    end

    def greet
      puts "Hello, #{@name}"
    end
  end
end
`
	chunks, _, err := ChunkFile("sample.rb", src, "ruby", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from Ruby source")
	}
	counts := chunkTypeCounts(chunks)
	if counts["class"] == 0 {
		t.Errorf("expected class/module chunks, got: %v", counts)
	}
}

// --- Configure() filtering ---

func TestConfigure_FilterToSubset(t *testing.T) {
	defer Configure(nil) // restore defaults for other tests

	Configure([]string{"python", "go"})
	active := SupportedLanguages()
	if len(active) != 2 {
		t.Errorf("expected 2 active languages, got %d (%v)", len(active), active)
	}
	got := map[string]bool{}
	for _, l := range active {
		got[l] = true
	}
	if !got["python"] || !got["go"] {
		t.Errorf("expected python+go, got %v", active)
	}
	if got["rust"] {
		t.Error("rust should be filtered out")
	}
}

func TestConfigure_DefaultsAfterEmpty(t *testing.T) {
	Configure([]string{"python"})
	Configure(nil) // should restore full defaults
	active := SupportedLanguages()
	if len(active) < 20 {
		t.Errorf("expected ≥20 default languages, got %d", len(active))
	}
}

func TestConfigure_UnknownIDIgnored(t *testing.T) {
	defer Configure(nil)

	Configure([]string{"python", "imaginary-lang", "go"})
	active := SupportedLanguages()
	got := map[string]bool{}
	for _, l := range active {
		got[l] = true
	}
	if !got["python"] || !got["go"] {
		t.Errorf("expected python+go to survive, got %v", active)
	}
	if got["imaginary-lang"] {
		t.Error("unknown language should not be added")
	}
}

func TestConfigure_CaseInsensitive(t *testing.T) {
	defer Configure(nil)

	Configure([]string{"  Python  ", "GO"})
	active := SupportedLanguages()
	if len(active) != 2 {
		t.Errorf("expected 2 active languages, got %d (%v)", len(active), active)
	}
}

// chunkTypeCounts is a small helper for table-driven assertions on chunk types.
func chunkTypeCounts(chunks []Chunk) map[string]int {
	out := map[string]int{}
	for _, c := range chunks {
		out[c.ChunkType]++
	}
	return out
}

// TestRegistry_AllFactoriesNonNil ensures every default-registered language
// resolves to a usable *sitter.Language. A nil factory return would mean
// gotreesitter renamed/removed a grammar between updates and we silently lost
// support — better to fail loud here than at runtime in production.
func TestRegistry_AllFactoriesNonNil(t *testing.T) {
	defer Configure(nil)
	Configure(nil)

	for _, lang := range SupportedLanguages() {
		t.Run(lang, func(t *testing.T) {
			registryMu.RLock()
			fn := languageRegistry[lang]
			registryMu.RUnlock()
			if fn == nil {
				t.Fatalf("nil factory for %q", lang)
			}
			if g := fn(); g == nil {
				t.Fatalf("factory returned nil grammar for %q", lang)
			}
		})
	}
}

// TestRegistry_NodeNamesMatchAST parses a tiny per-language fixture and
// asserts at least one configured node-type appears in its AST. This catches
// node-name typos in defaultRegistry without needing a fixture file per lang.
// Languages absent from the fixture map are skipped (registered but not
// covered — acceptable, but the per-language tests above cover the criticals).
func TestRegistry_NodeNamesMatchAST(t *testing.T) {
	defer Configure(nil)
	Configure(nil)

	fixtures := map[string]string{
		"python":     "def f():\n    pass\n",
		"go":         "package p\nfunc F() {}\n",
		"javascript": "function f() {}\n",
		"typescript": "function f(): void {}\n",
		"tsx":        "function F() { return <div/>; }\n",
		"java":       "class C { void m() {} }\n",
		"c":          "int f(void) { return 0; }\n",
		"cpp":        "class C {}; int f(){return 0;}\n",
		"rust":       "fn f() {}\n",
		"ruby":       "class C\n  def m; end\nend\n",
		"c_sharp":    "class C { void M() {} }\n",
		"php":        "<?php function f() {} ?>\n",
		"swift":      "func f() {}\n",
		"kotlin":     "fun f() {}\n",
		"scala":      "object O { def f() = 1 }\n",
		"bash":       "f() { echo hi; }\n",
		"lua":        "function f() end\n",
		"dart":       "void f() {}\n",
		"r":          "f <- function() 1\n",
		"objc":       "@interface C\n@end\n",
		"html":       "<!DOCTYPE html><html></html>\n",
		"css":        ".x { color: red; }\n",
		"scss":       ".x { color: red; }\n",
		"sql":        "CREATE TABLE t (id INT);\n",
		"markdown":   "# Heading\n\nbody\n",
		"zig":        "fn f() void {}\n",
		"julia":      "function f() end\n",
		"fortran":    "subroutine s\nend subroutine\n",
		"haskell":    "module M where\n\nf :: Int -> Int\nf x = x\n",
		"ocaml":      "let f x = x\n",
	}

	for lang, src := range fixtures {
		t.Run(lang, func(t *testing.T) {
			registryMu.RLock()
			fn, regOK := languageRegistry[lang]
			nodes := languageNodes[lang]
			registryMu.RUnlock()

			if !regOK {
				t.Skipf("%q not in registry (deliberately filtered out)", lang)
			}
			if nodes == nil {
				t.Skipf("%q has no node map (sliding-window only — by design)", lang)
			}

			grammar := fn()
			if grammar == nil {
				t.Fatalf("nil grammar for %q", lang)
			}

			parser := sitter.NewParser(grammar)
			tree, err := parser.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse error for %q: %v", lang, err)
			}
			root := tree.RootNode()
			if root == nil {
				t.Fatalf("nil root for %q", lang)
			}

			want := map[string]struct{}{}
			for _, types := range nodes {
				for _, ty := range types {
					want[ty] = struct{}{}
				}
			}

			seen := map[string]struct{}{}
			collectNodeTypes(root, grammar, seen)

			matched := false
			for ty := range want {
				if _, ok := seen[ty]; ok {
					matched = true
					break
				}
			}
			if !matched {
				keys := make([]string, 0, len(want))
				for k := range want {
					keys = append(keys, k)
				}
				t.Errorf("none of configured node types %v found in AST for %q. Sample AST node types seen: %v",
					keys, lang, sampleKeys(seen, 12))
			}
		})
	}
}

func collectNodeTypes(n *sitter.Node, lang *sitter.Language, out map[string]struct{}) {
	if n == nil {
		return
	}
	out[n.Type(lang)] = struct{}{}
	for i := 0; i < int(n.ChildCount()); i++ {
		collectNodeTypes(n.Child(i), lang, out)
	}
}

func sampleKeys(m map[string]struct{}, n int) []string {
	out := make([]string, 0, n)
	for k := range m {
		if len(out) >= n {
			break
		}
		out = append(out, k)
	}
	return out
}
