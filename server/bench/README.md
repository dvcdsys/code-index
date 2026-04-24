# api-go-poc/bench — Phase 0 risk-validation benchmarks

Standalone Go module that de-risks three hard dependencies for the Python →
Go migration (see `~/.claude/plans/go-prancy-waterfall.md` Phase 0):

| Bench | What it checks | Risk it retires | Gate |
|---|---|---|---|
| 1 | `github.com/philippgille/chromem-go` — vector DB | scale (50k × 768-dim vectors) | P95 < 200 ms, RAM < 4 GB |
| 2 | `github.com/odvcencio/gotreesitter` — AST | top-10 languages parse cleanly | all 10 PASS |
| 3 | `github.com/go-skynet/go-llama.cpp` — embeddings | parity with Python llama-cpp | mean cos ≥ 0.999, min ≥ 0.995 |

Scope is **only** this directory. No changes to `api/`, `cli/`, or repo root.

---

## Layout

```
bench/
  go.mod                         # standalone module, Go 1.23
  bench_chromem.go               # Bench 1
  bench_treesitter.go            # Bench 2
  bench_embed_parity.go          # Bench 3 (Go side)
  emit_reference_embeddings.py   # Bench 3 Python reference generator
  fixtures/                      # 10 language samples (~30-50 LOC each)
    sample.py, sample.go, sample.js, sample.ts, sample.tsx,
    Sample.java, sample.c, sample.cpp, sample.rs, sample.rb
  results/
    chromem.json                 # Bench 1 output
    treesitter.json              # Bench 2 output
    embed_parity.json            # Bench 3 output
    reference_embeddings.json    # emitted by Python helper (Bench 3 input)
    reference_gguf_path.txt
```

Each `bench_*.go` is guarded by a build tag (`bench_chromem`, `bench_treesitter`,
`bench_embed_parity`) so `go build ./...` doesn't try to compile all three at
once (different heavy imports).

---

## How to run

First-time deps:

```bash
cd api-go-poc/bench
go mod tidy
```

### Bench 1 — chromem-go scale

```bash
go run -tags=bench_chromem ./bench_chromem.go
# → results/chromem.json
```

What it does:
1. Creates an in-memory chromem collection with `EmbeddingFuncIdentity`
   (so we provide pre-computed vectors — no actual embedding work inline).
2. Inserts 50,000 random L2-normalized 768-dim vectors.
3. Runs 100 query vectors, top-10, measures per-query latency.
4. Reports upsert time, `runtime.MemStats.HeapAlloc` after insert & after
   queries, P50/P95/P99.

Fails the gate if P95 ≥ 200 ms or peak heap ≥ 4 GB.

### Bench 2 — gotreesitter top-10 coverage

```bash
go run -tags=bench_treesitter ./bench_treesitter.go
# → results/treesitter.json
```

Language set (matches `api/app/services/chunker.py:LANGUAGE_NODES` + four
languages the Python side currently sliding-windows but the Go fork supports
natively):

- python, go, javascript, typescript, tsx, java, c, cpp, rust, ruby

For each fixture: parses with gotreesitter, walks the tree, counts nodes whose
type matches the `targetNodes[lang]` set (mirrors `LANGUAGE_NODES`). A language
passes if the root parses **and** at least one symbol-like node was found.

**Dependency note:** `github.com/odvcencio/gotreesitter` is a fork of
`smacker/go-tree-sitter` (per the plan); the import paths in `bench_treesitter.go`
(`.../python`, `.../golang`, `.../cpp`, etc.) mirror that layout. If the fork
has diverged, update the sub-package imports and the `GetLanguage()` call
names. This is the riskiest dep per the plan — if it won't `go get` at all on
mac, flag "needs Linux server retry" and don't thrash.

### Bench 3 — embed parity

Two-step; step 1 is Python, step 2 is Go. They share the GGUF file on disk.

**Step 1 — emit Python reference:**

```bash
# From repo root, activate api/ venv (has llama-cpp-python + huggingface_hub)
source api/.venv/bin/activate   # or however it's set up
cd api-go-poc/bench
python emit_reference_embeddings.py
# → results/reference_embeddings.json
# → results/reference_gguf_path.txt   (absolute path to the GGUF on disk)
```

This downloads `awhiteside/CodeRankEmbed-Q8_0-GGUF` from Hugging Face on
first run (~1 GB cached in `~/.cache/huggingface/hub/`). The 10 phrases are
hard-coded in the script — 5 code snippets + 5 natural-language queries
(the `is_query=True` ones get the query prefix
`"Represent this query for searching relevant code: "`, per
`QUERY_PREFIX_MODELS` in `api/app/services/embeddings.py`).

If the api/ venv isn't available and the GGUF isn't already cached, mark
Bench 3 **BLOCKED** — do **not** download a multi-GB GGUF on mac just for
this. Leave `results/embed_parity.json` with `gate: BLOCKED`.

Alternatives if the Python API is already running:
- We could add a `/api/v1/debug/embed` route temporarily, but Phase 0 scope
  says no changes to `api/`. If the user wants to unblock without the venv,
  they need to bring up the docker compose stack: `docker compose up -d api`
  and then exec `python emit_reference_embeddings.py` inside the container
  (it has the venv baked in).

**Step 2 — Go parity check:**

```bash
cd api-go-poc/bench
go run -tags=bench_embed_parity ./bench_embed_parity.go
# → results/embed_parity.json
```

Loads the same GGUF via go-llama.cpp (`llama.EnableEmbeddings`), feeds the
*exact* `text_sent_to_model` string from the reference JSON (prefix already
applied Python-side), computes cosine per phrase. Gate: mean ≥ 0.999, min
≥ 0.995.

**CGO requirement:** go-llama.cpp needs CGO_ENABLED=1 + a working C++ toolchain.
On mac: `xcode-select --install`. If the build fails (CMake / Metal
linking issues), document the exact error and flag the bench for re-run on
the Linux GPU server — don't thrash on mac.

---

## Results — Phase 0 run (2026-04-23)

| Bench | Gate | Numbers |
|---|---|---|
| 1 chromem-go | **PASS** | P50=22.6ms, P95=23.9ms, P99=24.3ms, RAM=0.18GB heap, upsert 50k in 0.05s (~940k docs/sec) |
| 2 gotreesitter | **PASS** | 10/10 languages parsed, symbol nodes found in all (python=6, go=7, js=9, ts=11, tsx=7, java=8, c=7, cpp=8, rust=12, ruby=11) |
| 3 embed parity | **FAIL → redirect** | `go-skynet/go-llama.cpp` master (pinned 2024-03) cannot load CodeRankEmbed — `error loading model: unknown model architecture: 'nomic-bert'`. Library's vendored llama.cpp predates nomic-bert support (added upstream late 2024). Python-side reference emitted successfully (10 vectors). |

### Critical finding: `go-skynet/go-llama.cpp` is stale

The library hasn't bumped its llama.cpp submodule since March 2024. Modern embedding models (nomic-bert family — CodeRankEmbed, nomic-embed-text, etc.) won't load. Must switch to an alternative before Phase 3. Candidates to evaluate:

- `github.com/gpustack/llama-box` — active, bundles current llama.cpp
- `github.com/mudler/LocalAI` bindings — same author maintains a newer fork internally
- Fork `go-skynet/go-llama.cpp` and bump the submodule — low-trust, fragile
- Run `llama.cpp/examples/server` as a sidecar and call via HTTP — contradicts memory plan's "no sidecar" stance but is the lowest-risk fallback

### Environment findings

- macOS Darwin 25.4.0, Go 1.25.3 darwin/arm64, CGO_ENABLED=1 ✓
- Apple clang works, Metal + Accelerate frameworks link ✓
- GGUF cached at `~/.cache/huggingface/hub/models--awhiteside--CodeRankEmbed-Q8_0-GGUF/snapshots/.../coderankembed-q8_0.gguf`
- System `python3` has `llama-cpp-python` 0.3.20 — loads model fine, emits reference embeddings (`results/reference_embeddings.json`)
- Python API at `:8000` not running locally — not needed for these benches.

### Gate verdict

- Phase 0 stack check: **2 of 3 PASS**, Bench 3 is a known-fixable stack swap (not a fundamental blocker).
- chromem-go scale risk = RETIRED — 100× headroom on both latency and RAM.
- gotreesitter risk = RETIRED for top-10 languages with real fixtures.
- Embedding binding risk = ESCALATED — the assumed library is dead, but GGUF + llama.cpp itself still works. Phase 3 must open with a binding re-selection spike.

---

## Dependency versions

`go.mod` declares the three deps without versions — `go mod tidy` picks the
latest tagged release on first run. Pin to what tidy resolves if reproducibility
matters for the verifier phase.

Current imports assume these API surfaces:
- **chromem-go**: `chromem.NewDB`, `CreateCollection(name, metadata, embedFn)`,
  `chromem.EmbeddingFuncIdentity()`, `coll.AddDocuments(ctx, docs, concurrency)`,
  `coll.QueryEmbedding(ctx, vec, topK, where, whereDoc)`. Stable since v0.6.
- **gotreesitter**: `sitter.NewParser()`, `parser.SetLanguage(*sitter.Language)`,
  `parser.ParseCtx(ctx, oldTree, src)`, `tree.RootNode()`, `node.Type()`,
  `node.ChildCount()`, `node.Child(i)`, `node.HasError()`. Mirrors
  `smacker/go-tree-sitter`.
- **go-llama.cpp**: `llama.New(path, opts...)`, `llama.EnableEmbeddings`,
  `llama.SetContext(n)`, `llama.SetGPULayers(n)`, `model.Embeddings(text)`
  returning `[]float32`. Stable since late 2024.

If any of these have shifted, `go build` will tell us in seconds once bash is available.
