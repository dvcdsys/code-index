---
name: cix-workspace
description: Cross-project research via cix workspace search. Use when a task touches more than the project you're cd'd into — microservices that talk to each other, a feature whose implementation lives in N repos (backend + contracts + smart-contracts + webhooks + infra + …), or any time the user mentions a product name / service / event that isn't fully defined in the primary repo. The skill structures the research around three questions and a sub-agent fan-out so the answer doesn't drown in chunks.
user-invocable: true
---

# `cix workspace` — Cross-Project Research Workflow

You usually work inside one repo — your **primary project** — the
directory the user opened you in. Most tasks are fully contained there
and `cix search` / `cix definitions` / `cix references` are the right
tools.

But some tasks are not contained. A "sell flow" feature in a payments
product touches the API backend, the smart contracts, the webhook
notifier, the deployment manifests, the marketplace contract. Reading
the primary repo alone gives you 1/N of the picture. Worse, you don't
know which N repos are actually involved until you look.

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
# GOOD — the product name + the action verb
cix ws platform search "XYZ sell"

# BAD — full sentence dilutes BM25 with stopwords ("add", "to", "a")
cix ws platform search "Add a sell flow to XYZ"
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
  --data-urlencode "q=sell offer accept handler" \
  --data-urlencode "min_score=0" \
  "$CIX_URL/api/v1/projects/$(project_hash)/search"
```

**B. Fan-out to sub-agents (≥ 3 repos, or you need a thorough read):**
spawn one Explore sub-agent per relevant repo, in parallel.

Each sub-agent gets:

- The user's task description (the full sentence — sub-agents have
  fresh context).
- The project_path it's investigating.
- The top chunks from workspace search for that project, as seed
  pointers (so the sub-agent doesn't restart from zero).
- An explicit instruction: "Locate the entry points relevant to
  *<task>*, summarize the data flow, and identify what would need to
  change. Don't propose code yet. Report file:line for everything."

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
acme-backend@main         0.500   5 hits   bm25 0.421   dense 0.556
acme-shared@main        0.412   5 hits   bm25 0.318   dense 0.498
acme-models@main 0.288   3 hits   bm25 0.155   dense 0.362
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

When you have 3+ relevant repos, parallel sub-agents beat sequential
self-investigation. Template:

```
For each project P in surviving_projects (except primary):
    spawn Agent(
        subagent_type="Explore",
        description="<P short label>: locate <task> entry points",
        prompt=f"""
        You're investigating one repo in a workspace fan-out for the
        task: "<user task, verbatim>".

        Repo to investigate: {P.project_path}
        Seed chunks (from workspace search):
        {top_chunks_for_P}

        Your job:
        1. Confirm the seed chunks are actually the right entry point.
           If they're not, find the real one and report it.
        2. Trace the data flow inside this repo that's relevant to
           the task. Brief — names and file:line, not whole files.
        3. List what would need to change here to implement the task.
           Don't write code. Report what changes and why.

        Report under 300 words. No filler.
        """
    )
```

Run them all in **one message with multiple Agent calls** so they
execute in parallel. Collect responses, then synthesize.

Synthesis = your job. The sub-agents don't see each other's findings;
you do. Surface inconsistencies (e.g. two repos disagree on which
event format is canonical) back to the user.

---

## Worked example — the XYZ retro

This is how this skill was developed. The user asked: *"Add sell flow
to XYZ"* (XYZ is the internal name of a product in their workspace).

**What went wrong with naïve approach:**

I ran the pre-hybrid workspace search with the full sentence: `"Add
sell flow to XYZ"`. It returned 8 projects ranked by mean dense
similarity:

```
acme-backend          0.393
acme-platform 0.279
acme-shared         0.270
acme-models  0.258
acme-worker     0.247
acme-notifier 0.189
acme-directory        0.170
acme-inventory        0.164
```

All 8 repos surfaced. I confidently reported all 8 as relevant. **The
user flagged that acme-worker, acme-directory, acme-inventory had
zero XYZ mentions whatsoever.** A literal grep confirmed: 0 lines
mentioning XYZ in those 3 repos. The dense embedding had been
returning the N nearest vectors regardless of how far away "nearest"
actually was — those repos surfaced on noise-level cosine similarity
(0.16-0.25).

**The structural failure:**

1. Pure-dense fan-out cannot tell "no signal" apart from "weak
   signal" — chromem always returns the K nearest vectors.
2. Long natural-language queries dilute the few tokens (`XYZ`,
   `sell`) that carry the actual gating signal.
3. Without a sparse-retrieval channel, an acronym query has nothing
   to lock onto.

**What I should have done from the start:**

1. Query with **just `XYZ`** first to identify the surface area. The
   product code is the high-precision term; everything else is
   noise.
2. Verify projects with `bm25_score = 0` aren't masquerading as
   relevant. (After the hybrid landed, those 3 dead-weight repos
   drop out automatically via the project-gate.)
3. Confirm with the user before treating "this repo surfaced in
   search" as "this repo is in scope for the change".

**Result after fixing the algorithm:**

Workspace search with `XYZ` now keeps acme-backend (780 mentions),
acme-shared (119 mentions in 8 files), acme-platform (98
mentions in 6 files), acme-notifier (18 mentions in 1 file),
acme-models (1 mention + 2 sell-related files). Drops the
three zero-mention repos. The 5 survivors are the actual scope —
each plays a real role (backend / API contracts / k8s configs /
event notifications / shared data models).

**The lesson encoded in this skill:**

- Step 1: query the term, not the sentence.
- Step 1: trust the project-gate; if a repo dropped out, it dropped
  out for a reason.
- Step 2: read the surface area from `projects[]` first, then read
  the chunks as starting points.
- Step 3: never assume "in search results" == "in scope". Verify.

---

## Troubleshooting

### `bm25_score` is 0.000 on every project

The workspace was indexed before the FTS5 mirror existed and the
sparse half of the hybrid is empty. Hybrid degrades to pure-dense
fan-out — the same algorithm that produced the XYZ false-positive
above.

The response includes `stale_fts_repos` listing the affected
project_paths. Fix: reindex each repo (dashboard → repo card →
reindex button, or `POST /api/v1/workspaces/{id}/repos/{repo_id}/reindex`).
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
cix ws platform search "XYZ sell"
cix ws platform search "JWT validation" --top-projects 8 --top-chunks 30
cix ws platform search "rate limiting" --json
```

Flags:

- `--top-projects N` — surface up to N projects in the panel
  (default 10, max 50). Increase for very broad explorations.
- `--top-chunks K` — return up to K chunks total (default 20, max
  200). Round-robin interleaved across surviving projects.
- `--min-score F` — drop dense hits below cosine F before scoring.
  Default 0. Useful when natural-language queries hit too much
  noise; leave at 0 for short acronyms (cosine for short tokens
  is naturally small).
- `--json` — raw machine-readable response.

---

## TL;DR

When the user's task plausibly spans more than one repo:

1. `cix ws` → find the workspace, then `cix ws <name>` describe it.
2. Workspace search with a **short, term-rich** query.
3. Read `projects[]` → that's your scope (Q1 answered).
4. For each repo in scope, either single-project search or spawn an
   Explore sub-agent — in parallel.
5. Synthesize the sub-agent reports → plan changes per repo, with
   order constraints (Q2 + Q3 answered).
6. Ask the user to confirm the scope and plan before implementing.

If `bm25_score` is 0 across the board, the FTS index is stale —
fix it before trusting the result.
