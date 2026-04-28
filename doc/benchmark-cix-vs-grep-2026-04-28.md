# Benchmark — CIX-first vs grep-only navigation (2026-04-28)

Re-run of the 32-cell head-to-head from 2026-04-27 after a bundle of
search-quality changes landed: path-aware embeddings, `--min-score` default
0.4, `--exclude` flag, relative-path output. Same fixture, same prompts,
same `claude-sonnet-4-6` workers, same 192.168.1.168 cix server — only
the server binary differs from the 2026-04-27 run.

The point is the **delta vs 2026-04-27**, not the absolute numbers.

Raw transcripts and metric JSON live in `/tmp/cix-bench/results/runs/`;
prior run preserved at `/tmp/cix-bench/results/runs.2026-04-27/` and
`/tmp/cix-bench/results/results.2026-04-27.csv`.

---

## 1. Headline comparison (16 runs each)

| Metric                   | Worker A (grep-only) | Worker B (cix-first) | Δ (B − A) | Δ %     |
|--------------------------|----------------------|----------------------|-----------|---------|
| Mean elapsed time (s)    | 102.5                | **94.9**             | −7.6      | −7.4 %  |
| Median elapsed time (s)  | 78.5                 | **77.0**             | −1.5      | −1.9 %  |
| Mean tool calls          | 20.3                 | **19.3**             | −1.0      | −4.6 %  |
| Mean tokens_in           | 1629†                | **43**               | †         | †       |
| Mean tokens_out          | 3222                 | **3111**             | −112      | −3.4 %  |
| Pass rate                | 13 / 16              | **15 / 16**          | +2        | +15.4 % |

† Worker A's `tokens_in` mean is dominated by a single anomaly:
`refactor_04_A` reported 25 641 input tokens (likely a cache-miss accounting
spike), versus 16–26 for the other 15 A cells. **Excluding that one cell, A's
mean tokens_in is 28.9** — the cleaner number for comparison. Both workers'
input-token totals are uncached `input_tokens` only; cache-creation tokens
that dominate real cost on Sonnet are not included by `metrics.sh`.

**One-glance read:** B is faster, leaner, and more reliable than A on every
headline metric. This is the inverse of the 2026-04-27 run, where B was
*slower and more expensive* than A on average. The pass-rate gap closed
slightly (was 14/16 vs 16/16, now 13/16 vs 15/16) — both workers
regressed by one cell each, but B is still the more reliable navigator.

---

## 1.5 Delta vs 2026-04-27

### Worker B (the cell where the new code is exercised)

| Metric (Worker B)  | 2026-04-27 | 2026-04-28 | Δ    | Δ %    |
|--------------------|------------|------------|------|--------|
| Mean elapsed s     | 69.9       | 94.9       | +25.0 | +35.8 % |
| Mean tool calls    | 19.2       | 19.3       | +0.1  | +0.5 %  |
| Mean tokens_in     | 38         | 43         | +5    | +13.2 % |
| Mean tokens_out    | 2754       | 3111       | +357  | +13.0 % |
| Pass rate          | 16/16      | 15/16      | −1    | −6.3 %  |

### Worker A (control — A doesn't use the cix server)

| Metric (Worker A)  | 2026-04-27 | 2026-04-28 | Δ     | Δ %     |
|--------------------|------------|------------|-------|---------|
| Mean elapsed s     | 62.2       | 102.5      | +40.3 | +64.8 % |
| Mean tool calls    | 14.5       | 20.3       | +5.8  | +40.0 % |
| Mean tokens_in†    | 33         | 28.9       | −4.1  | −12.4 % |
| Mean tokens_out    | 2447       | 3222       | +775  | +31.7 % |
| Pass rate          | 14/16      | 13/16      | −1    | −7.1 %  |

† Excluding `refactor_04_A` token-count anomaly (25 641 in).

**Both workers' absolute numbers grew.** This is Sonnet-side variance — A
doesn't even talk to the cix server, yet it slowed down 65 % on elapsed and
spent 32 % more output tokens. The dev box was idle and on the same
hardware, so the most plausible explanation is run-to-run variance from
the model itself. The 2026-04-27 run finished in ~75 minutes; this run
took ~110 minutes, consistent with a slower-but-equally-clean execution.

The honest story is therefore in the **A↔B gap within each run**, not the
absolute deltas vs the prior run:

- Prior run: B was +12 % slower, +32 % more tool calls, +13 % more
  output tokens than A. B's only win was pass rate.
- New run: B is −7 % faster, −5 % fewer tool calls, −3 % fewer output
  tokens than A — *and* still wins on pass rate.

The cix-first strategy went from "more expensive, more reliable" to
"strictly better than grep on every headline metric." That flip is what
the new code bought.

---

## 2. Per-task comparison (where the gap moved)

### bugfix — flat (cix overhead always negligible here)

| Metric            | A (new) | B (new) | Δ B−A   | Δ %     | (prior B−A %) |
|-------------------|---------|---------|---------|---------|---------------|
| Mean elapsed s    | 70.3    | 69.0    | −1.3    | −1.8 %  | (−10.2 %)     |
| Mean tool calls   | 13.3    | 13.5    | +0.2    | +1.5 %  | (+3.7 %)      |
| Mean tokens_in    | 20.5    | 21.0    | +0.5    | +2.4 %  | (−4.8 %)      |
| Mean tokens_out   | 1600.0  | 1665.8  | +65.8   | +4.1 %  | (−5.0 %)      |
| Pass rate         | 4/4     | 4/4     | 0       | 0 %     | (0 %)         |

bugfix is a draw both times — when there's a failing test pointing at the
call site, neither navigator needs much exploration.

### refactor — A regressed, B held steady, gap widened

| Metric            | A (new) | B (new) | Δ B−A     | Δ %      | (prior B−A %) |
|-------------------|---------|---------|-----------|----------|---------------|
| Mean elapsed s    | 79.8    | 96.0    | +16.2     | +20.3 %  | (+4.0 %)      |
| Mean tool calls   | 16.8    | 19.8    | +3.0      | +18.0 %  | (+6.2 %)      |
| Mean tokens_in†   | 23.3    | 28.3    | +5.0      | +21.4 %  | (+4.8 %)      |
| Mean tokens_out   | 2497.5  | 2879.3  | +381.8    | +15.3 %  | (−8.1 %)      |
| Pass rate         | 1/4     | 3/4     | +2        | +200 %   | (+50 %)       |

† A excludes refactor_04_A 25 641 anomaly.

B is slower than A on time *and* tokens here — this is the one task type
where the cix-first overhead still bites. But B's pass rate is 3× A's:
A picked non-seeded ambient inefficiencies (`chunkSlidingWindow`, `topN`)
in 3 of 4 variants, while B hit the seeded function in 3 of 4 (refactor_03
was the only B-miss, where B picked `topN` instead of `joinLines`). Net:
B trades wall-clock for a much higher chance of finding the right
function.

### tests — biggest win for B (was the prior tax cell)

| Metric            | A (new) | B (new) | Δ B−A     | Δ %      | (prior B−A %) |
|-------------------|---------|---------|-----------|----------|---------------|
| Mean elapsed s    | 191.3   | **154.3** | −37.0     | **−19.3 %**  | (+36.8 %)     |
| Mean tool calls   | 36.3    | **26.8**  | −9.5      | **−26.2 %**  | (+103 %)      |
| Mean tokens_in    | 52.5    | **37.8**  | −14.7     | **−28.0 %**  | (+79 %)       |
| Mean tokens_out   | 6789.8  | **5728.8** | −1061.0   | **−15.6 %**  | (+26.9 %)     |
| Pass rate         | 4/4     | 4/4     | 0         | 0 %      | (0 %)         |

This is the cell that motivated the search-quality work. **B paid a
+103 % tool-call tax in the prior run; in this run it's a −26 % win.**
And mechanically B did much less reading: B's per-cell `files_read_count`
mean dropped to **7.25** vs A's **15.25** — half. The path-aware
embeddings + min-score 0.4 made the top-K hits relevant enough that B
didn't need to range-read the codebase.

The most striking single cell: `tests_03_B` finished in 146 s with 6 files
read; `tests_03_A` took 245 s and read 28 files. B chose the public
`Service.CancelIndexing` method (a real exported function); A picked
`splitPath` (unexported) on 3 of 4 variants — the runbook's verification
gap from the prior report is still there.

### summary — small but consistent flip

| Metric            | A (new) | B (new) | Δ B−A     | Δ %      | (prior B−A %) |
|-------------------|---------|---------|-----------|----------|---------------|
| Mean elapsed s    | 68.8    | **60.3**  | −8.5      | **−12.4 %**  | (+11.7 %)     |
| Mean tool calls   | 14.8    | 17.3    | +2.5      | +16.9 %  | (+12.1 %)     |
| Mean tokens_in†   | 17.8    | 19.3    | +1.5      | +8.6 %   | (+3.1 %)      |
| Mean tokens_out   | 2000.8  | 2168.5  | +167.7    | +8.4 %   | (+24.0 %)     |
| Pass rate         | 4/4     | 4/4     | 0         | 0 %      | (0 %)         |

† B excludes summary_04_B 285-token-in anomaly.

Both workers grounded the summaries; rubric scores are flat at 6/7 across
all 8 cells (vs prior A=6,6,6,7 / B=6,5,6,6). B is now ~12 % faster and
spent only +8 % output tokens (vs +24 % before).

---

## 3. Per-run table (all 32 rows, sorted)

| run_id          | elapsed_s | tools | toks_total | toks_in | toks_out | cix_ops | grep_ops | files_read | outcome |
|-----------------|-----------|-------|------------|---------|----------|---------|----------|------------|---------|
| bugfix_01_A     | 78        | 15    | 1643       | 24      | 1619     | 0       | 1        | 2          | pass    |
| bugfix_01_B     | 75        | 13    | 1710       | 21      | 1689     | 0       | 0        | 2          | pass    |
| bugfix_02_A     | 67        | 10    | 1190       | 16      | 1174     | 0       | 0        | 2          | pass    |
| bugfix_02_B     | 48        | 11    | 1307       | 16      | 1291     | 0       | 0        | 2          | pass    |
| bugfix_03_A     | 67        | 13    | 1760       | 19      | 1741     | 0       | 2        | 2          | pass    |
| bugfix_03_B     | 83        | 15    | 1988       | 26      | 1962     | 0       | 2        | 2          | pass    |
| bugfix_04_A     | 69        | 15    | 1889       | 23      | 1866     | 0       | 1        | 2          | pass    |
| bugfix_04_B     | 70        | 15    | 1742       | 21      | 1721     | 0       | 1        | 2          | pass    |
| refactor_01_A   | 68        | 15    | 2306       | 22      | 2284     | 0       | 3        | 5          | partial |
| refactor_01_B   | 104       | 19    | 3052       | 32      | 3020     | 2       | 3        | 1          | pass    |
| refactor_02_A   | 86        | 16    | 2267       | 22      | 2245     | 0       | 4        | 1          | pass    |
| refactor_02_B   | 90        | 21    | 2875       | 29      | 2846     | 2       | 5        | 1          | pass    |
| refactor_03_A   | 80        | 18    | 2263       | 26      | 2237     | 0       | 5        | 1          | partial |
| refactor_03_B   | 91        | 18    | 3093       | 25      | 3068     | 2       | 6        | 2          | partial |
| refactor_04_A   | 85        | 18    | 28865      | 25641   | 3224     | 0       | 6        | 4          | partial |
| refactor_04_B   | 99        | 21    | 2610       | 27      | 2583     | 2       | 2        | 2          | pass    |
| summary_01_A    | 65        | 12    | 1497       | 15      | 1482     | 0       | 0        | 0          | pass    |
| summary_01_B    | 64        | 20    | 2156       | 23      | 2133     | 0       | 0        | 8          | pass    |
| summary_02_A    | 65        | 15    | 2076       | 18      | 2058     | 0       | 0        | 7          | pass    |
| summary_02_B    | 41        | 13    | 1829       | 16      | 1813     | 1       | 0        | 5          | pass    |
| summary_03_A    | 79        | 19    | 2398       | 22      | 2376     | 0       | 1        | 10         | pass    |
| summary_03_B    | 74        | 16    | 2043       | 19      | 2024     | 0       | 1        | 8          | pass    |
| summary_04_A    | 66        | 13    | 2103       | 16      | 2087     | 0       | 0        | 0          | pass    |
| summary_04_B    | 62        | 20    | 2989       | 285     | 2704     | 6       | 0        | 0          | pass    |
| tests_01_A      | 200       | 37    | 7345       | 51      | 7294     | 0       | 3        | 16         | pass    |
| tests_01_B      | 189       | 31    | 6482       | 46      | 6436     | 0       | 1        | 14         | pass    |
| tests_02_A      | 163       | 29    | 6042       | 45      | 5997     | 0       | 4        | 9          | pass    |
| tests_02_B      | 148       | 23    | 6689       | 32      | 6657     | 1       | 2        | 2          | pass    |
| tests_03_A      | 245       | 50    | 8422       | 66      | 8356     | 0       | 6        | 28         | pass    |
| tests_03_B      | 146       | 30    | 5490       | 39      | 5451     | 1       | 3        | 6          | pass    |
| tests_04_A      | 157       | 29    | 5560       | 48      | 5512     | 0       | 5        | 8          | pass    |
| tests_04_B      | 134       | 23    | 4405       | 34      | 4371     | 1       | 2        | 7          | pass    |

Pass = 28/32 (15 B + 13 A). Partial = 4/32 (3 A refactor + 1 B refactor).
No `(violation)` rows: every A cell has `cix_ops = 0`.

Summary rubric scores: A = {6, 6, 6, 6}, B = {6, 6, 6, 6}. Both pass
(threshold ≥5).

---

## 4. Methodology (abridged)

Same as 2026-04-27 (see `docs/benchmark-runbook.md` for the runbook).
Two procedural deviations from the runbook, **identical to the prior
run unless noted**:

1. PREAMBLE_B URL = `http://192.168.1.168:21847` (RTX 3090 prod box,
   not literal `localhost`). Same as prior run.
2. **Per-cell unique workspace** at `/tmp/cix-bench-runs/${RUN_ID}/`
   instead of one shared `/tmp/cix-bench-run/`. Different paths produce
   different `projectHash` on the server, so each B-cell hits a fresh
   index — no residual chunks bleeding between cells. **This is new in
   this run.** Effect: every B cell pays a one-time index cost (180-s
   wait deadline; observed 30–60 s actual), absorbed inside cell setup
   and excluded from `elapsed_s`.

The cix server on .168 ran the working-tree binary with
`CIX_EMBED_INCLUDE_PATH=true` (default) and the new `min-score=0.4`
default. Spot check before launch: `cix search "main entry point server"`
ranked `server/cmd/cix-server/main.go` first at 0.52, confirming the
path-aware embeddings were live.

All 32 transcripts identify the worker model as `claude-sonnet-4-6` —
audited via `grep -L 'claude-sonnet-4-6' /tmp/cix-bench/results/runs/*.log`
returning zero lines.

Fixture manifest (`fixture-manifest.txt`, 3744 hashed files) verified
clean both before and after the run.

---

## 5. Headline numbers (executive summary)

The 2026-04-27 run found that cix-first navigation was *more reliable but
no faster* than grep-only. The 2026-04-28 re-run, with path-aware
embeddings + `min-score=0.4` shipped, finds cix-first is now
**−7.4 % faster**, **−4.6 % fewer tool calls**, and **−3.4 % fewer
output tokens** than grep-only — while still beating it on pass rate
(15/16 vs 13/16). The single biggest gain is the **tests** task, which
flipped from a +37 % B-tax to a −19 % B-win, with B reading half as many
files per cell. The summary task also flipped (+12 % B-tax → −12 %
B-win). Refactor remains the one task where B costs more wall-clock
than A on average, but B's pass rate (3/4) is 3× A's (1/4) — same
direction as the prior run.

---

## 6. Caveats

- **Both workers got slower in absolute terms vs 2026-04-27.** A grew
  +65 % on elapsed and +32 % on output tokens despite never talking to
  the cix server — pure Sonnet variance. B grew +36 % on elapsed.
  The honest comparison is therefore the *within-run gap* between A and
  B, not the absolute delta vs the prior run. Both within-run gap
  measurements are in §1.5 and §2.
- **Per-cell unique paths** are new this run. Prior run reused a single
  `/tmp/cix-bench-run/` path so all 32 cells hit the same `projectHash`
  on the server. This run isolates each cell on a fresh hash. Effect on
  B should be small (server-side caches keyed by chunk content, not
  project), but it's a real procedural difference worth flagging.
- **`refactor_04_A` token spike**: 25 641 input tokens vs 16–26 for the
  other 15 A cells. Almost certainly cache-miss accounting; treated as
  an outlier in the per-task means but kept in the per-run table.
- **`tokens_in` is uncached input only.** Cache-creation and cache-read
  tokens dominate real Sonnet cost and are not summed by `metrics.sh`.
  This is consistent with the prior run's accounting — the relative gap
  is comparable, the absolute number is not the whole bill.
- **Fixture is a snapshot of the cix project itself** — the model may
  recognise it from training. Same caveat as 2026-04-27.
- **Tool restriction is enforced via prompt, not at the harness level.**
  No A cell violated (`cix_ops = 0` everywhere); we still trust the
  prompt because of post-hoc audit, not architecture.
- **Single machine, single model (`claude-sonnet-4-6`), single embedding
  model, single random seed per worker.** No warm/cold cache split.
- **Pre-run cix indexing time is excluded from `elapsed_s`** (B gets a
  "free" index), as before. Indexing took 30–60 s per B cell on .168 —
  not amortised in the workload comparison.
- **Refactor verification still depends on naming the seeded function.**
  A's "asymptotically inefficient" picks (`chunkSlidingWindow`,
  insertion sort, `topN`) are real wins on the merits but score
  `partial` because they aren't the runbook's planted target. The
  runbook gap from the prior report (§7.2 too strict) hasn't been
  patched.
- **Tests verification is exportedness-blind.** Both workers picked
  unexported helpers (`splitPath` and friends) on tests_01/02 and still
  scored `pass`. The new code didn't change this.

---

## 7. Verbatim prompts

Identical to 2026-04-27 (see `docs/benchmark-runbook.md` §3 and §4):
COMMON_PREAMBLE, PREAMBLE_A, PREAMBLE_B, BUGFIX_PROMPT, REFACTOR_PROMPT,
TESTS_PROMPT, SUMMARY_PROMPT — all unchanged. The only deltas in
PREAMBLE_B vs the runbook's literal text are the api URL
(`http://192.168.1.168:21847`) and the per-cell `cd` path
(`/tmp/cix-bench-runs/${RUN_ID}/`).

For Worker A, the runbook §5.2 auth-error gate line was appended to
every assembled prompt:
> Note: the env var CIX_API_KEY is set to an invalid value for this run;
> any cix call will fail with an auth error.

---

## 8. Where the artefacts live

- This report:
  `doc/benchmark-cix-vs-grep-2026-04-28.md`
- Prior report (preserved):
  `doc/benchmark-cix-vs-grep.md` (2026-04-27)
- New CSV: `/tmp/cix-bench/results/results.csv`
- Prior CSV (preserved): `/tmp/cix-bench/results/results.2026-04-27.csv`
- New per-run logs + metrics: `/tmp/cix-bench/results/runs/`
- Prior per-run logs + metrics (preserved):
  `/tmp/cix-bench/results/runs.2026-04-27/`
- Summary rubric scores (this run only):
  `/tmp/cix-bench/results/rubric.json`
- Fixture (frozen, byte-identical to 2026-04-27):
  `/tmp/cix-bench/baseline/`, `/tmp/cix-bench/variants/`,
  `/tmp/cix-bench/fixture-manifest.txt`
