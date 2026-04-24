//go:build bench_treesitter

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

var targetNodes = map[string]map[string]struct{}{
	"python": {
		"function_definition": {},
		"class_definition":    {},
	},
	"go": {
		"function_declaration": {},
		"method_declaration":   {},
		"type_spec":            {},
	},
	"javascript": {
		"function_declaration": {},
		"arrow_function":       {},
		"class_declaration":    {},
		"method_definition":    {},
	},
	"typescript": {
		"function_declaration":   {},
		"arrow_function":         {},
		"class_declaration":      {},
		"method_definition":      {},
		"interface_declaration":  {},
		"type_alias_declaration": {},
	},
	"tsx": {
		"function_declaration":   {},
		"arrow_function":         {},
		"class_declaration":      {},
		"method_definition":      {},
		"interface_declaration":  {},
		"type_alias_declaration": {},
	},
	"java": {
		"method_declaration":    {},
		"class_declaration":     {},
		"interface_declaration": {},
	},
	"c": {
		"function_definition": {},
		"struct_specifier":    {},
	},
	"cpp": {
		"function_definition":  {},
		"class_specifier":      {},
		"struct_specifier":     {},
		"namespace_definition": {},
	},
	"rust": {
		"function_item": {},
		"struct_item":   {},
		"enum_item":     {},
		"trait_item":    {},
		"impl_item":     {},
	},
	"ruby": {
		"method":           {},
		"class":            {},
		"module":           {},
		"singleton_method": {},
	},
}

type langCase struct {
	Lang    string
	Fixture string
	Lang_   *sitter.Language
}

type langResult struct {
	Lang       string   `json:"lang"`
	Fixture    string   `json:"fixture"`
	Loaded     bool     `json:"loaded"`
	ParseOK    bool     `json:"parse_ok"`
	RootErrors int      `json:"root_errors"`
	Nodes      int      `json:"total_nodes_walked"`
	SymbolHits int      `json:"symbol_hits"`
	HitTypes   []string `json:"hit_types"`
	Error      string   `json:"error,omitempty"`
	Gate       string   `json:"gate"`
}

type bench2Result struct {
	Benchmark string       `json:"benchmark"`
	Languages []langResult `json:"languages"`
	Passed    int          `json:"passed"`
	Total     int          `json:"total"`
	Gate      string       `json:"gate"`
}

func walk(n *sitter.Node, lang *sitter.Language, want map[string]struct{}, hits map[string]int, total *int) {
	if n == nil {
		return
	}
	*total++
	if _, ok := want[n.Type(lang)]; ok {
		hits[n.Type(lang)]++
	}
	cnt := n.ChildCount()
	for i := 0; i < cnt; i++ {
		walk(n.Child(i), lang, want, hits, total)
	}
}

func run(c langCase) langResult {
	out := langResult{Lang: c.Lang, Fixture: c.Fixture, Gate: "FAIL"}
	if c.Lang_ == nil {
		out.Error = "language binding nil"
		return out
	}
	out.Loaded = true

	src, err := os.ReadFile(filepath.Join("fixtures", c.Fixture))
	if err != nil {
		out.Error = fmt.Sprintf("read fixture: %v", err)
		return out
	}

	parser := sitter.NewParser(c.Lang_)
	tree, err := parser.Parse(src)
	if err != nil {
		out.Error = fmt.Sprintf("parse: %v", err)
		return out
	}
	root := tree.RootNode()
	if root == nil {
		out.Error = "nil root node"
		return out
	}
	out.ParseOK = true
	if root.HasError() {
		out.RootErrors = 1
	}

	want := targetNodes[c.Lang]
	hits := map[string]int{}
	total := 0
	walk(root, c.Lang_, want, hits, &total)
	out.Nodes = total

	totalHits := 0
	for t, n := range hits {
		totalHits += n
		out.HitTypes = append(out.HitTypes, fmt.Sprintf("%s:%d", t, n))
	}
	sort.Strings(out.HitTypes)
	out.SymbolHits = totalHits

	if out.ParseOK && totalHits >= 1 {
		out.Gate = "PASS"
	}
	return out
}

func main() {
	outDir := "results"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir results: %v\n", err)
		os.Exit(1)
	}

	cases := []langCase{
		{Lang: "python", Fixture: "sample.py", Lang_: grammars.PythonLanguage()},
		{Lang: "go", Fixture: "sample.go", Lang_: grammars.GoLanguage()},
		{Lang: "javascript", Fixture: "sample.js", Lang_: grammars.JavascriptLanguage()},
		{Lang: "typescript", Fixture: "sample.ts", Lang_: grammars.TypescriptLanguage()},
		{Lang: "tsx", Fixture: "sample.tsx", Lang_: grammars.TsxLanguage()},
		{Lang: "java", Fixture: "Sample.java", Lang_: grammars.JavaLanguage()},
		{Lang: "c", Fixture: "sample.c", Lang_: grammars.CLanguage()},
		{Lang: "cpp", Fixture: "sample.cpp", Lang_: grammars.CppLanguage()},
		{Lang: "rust", Fixture: "sample.rs", Lang_: grammars.RustLanguage()},
		{Lang: "ruby", Fixture: "sample.rb", Lang_: grammars.RubyLanguage()},
	}

	results := make([]langResult, 0, len(cases))
	passed := 0
	for _, c := range cases {
		r := run(c)
		results = append(results, r)
		if r.Gate == "PASS" {
			passed++
		}
		fmt.Printf("%-11s %-14s %s nodes=%d hits=%d %s\n",
			r.Lang, r.Fixture, r.Gate, r.Nodes, r.SymbolHits, r.Error)
	}

	gate := "FAIL"
	if passed == len(cases) {
		gate = "PASS"
	}
	res := bench2Result{
		Benchmark: "gotreesitter top-10 coverage",
		Languages: results,
		Passed:    passed,
		Total:     len(cases),
		Gate:      gate,
	}

	b, _ := json.MarshalIndent(res, "", "  ")
	outPath := filepath.Join(outDir, "treesitter.json")
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Printf("Gate: %s (%d/%d)\nWrote %s\n", gate, passed, len(cases), outPath)
}
