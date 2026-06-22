# PoC: tree-sitter via WASM/wazero (pure-Go, no cgo)

An alternative to the cgo backend on `feat/chunker-cgo-treesitter`. The **official**
tree-sitter C runtime + the official TypeScript grammar are compiled to a single
standalone `wasm32-wasi` reactor module (`build.sh`, via `zig cc`) and driven
from Go through [wazero](https://github.com/tetratelabs/wazero) — **no cgo, no
JavaScript, no third-party parser**. Only `wasmts.go` (our wazero host) is
bespoke; the parser itself is the unmodified upstream C.

Goal: give us real **speed + stability** numbers to choose between cgo and wasm.

## Results — same 852-file vscode TypeScript corpus, full-tree walk

| backend | wall | files/s | MB/s | ERROR trees | `editorOptions.ts` |
|---|---|---|---|---|---|
| gotreesitter (pure-Go GLR) | 13.83 s | 62 | 0.8 | **13** | 8.77 s → ERROR |
| **WASM (wazero, pure-Go host)** | **~2.5 s** | **~330** | **~4.1** | **0** | **49 ms** |
| cgo (native tree-sitter) | 1.26 s | 675 | 11.5 | 0 | 17 ms |

- **WASM is ~2× slower than cgo, ~5× faster than gotreesitter, and correct** (0 ERROR trees vs gotreesitter's 13).
- The WASM overhead is the **host↔guest call boundary**, not memory: each of the
  2.68 M nodes costs ~3 wazero calls (`ts_node_type`, `ts_node_child_count`,
  `ts_node_child`). Reusing node slots instead of `malloc`/`free` per node moved
  the number only 328→357 files/s — so it's the calls. A single batched
  "serialize subtree" export would close most of the remaining gap vs cgo
  (future work; not done here).

## Stability (`cmd/stability`)

- tree-sitter is **robust**: 6 adversarial inputs (100–200 k-deep nesting, 5 MB
  single token, invalid UTF-8, unbalanced templates) all parsed without crashing
  — this is true of cgo too, so it is **not** a bug WASM uniquely fixes.
- What WASM **adds** is containment: a guest-side fault (resource limit, and in
  principle any C bug — stack overflow, OOB) surfaces as a **recoverable Go
  error**; the host process stays alive. The memory-capped run demonstrates this.
- Under cgo the equivalent fault is a native **SIGSEGV/abort that kills the whole
  cix-server**. So crash-isolation is **insurance against unknown C bugs in
  grammars/scanners**, not a fix for an observed crash.

## Trade-off summary

| | cgo (current) | WASM/wazero (this PoC) |
|---|---|---|
| Parse speed | 🟢 fastest | 🟡 ~2× slower (≈invisible end-to-end: embeddings dominate) |
| Correctness | 🟢 official | 🟢 official (identical parser) |
| Build | 🟡 needs C toolchain (musl-static solved it) | 🟢 `CGO_ENABLED=0`, trivial cross-compile; `zig` only at wasm-build time (one-off, artifact committed) |
| Crash isolation | 🔴 C fault kills process | 🟢 contained → Go error |
| Binary size | 🔴 ~78 MB (grammar tables linked natively) | 🟢 likely smaller: pure-Go host (~41 MB) + embedded `.wasm` (1.4 MB / grammar, brotli-compressible) |
| Maturity / effort | 🟢 drop-in (official binding + 31 grammar modules) | 🔴 bespoke host; must build/bundle 31 grammar `.wasm` + flesh out node API + batched walk |

## Honest read

It's close. cgo is done and fastest. WASM costs ~2× on **parsing**, but since
**embeddings dominate end-to-end indexing time**, that 2× is largely invisible in
production — while WASM's upsides (no cgo, crash-isolation, smaller binary,
toolchain-free server builds) are real. The price of WASM is **engineering
effort** to productionize: build all 31 grammars into the module, write the full
node-walk API the chunker needs (with a batched-walk export to recover speed),
and wire it behind the same `tsgrammars`-style registry.

## Build & run

```bash
brew install zig          # provides clang + wasi-libc cross-compile
./build.sh                 # → ts-ts.wasm (official tree-sitter v0.25.10 + tree-sitter-typescript v0.23.2)
go run ./cmd/bench  /path/to/vscode/src/vs/editor
go run ./cmd/stability
```

`ts-ts.wasm` is committed so the benchmarks run without zig.
