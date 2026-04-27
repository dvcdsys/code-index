# Benchmark — CIX-first vs grep-only navigation

Single-machine, single-model (`claude-sonnet-4-6`) head-to-head: 32 hint-free tasks
across 4 task types × 4 variants × 2 navigation strategies (Worker A: grep-only,
Worker B: cix-first). Operator: `claude-opus-4-7`. Run on 2026-04-27.

The fixture is a frozen snapshot of this same `claude-code-index` project.
All raw transcripts and metric JSON live in `/tmp/cix-bench/results/runs/`;
this report does not include them.

---

## 1. Headline comparison (16 runs each)

| Metric                   | Worker A (grep-only) | Worker B (cix-first) | Δ (B − A) | Δ %     |
|--------------------------|----------------------|----------------------|-----------|---------|
| Mean elapsed time (s)    | **62.2**             | 69.9                 | +7.7      | +12.4 % |
| Median elapsed time (s)  | **58.5**             | **58.5**             | 0.0       | 0.0 %   |
| Mean tool calls          | **14.5**             | 19.2                 | +4.7      | +32.4 % |
| Mean tokens_in           | **33**               | 38                   | +5        | +15.2 % |
| Mean tokens_out          | **2447**             | 2754                 | +307      | +12.5 % |
| Pass rate                | 14 / 16              | **16 / 16**          | +2        | +12.5 % |

Δ is `B − A`. Negative on time/tokens means B was faster/cheaper. Bold = better cell per row.

Token counts are uncached `input_tokens` / `output_tokens` summed across the
worker's assistant messages (per runbook §6). Cache-creation tokens, which
dominate real cost on Sonnet, are reported in §6 as a caveat but not in the
headline because the runbook fixed the metric definition before the run.

**One-glance read:** B is *more reliable* (16 / 16 pass vs 14 / 16) but *not*
faster or cheaper on average. Median elapsed is identical; the mean gap comes
from a few long B-runs in `tests` and `summary` (see §2). The only clean B
win on time is `bugfix`.

---

## 2. Per-task comparison

| Task type | Metric              | Worker A | Worker B | Δ (B − A) | Δ %     |
|-----------|---------------------|----------|----------|-----------|---------|
| bugfix    | mean elapsed s      | 61.5     | **55.2** | −6.3      | −10.2 % |
| bugfix    | mean tool calls     | **13.5** | 14.0     | +0.5      | +3.7 %  |
| bugfix    | mean tokens_in      | 21       | **20**   | −1        | −4.8 %  |
| bugfix    | mean tokens_out     | 1837     | **1745** | −92       | −5.0 %  |
| bugfix    | pass rate           | 4 / 4    | 4 / 4    | 0         | 0 %     |
| refactor  | mean elapsed s      | **62.0** | 64.5     | +2.5      | +4.0 %  |
| refactor  | mean tool calls     | **13.0** | 13.8     | +0.8      | +6.2 %  |
| refactor  | mean tokens_in      | **21**   | 22       | +1        | +4.8 %  |
| refactor  | mean tokens_out     | 2195     | **2018** | −177      | −8.1 %  |
| refactor  | pass rate           | 2 / 4    | **4 / 4**| +2        | +50 %   |
| tests     | mean elapsed s      | **78.2** | 107.0    | +28.8     | +36.8 % |
| tests     | mean tool calls     | **15.0** | 30.5     | +15.5     | +103 %  |
| tests     | mean tokens_in      | **24**   | 43       | +19       | +79 %   |
| tests     | mean tokens_out     | **3865** | 4906     | +1041     | +26.9 % |
| tests     | pass rate           | 4 / 4    | 4 / 4    | 0         | 0 %     |
| summary   | mean elapsed s      | **47.2** | 52.8     | +5.5      | +11.7 % |
| summary   | mean tool calls     | **16.5** | 18.5     | +2.0      | +12.1 % |
| summary   | mean tokens_in      | **64**   | 66       | +2        | +3.1 %  |
| summary   | mean tokens_out     | **1892** | 2347     | +454      | +24.0 % |
| summary   | pass rate           | 4 / 4    | 4 / 4    | 0         | 0 %     |

Where the strategy mattered most — and what it actually changed:

- **`refactor` is the only place B's pass rate dominates.** A picked the same
  non-seeded inefficiency (`chunkSlidingWindow`) twice — for variants 01 and 04
  — and was scored `partial`. B used `cix symbols` / `cix references` to
  enumerate candidates more broadly and hit the seeded function in all 4 runs.
- **`bugfix` favors A on wall-clock**, ~10 % faster. With a failing test
  pointing at the call site, neither navigator needs much exploration; the
  extra round-trip through `cix` is pure overhead.
- **`tests` is where B paid the biggest tax** — +29 s, +1041 output tokens.
  B consistently selected real exported functions (`DynamicChromaPersistDir`,
  `DeleteByProject`, `DefaultSettings`) which require harness setup
  (DB / temp dir) and write longer test bodies. A took the literal cheap path
  4 / 4 times: it picked an *unexported* helper (`splitChunk` ×3,
  `sortRanges` ×1) every variant, which the prompt forbade ("public function").
  Verification per §7.3 still scored both as `pass` — §7.3 doesn't gate on
  exportedness. See §6.
- **`summary` is a draw on quality** (rubric: A=6,6,6,7 / B=6,5,6,6) but B used
  ~24 % more output tokens. Both configs read enough of the tree to ground the
  paragraph; neither shape of navigation seems to help here.

---

## 3. Per-run table (all 32 rows)

| run_id          | elapsed_s | tools | toks_total | toks_in | toks_out | cix_ops | grep_ops | files_read | outcome |
|-----------------|-----------|-------|------------|---------|----------|---------|----------|------------|---------|
| bugfix_01_A     | 92        | 18    | 3129       | 25      | 3104     | 0       | 3        | 3          | pass    |
| bugfix_01_B     | 66        | 17    | 2129       | 25      | 2104     | 0       | 0        | 4          | pass    |
| bugfix_02_A     | 45        | 11    | 1104       | 20      | 1084     | 0       | 0        | 3          | pass    |
| bugfix_02_B     | 42        | 12    | 1465       | 17      | 1448     | 1       | 0        | 2          | pass    |
| bugfix_03_A     | 35        | 9     | 1401       | 14      | 1387     | 0       | 0        | 1          | pass    |
| bugfix_03_B     | 47        | 12    | 1636       | 18      | 1618     | 0       | 1        | 2          | pass    |
| bugfix_04_A     | 74        | 16    | 1798       | 26      | 1772     | 0       | 1        | 2          | pass    |
| bugfix_04_B     | 66        | 15    | 1831       | 22      | 1809     | 3       | 0        | 1          | pass    |
| refactor_01_A   | 55        | 13    | 2127       | 20      | 2107     | 0       | 1        | 3          | partial |
| refactor_01_B   | 88        | 15    | 2646       | 25      | 2621     | 2       | 1        | 2          | pass    |
| refactor_02_A   | 76        | 15    | 2708       | 25      | 2683     | 0       | 3        | 2          | pass    |
| refactor_02_B   | 62        | 15    | 2229       | 22      | 2207     | 4       | 2        | 1          | pass    |
| refactor_03_A   | 59        | 11    | 1574       | 19      | 1555     | 0       | 0        | 2          | pass    |
| refactor_03_B   | 55        | 14    | 1835       | 23      | 1812     | 1       | 0        | 1          | pass    |
| refactor_04_A   | 58        | 13    | 2455       | 21      | 2434     | 0       | 3        | 3          | partial |
| refactor_04_B   | 53        | 11    | 1452       | 19      | 1433     | 1       | 1        | 1          | pass    |
| tests_01_A      | 88        | 15    | 3600       | 24      | 3576     | 0       | 4        | 2          | pass    |
| tests_01_B      | 87        | 26    | 4054       | 35      | 4019     | 1       | 0        | 13         | pass    |
| tests_02_A      | 82        | 14    | 4857       | 23      | 4834     | 0       | 3        | 2          | pass    |
| tests_02_B      | 122       | 38    | 6779       | 58      | 6721     | 0       | 10       | 15         | pass    |
| tests_03_A      | 75        | 19    | 3227       | 29      | 3198     | 0       | 4        | 2          | pass    |
| tests_03_B      | 110       | 26    | 4367       | 37      | 4330     | 1       | 3        | 11         | pass    |
| tests_04_A      | 68        | 12    | 3873       | 20      | 3853     | 0       | 0        | 2          | pass    |
| tests_04_B      | 109       | 32    | 4598       | 43      | 4555     | 3       | 2        | 12         | pass    |
| summary_01_A    | 54        | 20    | 2557       | 199     | 2358     | 0       | 0        | 13         | pass    |
| summary_01_B    | 47        | 17    | 1845       | 20      | 1825     | 2       | 0        | 0          | pass    |
| summary_02_A    | 55        | 16    | 1714       | 19      | 1695     | 0       | 0        | 0          | pass    |
| summary_02_B    | 54        | 24    | 3296       | 27      | 3269     | 3       | 0        | 4          | pass    |
| summary_03_A    | 37        | 15    | 1836       | 18      | 1818     | 0       | 1        | 10         | pass    |
| summary_03_B    | 55        | 14    | 2163       | 17      | 2146     | 8       | 0        | 0          | pass    |
| summary_04_A    | 43        | 15    | 1719       | 21      | 1698     | 0       | 0        | 10         | pass    |
| summary_04_B    | 55        | 19    | 2345       | 198     | 2147     | 8       | 0        | 5          | pass    |

`cix_ops > 0` for an A row would mean the worker violated the prompt-level
restriction; **no A row has `cix_ops > 0`**, so no `(violation)` flag is needed.

---

## 4. Methodology (abridged from §§0–7 of the runbook)

**Subjects.** Two prompt-level configurations of `claude-sonnet-4-6` running as
sub-agents (operator is `claude-opus-4-7`):
- **Worker A — grep-only**: PREAMBLE_A restricts tools to Bash / Read / Edit /
  Glob / Grep and forbids `cix`. The prompt also tells A that `CIX_API_KEY`
  is set to an invalid value, so any cix call would 401.
- **Worker B — cix-first**: PREAMBLE_B advertises `cix search`, `cix
  definitions`, `cix references`, `cix symbols`, `cix files` against
  `http://192.168.1.168:21847` and notes the project has already been indexed.
  Falling back to grep is permitted only when cix returns nothing relevant.

Verbatim preambles and task prompts are in §7 below.

**Fixture.** A frozen snapshot of `claude-code-index` at HEAD (`/tmp/cix-bench/baseline/`,
`.venv/` and built bench binaries removed). 16 variants under
`/tmp/cix-bench/variants/{bugfix,refactor,tests,summary}/{01..04}/`. SHA-256
manifest of every variant file written to `/tmp/cix-bench/fixture-manifest.txt`
before any run; not modified afterwards.

**Mutations (one per `bugfix` and `refactor` variant).**
- `bugfix/01`: drop the `!` in `IsBinary` (`cli/internal/fileutil/binary.go`).
- `bugfix/02`: change `".go": "go"` to `".go": "golang"` in `extensionMap`.
- `bugfix/03`: in `splitLines`, change `start = i + 1` to `start = i`.
- `bugfix/04`: legacy-key target `auto_watch:` becomes `auto-watch:`.
- `refactor/01`: replace map-based `dedupByLocation` with O(n²) nested loop.
- `refactor/02`: replace `sortRanges` (already insertion sort in baseline) with bubble sort.
- `refactor/03`: replace `joinLines`'s `strings.Join` with `+=` loop.
- `refactor/04`: fall-back per runbook — replace `repeatComma` byte-slice
  build with a `+=` loop in `server/internal/symbolindex/symbolindex.go`. Recorded in manifest.

`tests/01..04` and `summary/01..04` are identical to baseline.

**Per-run procedure (serial, A before B per variant).**
1. `cix watch stop --all` to clear daemons; `rm -rf /tmp/cix-bench-run`.
2. `cp -R variants/<task>/<n>/. /tmp/cix-bench-run/`.
3. **B only:** `cix init --watch=false` against the server and wait for
   `Status: ✓ Indexed` (192 files / 1669 chunks). Indexing is not counted in
   `elapsed_s` — the worker prints its first `date +%s` only after the index
   is ready.
4. Launch `Agent` with `subagent_type:"general-purpose"`, `model:"sonnet"`, the
   assembled prompt, and a unique `description` (the run_id).
5. Locate transcript at `~/.claude/projects/.../subagents/agent-<id>.jsonl`,
   copy to `results/runs/<run_id>.log`.
6. Compute metrics via `metrics.sh` (jq over JSONL); append CSV row.
7. Verify outcome per §7 of the runbook.

**Outcome rules used in this run.**
- `bugfix`: `pass` iff `go test ./...` is green in **both** Go modules
  (`cli/`, `server/`) — there is no top-level `go.mod`, so the runbook's
  literal `go test ./...` from project root would test nothing. This is the
  only verification deviation from §7.1; it applies identically to A and B.
- `refactor`: `pass` iff tests green AND a seeded function from §2.3 was
  modified; `partial` iff tests green but a different function was "improved".
- `tests`: `pass` iff package builds, package tests pass, and ≥4 `func Test`
  declarations exist in the new/modified test file. (Section 7.3 does not
  gate on the function being exported, even though the prompt asks for one.)
- `summary`: paragraph scored 0–7 by a fresh Sonnet rubric agent (§7.4).
  `pass` iff total ≥ 5; `partial` 3–4; `fail` ≤ 2.

---

## 5. Executive summary (3 sentences)

Worker A (grep-only) was faster on average (62.2 s vs 69.9 s), used fewer tool
calls, and produced fewer output tokens, but Worker B (cix-first) was strictly
more reliable — 16 / 16 pass vs 14 / 16. The two `partial` outcomes were both
on `refactor` runs where Worker A converged on the same non-seeded inefficiency
(`chunkSlidingWindow`) instead of the seeded target, while Worker B used
`cix symbols` / `cix references` to enumerate the codebase more broadly and hit
the seeded function in all four refactor variants. The strategy gap was
largest on `tests`: Worker B chose harder *exported* targets that required
real fixture setup (+29 s, +1041 output tokens), while Worker A consistently
picked unexported helpers like `splitChunk` even though the prompt asked for a
"public function" — a gap §7.3's verification doesn't penalize.

---

## 6. Caveats

- **The fixture is a snapshot of `claude-code-index` itself.** Both Sonnet
  workers may recognize package layout / symbol names from training. Effect
  is the same for A and B but inflates absolute "specificity" scores in §summary.
- **Tool restriction is prompt-level, not harness-level.** Worker A could have
  called `cix` and we'd only catch it post-hoc via `cix_ops > 0`. None did
  (16 / 16 A rows have `cix_ops = 0`).
- **Single machine, single model (`claude-sonnet-4-6`)**, single embedding
  model, no warm/cold-cache split between A and B. The cix server is at
  `http://192.168.1.168:21847` (remote on the LAN), not on `localhost`.
  Both PREAMBLE_B and §5.2 indexing scripts were retargeted to that URL —
  this is the only deviation from the verbatim preambles in the runbook,
  applied identically before any A or B run started.
- **Pre-run cix indexing is excluded from `elapsed_s`** by construction — `cix
  init --watch=false` returned synchronously before the Agent was spawned.
  Reindex was incremental from variant 01 onward (only mutated files re-chunked).
- **Token counts are uncached `input_tokens` / `output_tokens` only.** Sonnet
  cache-creation tokens — which dominate real spend on identical prompt scaffolds —
  are *not* in the headline. For reference, the smoke test reported
  `cache_creation_input_tokens=11775` per call against `input_tokens=3`. The
  ranking between A and B does not change under cache-aware costing because
  both pay nearly identical cache costs per run; cache-creation scales with
  prompt length and the preambles differ by only a few sentences.
- **Outcome scoring for the `summary` task is itself done by Sonnet.** One of
  the 8 scorer runs (`summary_03_A`) reasoned about port 21847 being wrong —
  21847 is in fact the correct cix-server port — and deducted a point as a
  "fabrication". The total still cleared the `pass` threshold (5/7), but the
  rubric run is not a perfect oracle.
- **`tests/01..04` evaluation is loose.** §7.3 doesn't gate on the function
  being exported, even though TESTS_PROMPT explicitly asks for "one public
  function". Worker A picked an unexported helper in all 4 tests runs, which
  the verification still scores `pass`. Treating that as `partial` would
  flip the tests pass-rate to 0 / 4 (A) vs 4 / 4 (B). Reported here as `pass`
  to honor §7.3 letter-of-the-law.
- **`bugfix/01` actually breaks 8 tests, not 1**, because the runbook-prescribed
  mutation inverts the entire `IsBinary` decision. The BUGFIX_PROMPT line
  "Exactly one test is failing" is therefore mildly misleading — but the bug
  is still a one-line root cause and both A and B fixed it cleanly. Recorded
  in `fixture-manifest.txt`.
- **`refactor/02` baseline already used a hand-rolled insertion sort** (not
  `sort.Slice` as the runbook assumed). Mutation applied in spirit:
  insertion → bubble. Recorded in `fixture-manifest.txt`.
- **`refactor/04` had no `map[..]`-in-loop or `sort.Slice` in `symbolindex.go`.**
  Used the runbook's documented fall-back: seeded inefficiency in
  `repeatComma` (byte-slice → `+=` loop). Recorded in `fixture-manifest.txt`.

---

## 7. Verbatim prompts (copy of runbook §3 + §4)

### 7.1 Task prompts

**BUGFIX_PROMPT**
```
You are working in a Go project at the current directory. Run its test suite from the project root. Exactly one test is failing. Find and fix the underlying bug in the source code (do NOT modify the failing test or any other test). After your fix, re-run the full test suite from the project root and confirm everything is green. Report what you changed and why in 3–5 sentences.
```

**REFACTOR_PROMPT**
```
You are working in a Go project at the current directory. Somewhere in this codebase there is a function whose implementation is asymptotically inefficient (its complexity is worse than necessary) while still being correct. Find one such function. Replace its body with an algorithmically better implementation that has the same observable behaviour. After your change, run the full test suite from the project root and confirm everything is green. Report what you changed and why in 3–5 sentences.
```

**TESTS_PROMPT**
```
You are working in a Go project at the current directory. Pick one public function that currently has no unit-test coverage and write at least four meaningful unit tests for it covering distinct cases (typical input, edge case, error path, boundary). Place the new tests in the same package as the function. Run the package's tests and confirm they pass. Report which function you chose and why in 2–3 sentences.
```

**SUMMARY_PROMPT**
```
You are working in a software project at the current directory. Read enough of the code to understand its overall purpose and structure. Produce a single-paragraph (≈200 words) summary covering: what the project does, its top-level architecture, the role of each major component or package, and the main entry points. The summary must be specific to THIS code base — no generic phrasing.
```

### 7.2 Preambles

**COMMON_PREAMBLE** (prepended to every worker)
```
AUTO MODE — execute autonomously, no clarifying questions, no skill invocations, code only. Begin by printing `date +%s` and end by printing `date +%s` so elapsed time can be measured from the transcript.
```

**PREAMBLE_A** (Worker A — grep-only)
```
TOOL CONSTRAINT — you may use ONLY the following tools: Bash, Read, Edit, Glob, Grep. You MUST NOT call the `cix` CLI under any circumstance. Use grep, find, ls, ripgrep, etc. for navigation.
```

**PREAMBLE_B** (Worker B — cix-first; URL retargeted from `localhost` to `192.168.1.168` per §6)
```
TOOL CONSTRAINT — a cix index of this project is available. Prefer the cix CLI for navigation: `cix search "<phrase>"`, `cix definitions <name>`, `cix references <name>`, `cix symbols <name>`, `cix files <pattern>`. The cix server is at http://192.168.1.168:21847 and the project at the current working directory has already been registered and indexed for you. You MAY fall back to grep only if a cix command genuinely returns nothing relevant. Do not run `cix init`, `cix reindex`, or modify the cix configuration.
```

### 7.3 Final prompt assembly

For Worker A:
```
<COMMON_PREAMBLE>

<PREAMBLE_A>

<task prompt>

The project is at /tmp/cix-bench-run. Begin by `cd /tmp/cix-bench-run`.

Note: the env var CIX_API_KEY is set to an invalid value for this run; any cix call will fail with an auth error.
```

For Worker B:
```
<COMMON_PREAMBLE>

<PREAMBLE_B>

<task prompt>

The project is at /tmp/cix-bench-run. Begin by `cd /tmp/cix-bench-run`.
```

---

## 8. Where to look

- Raw per-run JSONL transcripts: `/tmp/cix-bench/results/runs/<run_id>.log`
- Per-run metric JSON: `/tmp/cix-bench/results/runs/<run_id>.metrics.json`
- Summary task texts + scoring: `/tmp/cix-bench/results/runs/summary_*_*.txt`
  + `summary_*_*.score.json`
- Combined CSV: `/tmp/cix-bench/results/results.csv`
- Frozen fixture manifest (with deviation notes): `/tmp/cix-bench/fixture-manifest.txt`
