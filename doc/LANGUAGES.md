# Supported languages

cix uses tree-sitter to extract semantic chunks (functions, classes, methods, types) from source code. The grammars are compiled to WebAssembly and run in [wazero](https://github.com/tetratelabs/wazero), a pure-Go WASM runtime — so the server stays a cgo-free static binary and a runaway grammar hits a memory limit inside the sandbox instead of the process heap (`server/internal/chunker/tswasm`). Files in unsupported languages still get indexed via a sliding-window fallback — they're searchable, just without per-symbol granularity.

## Default language set (31)

| ID | Function | Class | Method | Type |
|---|:-:|:-:|:-:|:-:|
| `python` | ✓ | ✓ | | |
| `typescript` | ✓ | ✓ | ✓ | ✓ |
| `tsx` | ✓ | ✓ | ✓ | ✓ |
| `javascript` | ✓ | ✓ | ✓ | |
| `go` | ✓ | | ✓ | ✓ |
| `rust` | ✓ | ✓ | | ✓ |
| `java` | ✓ | ✓ | | ✓ |
| `c` | ✓ | ✓ | | ✓ |
| `cpp` | ✓ | ✓ | | ✓ |
| `c_sharp` | ✓ | ✓ | ✓ | ✓ |
| `ruby` | ✓ | ✓ | | |
| `php` | ✓ | ✓ | ✓ | ✓ |
| `swift` | ✓ | ✓ | | ✓ |
| `kotlin` | ✓ | ✓ | | |
| `scala` | ✓ | ✓ | | ✓ |
| `bash` | ✓ | | | |
| `lua` | ✓ | | | |
| `dart` | ✓ | ✓ | ✓ | ✓ |
| `r` | ✓ | | | |
| `objc` | ✓ | ✓ | ✓ | ✓ |
| `html` | | | | ✓ |
| `css` | | ✓ | | |
| `scss` | ✓ | ✓ | | |
| `sql` | ✓ | | | ✓ |
| `markdown` | | | | ✓ |
| `zig` | ✓ | ✓ | | |
| `julia` | ✓ | | | |
| `fortran` | ✓ | ✓ | | |
| `haskell` | ✓ | | | ✓ |
| `ocaml` | ✓ | ✓ | | ✓ |
| `solidity` | ✓ | ✓ | | ✓ |

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

PRs welcome — the node names in `defaultRegistry` must match what the grammar actually emits, and `chunker_test.go` walks the registry against the compiled grammars to catch a typo without needing a fixture file per language.

## How the chunker decides

1. `langdetect.Detect(filePath)` maps extension/filename → language ID.
2. `chunker.ChunkFile()` looks up the ID in the active registry.
3. If found and its `languageNodes` map is non-empty → AST-based extraction (function/class/method/type chunks + identifier references).
4. Otherwise → sliding-window chunks of `windowSize=4000` bytes with `overlap=500`.
