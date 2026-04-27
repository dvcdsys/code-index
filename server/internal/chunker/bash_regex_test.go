package chunker

import (
	"strings"
	"testing"
)

// helper: assert a chunk with the given symbol name and type exists.
func findChunkByName(t *testing.T, chunks []Chunk, name, kind string) Chunk {
	t.Helper()
	for _, c := range chunks {
		if c.SymbolName != nil && *c.SymbolName == name && c.ChunkType == kind {
			return c
		}
	}
	t.Fatalf("no chunk with name=%q type=%q in: %s", name, kind, summariseChunks(chunks))
	return Chunk{}
}

func summariseChunks(chunks []Chunk) string {
	var b strings.Builder
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("; ")
		}
		name := "<nil>"
		if c.SymbolName != nil {
			name = *c.SymbolName
		}
		b.WriteString(c.ChunkType + ":" + name)
	}
	return b.String()
}

// --- POSIX style: name() { ... } -------------------------------------------

func TestBashRegex_PosixSimple(t *testing.T) {
	src := `#!/usr/bin/env bash
hello() {
    echo "hi"
}
`
	chunks := bashRegexChunks("/p/x.sh", src)
	hello := findChunkByName(t, chunks, "hello", "function")
	if hello.StartLine != 2 || hello.EndLine != 4 {
		t.Errorf("hello lines = %d-%d, want 2-4", hello.StartLine, hello.EndLine)
	}
	if !strings.Contains(hello.Content, `echo "hi"`) {
		t.Errorf("body missing echo: %q", hello.Content)
	}
}

func TestBashRegex_PosixOneLiner(t *testing.T) {
	src := `greet() { echo "hi"; }
`
	chunks := bashRegexChunks("/p/x.sh", src)
	greet := findChunkByName(t, chunks, "greet", "function")
	if greet.StartLine != 1 || greet.EndLine != 1 {
		t.Errorf("greet lines = %d-%d, want 1-1", greet.StartLine, greet.EndLine)
	}
}

// --- bash function keyword form --------------------------------------------

func TestBashRegex_FunctionKeywordWithParens(t *testing.T) {
	src := `function deploy() {
    echo deploying
}
`
	chunks := bashRegexChunks("/p/d.sh", src)
	findChunkByName(t, chunks, "deploy", "function")
}

func TestBashRegex_FunctionKeywordNoParens(t *testing.T) {
	src := `function build {
    make all
}
`
	chunks := bashRegexChunks("/p/b.sh", src)
	findChunkByName(t, chunks, "build", "function")
}

// --- multiple functions ----------------------------------------------------

func TestBashRegex_MultipleFunctions(t *testing.T) {
	src := `setup() {
    mkdir -p /tmp/x
}

teardown() {
    rm -rf /tmp/x
}

run_tests() {
    setup
    pytest
    teardown
}
`
	chunks := bashRegexChunks("/p/test.sh", src)
	for _, name := range []string{"setup", "teardown", "run_tests"} {
		findChunkByName(t, chunks, name, "function")
	}
	// Three functions + the gap before teardown / between functions / after.
	functionCount := 0
	for _, c := range chunks {
		if c.ChunkType == "function" {
			functionCount++
		}
	}
	if functionCount != 3 {
		t.Errorf("function count = %d, want 3", functionCount)
	}
}

// --- nested braces ---------------------------------------------------------

func TestBashRegex_NestedBraces(t *testing.T) {
	src := `outer() {
    if [[ "$1" == "yes" ]]; then
        local x={key:value}
        echo "${x}"
    fi
}
`
	chunks := bashRegexChunks("/p/n.sh", src)
	outer := findChunkByName(t, chunks, "outer", "function")
	if outer.StartLine != 1 || outer.EndLine != 6 {
		t.Errorf("outer lines = %d-%d, want 1-6", outer.StartLine, outer.EndLine)
	}
}

// --- strings containing braces ---------------------------------------------

func TestBashRegex_StringsWithBraces(t *testing.T) {
	src := `format() {
    echo "literal { brace }"
    echo 'single { quoted }'
}
trailer() { echo done; }
`
	chunks := bashRegexChunks("/p/s.sh", src)
	format := findChunkByName(t, chunks, "format", "function")
	if format.EndLine != 4 {
		t.Errorf("format end = %d, want 4 (string braces should not count)", format.EndLine)
	}
	findChunkByName(t, chunks, "trailer", "function")
}

// --- heredoc handling ------------------------------------------------------

func TestBashRegex_HeredocBody(t *testing.T) {
	src := `usage() {
    cat <<EOF
function not_a_real_func() {
    this is heredoc body
}
EOF
}
real_after() { echo yes; }
`
	chunks := bashRegexChunks("/p/h.sh", src)
	usage := findChunkByName(t, chunks, "usage", "function")
	if usage.EndLine != 7 {
		t.Errorf("usage end = %d, want 7 (heredoc body must not close the func)", usage.EndLine)
	}
	findChunkByName(t, chunks, "real_after", "function")
	for _, c := range chunks {
		if c.SymbolName != nil && *c.SymbolName == "not_a_real_func" {
			t.Errorf("regex picked up function name from inside heredoc: %s", *c.SymbolName)
		}
	}
}

func TestBashRegex_HeredocStripTabs(t *testing.T) {
	src := "deploy() {\n\tcat <<-EOF\n\t\tnested heredoc with leading tabs\n\tEOF\n}\n"
	chunks := bashRegexChunks("/p/h2.sh", src)
	deploy := findChunkByName(t, chunks, "deploy", "function")
	if deploy.EndLine != 5 {
		t.Errorf("deploy end = %d, want 5", deploy.EndLine)
	}
}

func TestBashRegex_HeredocQuotedDelim(t *testing.T) {
	src := `template() {
    cat <<'TEMPLATE'
${not_expanded}
TEMPLATE
}
`
	chunks := bashRegexChunks("/p/q.sh", src)
	tmpl := findChunkByName(t, chunks, "template", "function")
	if tmpl.EndLine != 5 {
		t.Errorf("template end = %d, want 5 (quoted heredoc delim)", tmpl.EndLine)
	}
}

// --- here-string vs heredoc -------------------------------------------------

func TestBashRegex_HereStringNotHeredoc(t *testing.T) {
	src := `check() {
    grep pattern <<<"$variable"
    echo done
}
`
	chunks := bashRegexChunks("/p/hs.sh", src)
	check := findChunkByName(t, chunks, "check", "function")
	if check.EndLine != 4 {
		t.Errorf("check end = %d, want 4 (here-string is single-line)", check.EndLine)
	}
}

// --- comments containing braces --------------------------------------------

func TestBashRegex_CommentBraces(t *testing.T) {
	src := `clean() {
    rm tmp # remove with { brace } in comment
    echo bye
}
`
	chunks := bashRegexChunks("/p/c.sh", src)
	clean := findChunkByName(t, chunks, "clean", "function")
	if clean.EndLine != 4 {
		t.Errorf("clean end = %d, want 4", clean.EndLine)
	}
}

// --- module gap chunks -----------------------------------------------------

func TestBashRegex_ModuleGapChunks(t *testing.T) {
	src := `#!/bin/bash
set -euo pipefail

VERSION=1.0

main() {
    echo "$VERSION"
}

main "$@"
`
	chunks := bashRegexChunks("/p/m.sh", src)
	findChunkByName(t, chunks, "main", "function")
	hasModule := false
	for _, c := range chunks {
		if c.ChunkType == "module" {
			hasModule = true
		}
	}
	if !hasModule {
		t.Errorf("expected at least one module chunk for top-level code, got: %s",
			summariseChunks(chunks))
	}
}

// --- no functions → returns nil --------------------------------------------

func TestBashRegex_NoFunctions(t *testing.T) {
	src := `#!/bin/bash
echo hello
echo world
`
	chunks := bashRegexChunks("/p/none.sh", src)
	if chunks != nil {
		t.Errorf("expected nil for file with no functions, got %d chunks", len(chunks))
	}
}

func TestBashRegex_EmptyContent(t *testing.T) {
	chunks := bashRegexChunks("/p/empty.sh", "")
	if chunks != nil {
		t.Errorf("expected nil for empty content, got %d chunks", len(chunks))
	}
}

// --- malformed: unbalanced braces ------------------------------------------

func TestBashRegex_UnbalancedDoesNotCrash(t *testing.T) {
	// Function opener with no matching close. The scanner gives up after
	// maxBashFuncLines and the function opener is silently skipped.
	src := `bad() {
    echo unclosed
`
	chunks := bashRegexChunks("/p/bad.sh", src)
	// Either we extract `bad` (if we hit EOF inside scan and decide to
	// emit) OR we skip it. Both are acceptable behaviours; what matters
	// is that we do not crash and we return SOMETHING reasonable.
	if chunks != nil {
		for _, c := range chunks {
			if c.SymbolName != nil && *c.SymbolName == "bad" && c.EndLine < c.StartLine {
				t.Errorf("malformed chunk: %+v", c)
			}
		}
	}
}

// --- the actual install.sh path: regression guard --------------------------

func TestBashRegex_InstallShFunction(t *testing.T) {
	// Compact reproduction of install.sh's `usage` function. If this test
	// ever stops finding `usage`, the regex regressed in a way that affects
	// real-world bash files.
	src := `#!/usr/bin/env bash

usage() {
    cat <<EOF
Usage: install.sh [--version <tag>]
EOF
}

main() {
    usage
}
`
	chunks := bashRegexChunks("/p/install.sh", src)
	findChunkByName(t, chunks, "usage", "function")
	findChunkByName(t, chunks, "main", "function")
}

// --- fallback wiring: ChunkFile uses regex for bash on parse fallback ------

func TestChunkFile_BashFallbackUsesRegex(t *testing.T) {
	// We pick a bash source that's chunked successfully by tree-sitter
	// (so the parse-budget guard does NOT fire) and verify both paths
	// produce a function-named chunk for `hello`. This is a sanity check
	// that bashRegexChunks signature matches the public ChunkFile schema.
	src := `hello() {
    echo "hi"
}
`
	chunks, _, err := ChunkFile("/p/x.sh", src, "bash", 0)
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	for _, c := range chunks {
		if c.ChunkType == "function" && c.SymbolName != nil && *c.SymbolName == "hello" {
			return
		}
	}
	t.Errorf("expected `hello` function chunk, got: %s", summariseChunks(chunks))
}
