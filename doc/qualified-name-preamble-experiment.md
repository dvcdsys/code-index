# Qualified-Name Preamble Experiment (2026-05-06)

## TL;DR

Tested whether wrapping the chunk preamble in a docstring and using
qualified symbol names (`UserService.authenticate` instead of bare
`authenticate`) improves embedding-search relevance. **Verdict: not
shipped.** The naive QID benchmark showed +5.6% but turned out to be
inflated by literal string-matching against the new preamble. A proper
semantic NL benchmark (queries describe behaviour, never name the class
or method) showed essentially zero gain on Python, zero on Go, and one
regression where a Mode A top-1 hit dropped below the relevance
threshold in Mode B. Feature reverted, this report kept as the record.

## What was tested

A new flag `CIX_EMBED_INCLUDE_QUALIFIED_NAME` (off by default) that
switched the preamble in front of every embedded chunk from the bare
path-aware form:

```
File: api/services/user.py
Language: python
method: authenticate

<body>
```

to a docstring-wrapped form with parent-class qualification:

```
"""
File: api/services/user.py
Language: python
method: UserService.authenticate
"""

<body>
```

Rationale: CodeRankEmbed was trained on (docstring, function-body)
pairs, so a docstring-style preamble was hypothesised to be more
in-distribution, and the qualified name was hypothesised to give the
model a stronger semantic anchor for "which class does this method
belong to."

Implementation covered ~20 files: config flag, runtimecfg layer + DB
migration, `embeddings.FormatChunkForEmbedding` rewrite to a
`FormatOptions` struct, indexer wiring, OpenAPI schema, admin handler,
dashboard toggle in Advanced section.

## Method

Three benchmarks across two real codebases.

### Codebases

| Codebase | Files | Chunks | Class methods (qualified) | Notes |
|---|---|---|---|---|
| `claude-code-index` (this repo) | 304 | 3268 | 77 of 394 (20%) | Go-heavy. Go methods do not get `ParentName` in the current chunker (Phase B gap). |
| `~/Cursor/brain-project` | 36 | 285 | 113 of 165 (69%) | Python class-heavy: `EventMemory`, `SemanticMemory`, `Retriever`, `LLMClient`, `ToolManager`, etc. Best-case for the feature. |

### Benchmarks

1. **Controlled fixture test** — six synthetic chunks (3 with `ParentName`,
   3 without) embedded directly via the live `llama-server` socket; cosine
   similarity computed against 8 hand-crafted queries (NL, QID, BARE).
   Used to isolate the effect of qualification from confounders.

2. **QID battery on brain-project** — 28 queries including 14 of the form
   `Class.method` (e.g. `EventMemory.add`, `TelegramInputSource.start`).
   Captured against fresh full-reindexes of brain-project under each flag
   state.

3. **Semantic NL battery (the real test)** — 12 queries on brain-project
   and 12 on cix-codebase, each describing the *behaviour* of a target
   method without ever naming the class or the method. For each query
   we recorded the rank and score of the expected file under both modes.

The QID battery (#2) was the test that initially looked positive. The
semantic battery (#3) was added after we noticed the QID gain was
suspiciously aligned with literal string-match against the new
preamble's `<kind>: ParentName.SymbolName` line.

## Results

### Backwards-compatibility sanity (Mode A old binary vs new binary flag-OFF)

```
hits      old=12/14  new=12/14
avg top-1 old=0.6625  new=0.6625   Δ=+0.0000
avg Jaccard top-3                  1.000
```

Identical to four decimal places. The new binary with the flag off is
bit-for-bit equivalent to the old code path. Not a concern.

### Controlled fixtures (50% with-parent)

Per query type:

| Flavor | Mode A | Mode B | Δ |
|---|---|---|---|
| QID  (`Class.method`) | 0.5264 | 0.5893 | +0.063 (+12%) |
| NL   (natural language) | 0.5759 | 0.5807 | +0.005 (+0.8%) |
| BARE (single name) | 0.6265 | 0.6212 | -0.005 (-0.8%) |

QID looks like a big win. NL barely moves.

### QID battery on brain-project (real reindex)

```
queries with hits     A=25/28   B=26/28   (B recovered one previous miss)
avg top-1 score       A=0.5612  B=0.5781  Δ=+0.0169 (+3.0%)
top-1 rank movement   ⬆14  ⬇7  ≈7
```

By query flavor:

| Flavor | N | Mode A | Mode B | Δ |
|---|---|---|---|---|
| QID  | 14 | 0.5592 | 0.5907 | **+0.0315 (+5.6%)** |
| NL   | 13 | 0.5518 | 0.5527 | +0.0009 (+0.2%) |
| BARE | 1  | 0.6900 | 0.6800 | -0.0100 |

Best individual movements: `TelegramInputSource.start` +0.120,
`EventMemory.add` +0.090, `ConsoleInputSource.run_loop` +0.070,
`ToolManager.execute` +0.070, `LLMClient.complete` +0.060.

This was the result that initially looked compelling.

### The catch — semantic NL battery (queries describe behaviour, never name the symbol)

The brain-project NL queries were of the form
"persist a new conversation exchange with bullet point summary into
durable storage" (target: `EventMemory.store`),
"merge results from episodic and semantic memory into one ranked list"
(target: `Retriever.retrieve_combined`), etc.

#### brain-project (Python, 69% qualified)

```
hit-rate              A=8/12   B=7/12      ⬇1
rank-1 hit-rate       A=7/12   B=6/12      ⬇1
avg expected-score    A=0.4838  B=0.4857   Δ=+0.0020 (+0.4%)
```

| Query (paraphrased) | Target | A | B | Δ |
|---|---|---|---|---|
| set up message handlers and spin up bot polling on background thread | `telegram.py` | 0.580 | 0.550 | **−0.030** |
| send a chat completion expecting JSON output matching pydantic model | `client.py` | 0.620 | 0.600 | −0.020 |
| summarize an exchange of input and response into bullet points | `compressor.py` | 0.420 (#1) | **dropped** | **regression below threshold** |
| look up past conversation events by semantic similarity | `event_memory.py` | 0.460 | 0.450 | −0.010 |
| use a language model to break a user message into sub-queries | `recall_planner.py` | 0.420 | 0.410 | −0.010 |
| merge results from episodic and semantic memory | `retriever.py` | 0.480 | 0.500 | +0.020 |
| look up extracted facts by semantic similarity | `semantic_memory.py` | 0.470 | 0.470 | 0 |

5 regressions, 1 small improvement, 2 neutral, 4 below-threshold misses
on both. The +5.6% QID win does not translate to the form of search a
real user actually issues.

#### cix-codebase (Go, 20% qualified — Go methods unaffected)

```
hit-rate              A=10/12  B=10/12   =
rank-1 hit-rate       A=8/12   B=8/12    =
avg expected-score    A=0.5040  B=0.5060  Δ=+0.0020 (+0.4%)
```

Effectively zero. Expected — Go methods aren't qualified, so the only
delta between Mode A and Mode B is the `"""..."""` wrapping itself,
which adds tokens without semantic content.

### The disambiguation control

`EventMemory.search_embeddings` and `SemanticMemory.search_embeddings`
have nearly-identical bodies (both call `self.collection.query(...)`).
The only thing that should disambiguate them is the parent class. If
qualification were doing useful work, Mode B should disambiguate them
*more strongly* than Mode A.

| Query | Mode A margin (event over semantic) | Mode B margin |
|---|---|---|
| "look up past conversation events by similarity" | 0.460 vs 0.420 = **0.040** | 0.450 vs 0.420 = **0.030** |

Margin shrank in Mode B. The model was already using the lexical
content of the body (`INSERT INTO events`, `events.append(...)`) to
disambiguate; adding `EventMemory.` to the preamble didn't help and
slightly muddled the signal.

## Conclusion

The hypothesis "qualified name in the preamble gives the embedding
model a class-context anchor" is **not supported** by semantic NL
testing. The +5.6% QID gain was almost entirely a literal-string-match
artefact of the new preamble containing `Class.method` verbatim — it
helps only when the user types `Class.method` into the search box,
which is a narrow slice of real queries.

Body content already carries enough lexical signal (`self.X`, type
hints, imports, SQL table names) for the model to disambiguate
class-scoped methods. Wrapping the preamble in `"""..."""` and adding
the parent name produces no additional semantic value, and in some
cases adds enough noise to push a previously-on-the-margin chunk below
the relevance threshold (see `Compressor.compress`).

**Decision: revert the feature.** Not over-engineering for a hypothesis
that didn't pan out under the right test. If a future iteration wants
to revisit the idea, the right starting point is a different preamble
shape — likely one that doesn't add tokens at all but instead changes
how we represent the symbol context (e.g. extending the chunker so Go
methods get a `ParentName` from the receiver, then judging on that
larger sample).

## Reproducing

The experiment used the live `llama-server` socket directly for the
fixture test, and `cix reindex --full` + `cix search` for the
real-codebase tests. The full query batteries and per-query top-3
output were saved under `/tmp/abc-real/` during the run; they are not
checked into the repo. The harness that ran the fixture test
(`server/cmd/abctest/`) was likewise removed when the feature was
reverted.

To reproduce: re-add the flag, build, capture searches under flag-OFF,
toggle flag, full reindex, capture again, compare scores against an
expected-target list. The decisive step is constructing queries that
do not lexically overlap with the symbol names in the preamble.
