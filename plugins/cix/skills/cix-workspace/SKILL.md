---
name: cix-workspace
description: Cross-project research workflow for cix workspaces. Manual-invocation skill — load explicitly via `/cix-workspace <task>` when a request spans multiple repos and you want the full workflow guidance (which repos? what code? what changes?) plus the trust rules for interpreting workspace search responses. Bundles the cix-workspace-investigator sub-agent for parallel per-repo fan-out. Do not auto-trigger.
user-invocable: true
allowed-tools: Bash(cix *), Agent
---

# `cix workspace` — Cross-Project Research Workflow

You usually work inside one repo — your **primary project** — the
directory the user opened you in. Most tasks are fully contained there
and `cix search` / `cix definitions` / `cix references` are the right
tools.

But some tasks are not contained. A request like "wire feature X
through the platform" can touch a half-dozen repos in different
languages, layers, and shapes — a service, a shared library, the
infra manifests, an API spec. Reading the primary repo alone gives
you 1/N of the picture. Worse, you don't know which N repos are
actually involved until you look.

`cix workspace` is the tool for that. It searches every repo in a
named workspace at once and tells you:

1. **Which repos are actually relevant to this request.**
2. **Which code in those repos is the entry point.**
3. **What changes need to land in each, and in what order.**

Those three questions are the *goal* of using this skill. Don't jump
to implementation before you can answer all three with evidence.

---

## When to reach for workspace search

| Signal in the user's request | What to do |
|---|---|
| Names a product / acronym you don't fully recognize from primary repo | Workspace search the acronym, see where it lives |
| "Add X to the Y flow", "wire Z into A" | Workspace search Y or Z — likely cross-cutting |
| "Across services", "between repos", "end-to-end" | Workspace search the feature |
| Talks about an event / topic / contract / API endpoint | Workspace search the event name |
| References infra / deployment alongside code | Workspace search — infra repo is probably in the workspace too |
| "How do I change X in production / staging" | Workspace search BUT look past top-1 — the answer is usually a manifests/config/contract repo even when a code repo ranks higher (rule 7 below) |
| Plain bugfix entirely inside one file | **Don't** workspace search. `cix search` is enough |
| User points at a specific symbol / file path | **Don't.** `cix definitions <name>` or just Read the path |

If you're not sure, run `cix ws` once to see whether the primary
project is even part of a workspace. If it isn't, this skill doesn't
apply.

---

## The workflow

The goal-driven loop. Don't shortcut it. Each step is fast.

### Step 0 — orient

```bash
cix ws                       # list workspaces; find the one your primary is in
cix ws <name>                # describe — confirm repos are indexed (✓ count)
```

If the workspace shows `stale_fts_repos` in any search response later,
trust the dense ranking less — see the troubleshooting section.

### Step 1 — answer "which repos?"

Run workspace search with a **short, term-rich query**, not the full
user sentence:

```bash
# GOOD — short, term-rich (a product acronym + an action verb)
cix ws platform search "rate-limit middleware"

# BAD — full sentence dilutes BM25 with stopwords ("add", "to", "a")
cix ws platform search "Add a rate limit to every API endpoint"
```

Why short: the hybrid algorithm fuses BM25 (literal token match) with
dense (semantic). BM25 carries the project-gating signal — repos that
share zero vocabulary with the query drop out. Common words ("add",
"flow", "for") match everywhere and dilute that signal.

Read the response:

- **`projects[]` is the answer to Q1.** Sorted by `project_score`
  (candidacy). Each entry has `bm25_score` (literal-token overlap)
  and `dense_score` (semantic similarity).
- Projects below the per-query relative threshold are already
  filtered out — you only see the survivors.
- Top entry's `project_score` is your reference. Entries at 60-100%
  of top are core relevant. Entries at 40-60% are secondary. Below
  40% would have been dropped server-side.

**Always include the primary project** even if workspace search ranks
it low — the user's task is rooted there. The workspace's other
repos are dependencies / consumers / providers / counter-parties.

### Step 2 — answer "what code is relevant?"

For each repo from step 1, look at the chunks panel. The chunk list
is interleaved by rank across surviving projects so each repo's top
hit appears early. Use these chunks as **starting points** for a
deeper read, not as the full answer.

For repos other than the primary, you have two options:

**A. Quick scan (≤ 2 repos to investigate):** use single-project
search directly.

```bash
# Search inside one specific project
curl -G -H "Authorization: Bearer $CIX_KEY" \
  --data-urlencode "q=rate limit middleware handler" \
  --data-urlencode "min_score=0" \
  "$CIX_URL/api/v1/projects/$(project_hash)/search"
```

The per-project default `min_score` is `0.2` — light floor that
keeps abstract NL queries non-empty. For drill-down on a natural-
language question ("how does X work end-to-end"), pass `min_score=0`
explicitly to be safe. For strict code-symbol matching, pass `0.4+`.

**B. Fan-out to sub-agents (≥ 3 repos, or you need a thorough read):**
spawn one `cix-workspace-investigator` sub-agent per relevant repo, in
parallel. See the dedicated [Sub-agent fan-out pattern](#sub-agent-fan-out-pattern)
section below for the four-part prompt template, including how to pass
seed chunks with your interpretive commentary.

Run them concurrently (one message, multiple Agent tool calls). When
they report back, you have N independent reads to synthesize, not N
sequential rabbit-holes.

### Step 3 — answer "what changes?"

This is your job, not a sub-agent's. Sub-agents report findings; you
write the plan.

For each relevant repo:

- What needs to change (specific file:line, or a new file).
- Why (which step of the data flow this implements).
- Order constraints (e.g. "shared-models migration must deploy
  before backend reads new field").
- Tests that prove it works.

Confirm with the user before any of this lands. The plan is the
deliverable of this skill; the implementation is a separate step.

### Throughout — ask, don't guess

Trigger a clarifying question when:

- Top-2 projects are at near-equal `project_score` and have different
  labels — the request might fit either repo, ask which.
- `bm25_score` is 0 across all projects → either the FTS index is
  stale (see troubleshooting) OR the user's term doesn't exist
  literally in any repo. Ask the user for the term that *would*
  appear in code ("we call it `Order` in code, not `Trade`").
- A sub-agent reports it can't find a clear entry point — surface
  that uncertainty back to the user, don't paper over it.
- The implementation plan needs a deploy-order assumption — confirm
  who owns each repo and what their cycle looks like.

Don't ask if the answer is obvious from the chunks. The bar is "I
have two plausible interpretations and the wrong one costs the user
real time."

---

## Reading the projects panel — what the numbers mean

```
project-a@main   0.500   5 hits   bm25 0.421   dense 0.556
project-b@main   0.412   5 hits   bm25 0.318   dense 0.498
project-c@main   0.288   3 hits   bm25 0.155   dense 0.362
```

- `project_score` (first column): the α-blended candidacy in [0, 1].
  Top = strongest signal across both retrieval modes.
- `bm25_score` and `dense_score`: the raw per-mode signals. The
  algorithm normalizes these per query before blending — useful for
  diagnosis, not for sorting.
- If `bm25_score` >> `dense_score` for a project: it's relevant
  because of literal token overlap (product name appears in code).
  Trust the surface area but verify semantic relevance manually.
- If `dense_score` >> `bm25_score`: it's relevant because of
  semantic similarity (handler shape matches the query intent) but
  the literal term isn't there. Common when the user's term is a
  product nickname not used in code.
- If both are near zero: you're seeing the project because nothing
  else cleared the gate either. Treat with skepticism.

---

## Trust rules — making sense of the response

These ten rules were derived from a calibration eval (113 synthetic
queries + 5 real engineering tasks against a mixed-domain workspace).
Apply them before acting on workspace-search output. Numbers below
are empirical, not vibes.

### Rule 1 — `chunk.score >= 0.4` is the trust threshold

Chunks with `score < 0.4` are noise about 75% of the time
(rank-inversion and weak-signal FPs from the relative project gate).
Skim them only when the higher-scored chunks don't answer the
question. With the default `min_score=0.4` you usually won't see them
at all; if you passed `min_score=0` (intentional broad sweep), apply
this rule yourself.

### Rule 2 — `chunk.score == 0` is a BM25-only hit, not low confidence

The chunk's project matched the literal query tokens via FTS5 but the
embedding side didn't surface it. These are valuable when the query
carries project-specific identifiers (CamelCase symbols, file names,
acronyms). Discount them when the query is a generic English word
(`error`, `data`, `config`) — common-word BM25 hits are noise.

### Rule 3 — Top-1 of `projects[]` is correct ~70% of the time in real tasks

The synthetic eval measured 91% on single-target queries; real
engineering tasks hit ~70% because real queries often span layers
(see rule 7). When the top-1 project doesn't match your task's
intent, **scan ranks 2–5 before reformulating** — the right repo is
usually there. The `projects[]` panel is the answer to "where do
the words live", not "where should the change happen".

### Rule 4 — Drop down to single-project search for depth

When `projects[]` shows the target at rank 1 with a clear lead
(`project_score` ≥ 1.5× the next), switch to per-project search.
You get file-grouped, deeper results without the cross-project
round-robin cap of 5 chunks per repo.

### Rule 5 — `min_score=0` for intentional cross-project sweeps

Default workspace `min_score` is `0.4`. For queries that should
legitimately span many repos ("authentication", "configuration
loading", "Kafka consumers"), pass `min_score=0` explicitly.
Expect `projects[]` to list 5–8 entries — that's the feature, not a
bug. Ignore rule 1 in this mode: many real positives sit below 0.4
in genuine cross-cutting queries.

### Rule 6 — Add a 3rd disambiguating token, carefully

If two query words are each domain-overloaded (e.g. "client SDK"
could be the generated API client, the shared library, or a model
type), add a third word. **Prefer meta-tokens** (`endpoint`,
`route`, `handler`, `manifest`, `migration`, `config file`) over
tech-stack guesses (`grpc`, `kafka`, `terraform`) — wrong stack
guesses actively rotate the ranking away from the right answer. If
unsure of the stack, run the query without a disambiguator first,
read the top-1 project's language/path patterns, then refine.

### Rule 7 — "Change X in production" → manifests repo, not code repo

For tasks framed as deploying / configuring / overriding a feature,
the answer usually lives in a manifests / config / contract repo
(K8s overlays, Helm charts, OpenAPI specs, environment-specific
yaml). Workspace search ranks by token frequency, so the code repo
typically wins. Look at `projects[]` for repos with **manifests,
config, platform, deploy, contract, openapi, infra** in their
names — those are often the right targets even at rank 3–5.

### Rule 8 — When top-1 doesn't fit, scan first, reformulate second

If you think top-1 is wrong:

1. First, scan ranks 2–5. The right project is there ~80% of the
   time when the layer mismatch caused rule 3 to fail.
2. Only after scanning, reformulate. Reformulating before scanning
   wastes a round-trip and risks the new query introducing fresh
   layer confusion.

### Rule 9 — For per-project NL drill-down, pass `min_score=0` explicitly

When dropping from workspace to per-project search with a natural-
language query (e.g. "how does X work"), pass `min_score=0` to be
safe. The per-project default `min_score=0.2` is lighter than it
used to be (`0.4`) and usually fine, but abstract semantic queries
can score in the 0.2–0.3 range that the default still rejects.

### Rule 10 — Words ≠ change location (the intent-vs-tokens watchword)

Workspace search ranks projects by *where the words live*. Your
task is usually about *where the change should happen*. These
coincide ~70% of the time, not 91%. When in doubt: read the
chunks in ranks 2–5 before committing to a target repo.

### Quick example — when rules 7 and 10 save you

> User: "Change the database timeout for the staging environment of
> the order service."

Workspace search ranks the **order-service code repo** at #1 (it's
where the word "database" appears most). But the change needs to
land in the **environment-platform manifests repo** at rank #4. If
you stopped at top-1 you'd edit the wrong file. Rules 7 and 10
remind you to scan further.

---

## Primary project nuance

You are typically `cd`'d into a single repo. That's the *primary
project*. The user's task is framed *from* that repo — they're
extending it, integrating with something it depends on, or wiring up
something that consumes it.

Patterns:

- **The change centers on primary, others are consumers/providers.**
  Most common. Primary gets the bulk of the implementation; the
  other repos get small adapter changes (new field consumption, new
  webhook subscriber, new client method).
- **The change is in another repo, primary just calls it.** Less
  common but real. Primary's role is the integration test or the
  feature-flag flip; the heavy lifting is elsewhere.
- **The change is genuinely distributed.** Migrations, schema changes
  rolling through many services, protocol bumps. Each repo gets a
  coordinated change with deploy-order constraints.

Workspace search tells you which pattern you're in. Don't assume.

---

## Sub-agent fan-out pattern

When you have 3+ relevant repos, fan out. Sub-agents run with isolated
context — the main session stays clean (no per-repo code chunks bloating
it) and the investigations run in parallel.

Use the dedicated **`cix-workspace-investigator`** sub-agent, which ships
with this skill. It's a thin, read-only shell around `cix search` / `cix
def` / `cix refs` / `Read` / `Grep` with three hard rules baked in:
stay inside the assigned project, no edits, no recursion. The
methodology — what to look for, what to report, in what format — is
**your** call, per spawn. The sub-agent follows your instructions; it
doesn't second-guess them.

### The four parts of a good per-spawn prompt

You'll write one prompt per repo. A good one has four parts:

#### 1. The user's task, verbatim

Sub-agents have zero prior context. Paste the original user request even
if it feels redundant — your interpretation might be wrong, and the
user's wording is the ground truth the sub-agent should reason from.

#### 2. The `project_path` you're assigning

Plus the workspace ID or `cix` command-prefix if your setup needs it.
One repo per spawn.

#### 3. Seed chunks **with your commentary**

This is the part most often done badly. Don't just paste raw chunk
pointers and hope the sub-agent figures out what matters. You saw the
workspace search response; you have hunches about which chunks are real
entry points and which are noise; pass that down.

For each chunk you cite, add one short line of interpretation. For
the response as a whole, flag suspicious signals:

- Which chunk looks like the most likely entry point and why
- Which chunks look like test fixtures / dead code / wrong-layer the
  sub-agent should de-prioritize
- Numeric signals that need a second opinion: `score=0` (BM25-only
  literal — verify the token isn't a false friend), `score < 0.4` (low
  confidence, possible rank-inversion), `bm25_score` high + `dense_score`
  near zero (literal-only match — concept may not actually live here)
- Whether you suspect this repo is wrong-layer (rule 7) — tell the
  sub-agent to confirm relevance before diving into the chunks

**Example "good chunk block":**

```
Seed chunks from workspace search:
- `internal/gateway/server.go:412-418` (score 0.55) — looks like the
  HTTP handler entry point for the rate-limit feature; confirm it
  invokes the limiter middleware rather than just returning 429.
- `internal/gateway/middleware.go:89-93` (score 0.49) — middleware
  registration site. Verify whether rate-limit is wired here or
  elsewhere.
- `tests/integration/rate_limit_test.go` (score 0.41) — integration
  test. Useful for understanding the expected shape, but not where
  the change lands. Skim only.
- `pkg/shared/util.go:1-30` (score 0) — BM25-only hit, "limit"
  appears in a comment. Almost certainly noise; skip unless you need
  shared utilities.

Panel-level notes:
- Workspace ranked this project #1 with a clear lead (project_score
  1.000 vs next 0.860). High confidence this is the right repo.
- bm25_score=8.5, dense_score=0.54 — strong on both signals, not a
  wrong-layer concern.
```

#### 4. Explicit deliverable

Tell the sub-agent **exactly** what to return and in what shape. Each
task has different needs:

- "Confirm whether this repo is in scope. Yes / no / partial + one
  sentence why."
- "Find the entry point for the rate-limit middleware. Report
  file:line of the entry and a five-step trace through the call
  graph."
- "List every file that would need to change to add a new audit-log
  event type. No code, just file path + one-line per-file reason."

Vague deliverables (`"investigate this repo"`) → vague answers.

### Anti-patterns to avoid

- **"Investigate this repo for rate-limit"** — no deliverable. The
  sub-agent guesses scope and you can't verify the result.
- **Three paragraphs of context with nested questions** — sub-agent
  answers the wrong question. Pick one deliverable per spawn.
- **"Read all the auth code"** — unbounded. Either fails or returns a
  wall of text.
- **Pasting raw chunks without interpretation** — you saw the
  response, you have hunches about what matters. Sub-agent doesn't.
  Skipping commentary throws away the most valuable thing you can pass
  down.

### Mechanics

Run all sub-agents in **one message with multiple Agent calls** so they
execute in parallel. Wait for completion. Synthesize their reports
yourself — sub-agents don't see each other's work; you do. Surface
inconsistencies (e.g. two repos disagree on which event format is
canonical) back to the user.

---

## Worked example — why this skill exists

A representative failure mode that motivated the hybrid algorithm:

**The naïve approach:** running workspace search with a full natural-
language sentence ("Add feature X to product Y"). The pre-hybrid
implementation was pure-dense — it returned the N nearest vectors
regardless of how far away "nearest" actually was. Every repo in the
workspace surfaced, including repos that contained **zero literal
mentions** of either the feature name or the product code. Confidently
reporting all of them as "relevant" wasted time on completely
unrelated repos.

**The structural failure:**

1. Pure-dense fan-out cannot tell "no signal" apart from "weak
   signal" — chromem always returns the K nearest vectors.
2. Long natural-language queries dilute the few tokens that carry
   the actual gating signal.
3. Without a sparse-retrieval channel, an acronym or unique
   identifier query has nothing to lock onto.

**What this skill teaches instead:**

1. Query with **just the high-precision term** first — the product
   acronym, the feature name, the unique symbol. Everything else
   is noise.
2. Verify that projects with `bm25_score = 0` aren't masquerading
   as relevant. After the hybrid landed, repos with no literal
   matches AND only marginal dense similarity drop out automatically
   via the project gate.
3. Confirm with the user before treating "this repo surfaced in
   search" as "this repo is in scope for the change".

**The lesson encoded in this skill:**

- Step 1: query the term, not the sentence.
- Step 1: trust the project gate; if a repo dropped out, it dropped
  out for a reason.
- Step 2: read the surface area from `projects[]` first, then read
  the chunks as starting points.
- Step 3: never assume "in search results" == "in scope". Verify.

---

## Troubleshooting

### `bm25_score` is 0.000 on every project

The workspace was indexed before the FTS5 mirror existed and the
sparse half of the hybrid is empty. Hybrid degrades to pure-dense
fan-out — the same algorithm that produces the false-positive
failure mode described in the worked example above.

The response includes `stale_fts_repos` listing the affected
project_paths. Fix: reindex each project (dashboard → project card →
reindex button, or `POST /api/v1/projects/{hash}/reindex`).
After reindex, BM25 populates incrementally per-file as chunks are
written.

Until reindex completes, **don't trust the project gating** — the
algorithm is producing the old failure mode. Verify project relevance
by literal grep on the term.

### `status: "empty"` despite obviously-relevant repos in the workspace

Either:

- The query terms don't appear literally in any repo AND the dense
  similarity is below threshold for everything (project-gate dropped
  everyone). Re-phrase with the term the code actually uses, or
  lower `min_score`.
- Every workspace repo is still indexing. Check `pending_repos` in
  the response.

### `status: "partial_failure"`

At least one repo errored out (`failed_repos` array names them).
Common cause: corrupt chromem collection. The remaining repos still
returned results. Surface to the user; don't silently treat as
complete.

### Top-2 projects are at near-equal candidacy

The algorithm isn't confident which repo is more relevant. Possible
causes:

- The feature genuinely lives in both. Ask the user which they
  intended as primary scope.
- The query is too broad — both repos match generic vocabulary.
  Re-query with a more specific term.
- One repo is a fork or duplicate. Confirm with `cix ws <name>`
  describe.

### One project absolutely dominates everything else

Could be legit (the user's task is mostly contained in one repo and
that repo is just very dense with relevant content). Or could be a
single repo accidentally matching the user's stopwords across many
files. Spot-check: is the project's `bm25_score` driven by the
high-IDF term (the product name) or by common words?

### Top-1 is wrong-layer (rule 7 / rule 10 in action)

The top-1 project contains the words but isn't where the change
should land. Classic example: "deploy X to staging" → workspace
ranks the code repo for X at #1, but the staging overlay lives in
a manifests repo at rank #4. Or: "add API endpoint Y" → ranks the
backend implementation at #1, but the OpenAPI contract repo at #3
must be updated first.

**Fix:** scan ranks 2–5 explicitly. Look for projects whose names
hint at a different layer (`*-platform`, `*-manifests`,
`*-contracts`, `*-config`, `*-infra`, `openapi*`). If you see one,
that's probably your real target.

### Disambiguator backfired — the query lost its grip

You added a 3rd word to discriminate between two overloaded terms,
and the response is *worse* — top projects all have mediocre scores
and the right repo isn't among them anymore. This usually happens
when the added token belongs to a different stack than your target
(e.g. you guessed a transport / framework / library that the canonical
repo doesn't use), so the extra token rotates the ranking toward
unrelated repos.

**Fix:** strip the guessed-stack token. Try a meta-token instead
(`endpoint`, `route`, `handler`, `manifest`, `migration`). Or: run
the 2-word query as-is, scan the top-1 project's path patterns and
language to see what stack it actually uses, then refine.

---

## Quick command reference

```bash
# List workspaces
cix ws
cix ws list --json

# Describe one workspace (always do this before searching)
cix ws platform
cix ws platform describe --json

# List repos attached to a workspace
cix ws platform list
cix ws platform repos --verbose

# Search a workspace
cix ws platform search "rate-limit middleware"
cix ws platform search "JWT validation" --top-projects 8 --top-chunks 30
cix ws platform search "audit logging" --json
```

Flags:

- `--top-projects N` — surface up to N projects in the panel
  (default 10, max 50). Increase for very broad explorations.
- `--top-chunks K` — return up to K chunks total (default 20, max
  200). Round-robin interleaved across surviving projects.
- `--min-score F` — drop dense hits below cosine F before scoring.
  **Default 0.4** (symmetric with per-project search default).
  Pass `0` explicitly for intentional cross-project sweeps that
  need long-tail recall — broad concepts like "authentication" or
  "Kafka consumers" that legitimately live in many repos. Higher
  values (0.5+) for queries you want laser-focused.
- `--json` — raw machine-readable response.

---

## TL;DR

When the user's task plausibly spans more than one repo:

1. `cix ws` → find the workspace, then `cix ws <name>` describe it.
2. Workspace search with a **short, term-rich** query.
3. Read `projects[]` → that's your scope (Q1 answered).
4. For each repo in scope, either single-project search or spawn a
   `cix-workspace-investigator` sub-agent — in parallel, with seed
   chunks AND your interpretive commentary on what to trust.
5. Synthesize the sub-agent reports → plan changes per repo, with
   order constraints (Q2 + Q3 answered).
6. Ask the user to confirm the scope and plan before implementing.

If `bm25_score` is 0 across the board, the FTS index is stale —
fix it before trusting the result.
