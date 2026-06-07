// Package tsgrammars maps cix language ids to official tree-sitter C grammars.
//
// Most grammars are consumed as upstream Go modules (their bindings/go package
// compiles the vendored parser.c via cgo from the module cache, so the C never
// lives in this repo). A handful of grammars do not publish a usable Go binding
// (or, for sql, do not commit a generated parser.c; or, for r, ship a binding
// that omits the external scanner), so they are vendored in-tree under
// tsgrammars/<id>/ — see vendor.sh for the pins.
//
// Adding/upgrading a grammar: prefer a Go module (add the require + a line in
// Factories). Only vendor when no module exposes bindings/go.
package tsgrammars

import (
	"unsafe"

	// --- upstream Go-module grammars (C in the module cache, not this repo) ---
	dart "github.com/UserNobody14/tree-sitter-dart/bindings/go"
	swift "github.com/alex-pinkus/tree-sitter-swift/bindings/go"
	fortran "github.com/stadelmanma/tree-sitter-fortran/bindings/go"
	kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	lua "github.com/tree-sitter-grammars/tree-sitter-lua/bindings/go"
	zig "github.com/tree-sitter-grammars/tree-sitter-zig/bindings/go"
	bash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	css "github.com/tree-sitter/tree-sitter-css/bindings/go"
	gogr "github.com/tree-sitter/tree-sitter-go/bindings/go"
	haskell "github.com/tree-sitter/tree-sitter-haskell/bindings/go"
	html "github.com/tree-sitter/tree-sitter-html/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	julia "github.com/tree-sitter/tree-sitter-julia/bindings/go"
	ocaml "github.com/tree-sitter/tree-sitter-ocaml/bindings/go"
	php "github.com/tree-sitter/tree-sitter-php/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	scala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	// --- in-tree vendored grammars (no upstream Go binding available) ---
	markdown "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/markdown"
	objc "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/objc"
	rlang "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/r"
	scss "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/scss"
	solidity "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/solidity"
	sql "github.com/dvcdsys/code-index/server/internal/chunker/tsgrammars/sql"
)

// Factories returns a fresh map of language id -> C TSLanguage pointer factory.
// Each value returns the unsafe.Pointer the go-tree-sitter binding wraps via
// tree_sitter.NewLanguage.
func Factories() map[string]func() unsafe.Pointer {
	return map[string]func() unsafe.Pointer{
		// Go-module grammars
		"python":     python.Language,
		"typescript": typescript.LanguageTypescript,
		"tsx":        typescript.LanguageTSX,
		"javascript": javascript.Language,
		"go":         gogr.Language,
		"rust":       rust.Language,
		"java":       java.Language,
		"c":          c.Language,
		"cpp":        cpp.Language,
		"ruby":       ruby.Language,
		"c_sharp":    csharp.Language,
		"php":        php.LanguagePHP,
		"swift":      swift.Language,
		"kotlin":     kotlin.Language,
		"scala":      scala.Language,
		"bash":       bash.Language,
		"lua":        lua.Language,
		"dart":       dart.Language,
		"html":       html.Language,
		"css":        css.Language,
		"julia":      julia.Language,
		"fortran":    fortran.Language,
		"haskell":    haskell.Language,
		"ocaml":      ocaml.LanguageOCaml,
		"zig":        zig.Language,

		// Vendored grammars
		"markdown": markdown.Language,
		"objc":     objc.Language,
		"r":        rlang.Language,
		"scss":     scss.Language,
		"solidity": solidity.Language,
		"sql":      sql.Language,
	}
}
