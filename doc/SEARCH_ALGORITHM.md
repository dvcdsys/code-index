# Search Algorithm

How cix ranks results for single-project semantic search, workspace
hybrid search, and the symbol/files/refs/defs lookups. Use this when
calibrating `--min-score`, choosing between modes, or debugging a query
that "should have found" something.

## 1. Per-project semantic flow

```
query string ──▶ "Represent this query for searching relevant code: " + query
              │
              ▼
       llama-server sidecar (CodeRankEmbed Q8_0 GGUF) — 768-dim vector
              │
              ▼
   cosine search over the project's vector collection
              │
              ▼
   per-chunk hits → merge windowed overlaps → group by file → top-N files
```

**Asymmetric model.** CodeRankEmbed is purpose-built for code retrieval
and embeds queries with a fixed prefix
(`"Represent this query for searching relevant code: "`). Queries and
passages live in *different* regions of the 768-dim space, so cosine
similarities are systematically lower than for symmetric models. A
"strong" match here is ~0.55, not ~0.80. Do not compare these numbers
against thresholds quoted for OpenAI, Voyage, or generic
sentence-transformers — they aren't measuring the same thing.

**Path-aware preamble.** Each chunk is embedded with its file path,
language, and parent symbol prefixed to the body. This is why
`cix search "auth middleware"` finds `auth.go` even when the file's
prose uses different vocabulary. Toggle with `CIX_EMBED_INCLUDE_PATH`
(default `true`); flipping it requires `cix reindex --full` because the
vectors change.

**Score landscape (Q8_0, path-aware on).**

| Score        | Meaning                                                     |
|--------------|-------------------------------------------------------------|
| ≥ 0.65       | Exact symbol/filename match — almost certainly relevant     |
| 0.50 – 0.65  | Strong concept match — usually relevant                     |
| 0.40 – 0.50  | Weak match — sometimes useful                               |
| < 0.40       | Noise — filtered by default                                 |

Default CLI floor is `--min-score 0.4`. Drop to `0.25` for sparse or
single-token queries; below `0.2` is essentially random.

**Result grouping.** `search.go` returns per-chunk hits, then
`search_merge.go` merges overlapping line windows from the same file
and groups everything by file path. The top-N flag (`--limit`) is N
*files*, each containing all relevant matches in order.

## 2. FTS5 / BM25 chunk mirror

Every chunk that lands in the vector store also lands as a row in two
sister SQLite tables:

- `chunks_meta` — regular indexed shadow (project_path, file_path,
  start/end line, chunk type, symbol name, language) — lets the
  indexer find and delete rows for a file efficiently.
- `chunks_fts` — FTS5 virtual table over `(content, symbol_name,
  file_path)` — provides BM25 scoring against literal tokens.

Both tables share a rowid and are written inside the indexer's per-file
SQL transaction, together with that file's symbols, references and hash —
so a chunk is in both of *these* tables or neither. The vector store is a
separate database file with its own write (see
[`VECTORSTORE.md`](VECTORSTORE.md)), so it is not part of that
transaction: an interrupted index can leave a file embedded but not
mirrored, which the next indexing pass corrects.

Code: `server/internal/chunksfts/chunksfts.go`. Introduced by `f00e3d3`.

The sparse signal does two jobs the dense model alone cannot do well:

1. **Acronym / short-token precision.** Short product codes and unique
   identifiers (`ACME-712`, `XYZId`) get diffuse cosine scores because
   the embedding model spreads rare-token mass across many neighbours.
   BM25 over the literal tokens recovers the precision.
2. **Project-relevance gating in workspace search.** Dense fan-out
   returns the N nearest vectors from every project's collection
   regardless of semantic distance, so projects that share zero
   vocabulary with the query can still surface at chunk_score ≈
   0.2–0.3. BM25 returning **zero hits** in a project is a strong
   "nothing here" signal that dense cannot produce.

`chunks_fts` is not used for single-project search today — that path
stays pure dense — but the table is populated on every project so the
workspace path can rely on it.

## 3. Workspace hybrid search

`POST /api/v1/workspaces/{id}/search` runs a two-stage hybrid:

```
                            ┌──────────────────────────┐
                            │  query                   │
                            └────┬───────────────┬─────┘
                                 │               │
                ┌────────────────▼──┐         ┌──▼──────────────┐
                │ dense fan-out     │         │ BM25 fan-out    │
                │ (per-project      │         │ (chunks_fts per │
                │  vector cosine)   │         │  project)       │
                └────────┬──────────┘         └─────────┬───────┘
                         │                              │
                         ▼                              ▼
                 ┌──────────────────────────────────────────────┐
                 │ stage 1 — project gating + hybrid score      │
                 │  • each project gets dense + bm25 scores     │
                 │  • zero-bm25 projects are demoted            │
                 │  • top-K projects survive                    │
                 └─────────────────┬────────────────────────────┘
                                   │
                                   ▼
                 ┌──────────────────────────────────────────────┐
                 │ stage 2 — within surviving projects, merge   │
                 │ dense+bm25 chunk scores, group by file,      │
                 │ return ranked top-N files                    │
                 └──────────────────────────────────────────────┘
```

Calibrated defaults live in `server/internal/httpapi/workspacesearch.go`
(see `96b487d` for the calibration commit). The endpoint accepts
explicit tunables (`alpha`, `topK`, `perProjectLimit`, …) so an operator
can override on a per-query basis, but the defaults are tuned to the
public eval corpus described in `docs/workspace-eval-2026-05-13/`.

**Why two stages.** Workspaces span 5–30+ repos. A naive flat top-N
across the union floods results from whichever project happens to have
the highest-density vocabulary overlap. Gating at the project level
first ("which repos should we even look in?") makes the result list
reflect the workspace shape — typically 2–4 distinct projects per
answer rather than 8 chunks from one repo.

Trust rules for an agent consuming the response (`chunks[]` vs
`projects[]` array) live in the `cix-workspace` skill at
`skills/cix-workspace/SKILL.md` and in
[`workspaces.md`](../workspaces.md#trust-rules).

## 4. Symbols / definitions / references / files

These bypass the embedding pipeline entirely. They run against
SQLite-backed indexes in the system database, which the chunker fills in
its own per-file transaction alongside the FTS mirror:

- **`cix symbols <name>`** — substring-and-trigram lookup over
  `symbols` (kind ∈ {function, class, method, type}). Fast (<50 ms on a
  10k-symbol project). Used when the agent already knows the name.
- **`cix def <name>`** — same table, filtered to where the symbol is
  *defined* (declaration site, not reference).
- **`cix refs <name>`** — looks up `symbol_refs`, which the chunker
  emits during AST traversal. The exact granularity varies by language
  (`server/internal/chunker/chunker.go` `languageNodes` map).
- **`cix files <pattern>`** — substring/glob over the `files` table.

None of these consume embedding capacity, so they keep working with
`CIX_EMBEDDINGS_ENABLED=false`.

## 5. Tuning the floor

The default `--min-score 0.4` works well on real codebases. Two
common reasons to override:

- **Too many results, too noisy.** Raise to `0.5` or `0.6`. Useful
  when the agent's context is filling up with weak matches.
- **No results, but you know the code exists.** Drop to `0.25` (or
  `0.2` for a last resort). Single-word queries on rare identifiers
  often need this. If `0.2` still returns nothing, the index is
  probably stale — run `cix status` and `cix reindex` if needed.

When in doubt: increase specificity in the query itself before
lowering the floor. "validation" → "input validation in auth
middleware" is usually a bigger improvement than threshold tuning,
because the path-aware preamble rewards locating phrasing.

## 6. Related files

- `server/internal/chunksfts/` — BM25 mirror schema and write path
- `server/internal/httpapi/workspacesearch.go` — two-stage hybrid endpoint
- `server/internal/httpapi/search.go` + `search_merge.go` — per-project search and result grouping
- `server/internal/symbolindex/` — symbol/refs/defs SQLite tables
- [`benchmarks.md`](benchmarks.md) — quantization vs retrieval-quality measurements
- [`../workspaces.md`](../workspaces.md) — agent-facing workspace search guide
