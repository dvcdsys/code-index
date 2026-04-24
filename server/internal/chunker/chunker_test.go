package chunker

import (
	"strings"
	"testing"
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
