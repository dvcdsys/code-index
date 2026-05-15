# Benchmarks

Index of dated benchmark snapshots. None of these are a continuously
maintained dashboard — each is a measurement taken at a point in time
against a frozen fixture, and they age as the product moves. Each
section ends with a "last measured" date so a reader can decide
whether the numbers are still trustworthy.

If you re-run a benchmark and find different numbers, prefer adding a
new dated section over editing the old one in place — the history is
useful when reasoning about regressions.

---

## 1. cix-first vs grep-only navigation

**File:** [`benchmark-cix-vs-grep.md`](benchmark-cix-vs-grep.md)
**Last measured:** 2026-04-27
**Setup:** 32 hint-free tasks (4 task types × 4 variants × 2 navigation
strategies) on a frozen snapshot of `claude-code-index` itself, with
`claude-sonnet-4-6` as worker and `claude-opus-4-7` as operator.

**Headline.** cix-first is *more reliable* (16 / 16 pass vs 14 / 16
for grep-only) but *not* faster or cheaper on average (+12 % elapsed,
+32 % tool calls, +12 % tokens). The reliability gap shows up on
refactor tasks, where grep-only converged on the wrong target twice.
On bugfix tasks where a failing test already points at the call site,
grep is slightly faster because it skips the cix round-trip.

See the file itself for per-task tables, the 32-row raw run table,
methodology, and caveats.

---

## 2. CodeRankEmbed GGUF quantization

**File:** [`benchmark-q8-vs-fp16.md`](benchmark-q8-vs-fp16.md)
**Last measured:** 2026-04-23
**Setup:** Apple Silicon (Metal), 218 code chunks + 20 queries from
this repo, k=10. fp16 reference is `nomic-ai/CodeRankEmbed` via
sentence-transformers; GGUFs from `limcheekin/CodeRankEmbed-GGUF`.

| Quant | Size | Jaccard@10 | Recall@10 | Kendall τ | Verdict |
|---|---:|---:|---:|---:|---|
| fp16 reference | ~522 MB | — | — | — | reference |
| F16 GGUF | 261 MB | 0.894 | 0.940 | 0.879 | pass |
| **Q8_0** (current default) | **139 MB** | **0.894** | **0.940** | **0.861** | **pass** |
| Q5_K_M | 98 MB | 0.815 | 0.895 | 0.786 | fail (Recall) |
| Q4_K_M | 86 MB | 0.787 | 0.875 | 0.760 | fail (Recall) |

Acceptance thresholds: Jaccard@10 ≥ 0.70, Recall@10 ≥ 0.90, Kendall
τ ≥ 0.50.

**Conclusion.** Q8_0 is the sweet spot — identical top-k retrieval to
fp16 at half the disk footprint, ~2.6× faster than the
sentence-transformers reference on Apple Silicon. Q5_K_M and Q4_K_M
both miss Recall@10 by a hair or more and aren't recommended.

The default shipped model is `awhiteside/CodeRankEmbed-Q8_0-GGUF`
(equivalent quality, more reliable HF availability than the
`limcheekin/*` repo).

---

## 3. VRAM profiling

**File:** [`vram-profiling.md`](vram-profiling.md)
**Status:** Methodology + expected baseline only; actual measured
numbers have not been backfilled. The doc states "Once
`profile_vram.py` has been run … this section should be replaced with
actual measured deltas."

Expected baseline (CodeRankEmbed Q8_0 on RTX 3090): ~0.5–0.7 GB idle
VRAM (weights ~200–250 MB + pre-allocated `n_ctx=8192` context
~200–400 MB).

If you re-run the profiler, update `vram-profiling.md` in place — the
file was always intended as a placeholder.

---

## Raw artefacts

The dated grep-vs-cix run also produced raw transcripts and metric
JSON. Per the README in that file, they live in
`/tmp/cix-bench/results/runs/` and are not checked into the repo —
the markdown file captures the headline numbers and methodology that
remain useful once the raw logs are gone.
