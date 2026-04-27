# Supported languages

cix uses tree-sitter (via `github.com/odvcencio/gotreesitter`) to extract semantic chunks (functions, classes, methods, types) from source code. Files in unsupported languages still get indexed via a sliding-window fallback — they're searchable, just without per-symbol granularity.

## Default language set (30)

| ID | gotreesitter factory | Function | Class | Method | Type |
|---|---|:-:|:-:|:-:|:-:|
| `python` | `PythonLanguage` | ✓ | ✓ | | |
| `typescript` | `TypescriptLanguage` | ✓ | ✓ | ✓ | ✓ |
| `tsx` | `TsxLanguage` | ✓ | ✓ | ✓ | ✓ |
| `javascript` | `JavascriptLanguage` | ✓ | ✓ | ✓ | |
| `go` | `GoLanguage` | ✓ | | ✓ | ✓ |
| `rust` | `RustLanguage` | ✓ | ✓ | | ✓ |
| `java` | `JavaLanguage` | ✓ | ✓ | | ✓ |
| `c` | `CLanguage` | ✓ | ✓ | | ✓ |
| `cpp` | `CppLanguage` | ✓ | ✓ | | ✓ |
| `c_sharp` | `CSharpLanguage` | ✓ | ✓ | ✓ | ✓ |
| `ruby` | `RubyLanguage` | ✓ | ✓ | | |
| `php` | `PhpLanguage` | ✓ | ✓ | ✓ | ✓ |
| `swift` | `SwiftLanguage` | ✓ | ✓ | | ✓ |
| `kotlin` | `KotlinLanguage` | ✓ | ✓ | | |
| `scala` | `ScalaLanguage` | ✓ | ✓ | | ✓ |
| `bash` | `BashLanguage` | ✓ | | | |
| `lua` | `LuaLanguage` | ✓ | | | |
| `dart` | `DartLanguage` | ✓ | ✓ | ✓ | ✓ |
| `r` | `RLanguage` | ✓ | | | |
| `objc` | `ObjcLanguage` | ✓ | ✓ | ✓ | ✓ |
| `html` | `HtmlLanguage` | | | | ✓ |
| `css` | `CssLanguage` | | ✓ | | |
| `scss` | `ScssLanguage` | ✓ | ✓ | | |
| `sql` | `SqlLanguage` | ✓ | | | ✓ |
| `markdown` | `MarkdownLanguage` | | | | ✓ |
| `zig` | `ZigLanguage` | ✓ | ✓ | | |
| `julia` | `JuliaLanguage` | ✓ | | | |
| `fortran` | `FortranLanguage` | ✓ | ✓ | | |
| `haskell` | `HaskellLanguage` | ✓ | | | ✓ |
| `ocaml` | `OcamlLanguage` | ✓ | ✓ | | ✓ |

The exact AST node types per language live in `server/internal/chunker/chunker.go` (`defaultRegistry`). File-extension mapping lives in `server/internal/langdetect/langdetect.go`.

## Configuring the active set

`CIX_LANGUAGES` (comma-separated, case-insensitive) restricts the active set. Empty / unset = all defaults.

```bash
# Only index Python and Go — every other language falls to sliding-window
CIX_LANGUAGES=python,go cix-server

# Add Rust to the trio
CIX_LANGUAGES="python, go, rust" cix-server
```

Unknown IDs are logged at startup and ignored — typos won't crash the server.

The active set is logged at INFO during startup:

```
{"level":"INFO","msg":"chunker languages configured","active":["python","go","rust"]}
```

## Languages with extension detection but no grammar

These produce sliding-window chunks. Adding semantic chunking is a one-map-entry addition in `defaultRegistry`. Candidates:

`erlang, elixir, commonlisp, svelte, graphql, hcl (terraform), cmake, dockerfile, regex, xml, make`

PRs welcome — verify node names with `gotreesitter`'s `cmd/tsquery` against a representative fixture before adding.

## How the chunker decides

1. `langdetect.Detect(filePath)` maps extension/filename → language ID.
2. `chunker.ChunkFile()` looks up the ID in the active registry.
3. If found and its `languageNodes` map is non-empty → AST-based extraction (function/class/method/type chunks + identifier references).
4. Otherwise → sliding-window chunks of `windowSize=4000` bytes with `overlap=500`.
