---
name: cix-workspace
description: Cross-project research workflow for cix workspaces in Claude Cowork. Use when a request spans multiple repositories — "wire feature X through the platform", "add Y across the services", "change Z in production/staging", "how does this work end-to-end across repos". Uses cix_workspace_search to find which repos are in scope, then cix_search to drill into one repo at a time. Talks to a cix server that may host many workspaces and repos, so name the workspace and project explicitly.
metadata:
  version: "0.1.0"
  surface: cowork
---

# `cix workspace` — Cross-Project Research Workflow

Some tasks are not contained in one repo. A request like "wire feature X through
the platform" can touch a half-dozen repos in different languages and layers — a
service, a shared library, the infra manifests, an API spec. Reading one repo
gives you 1/N of the picture, and you don't know which N repos are involved until
you look.

`cix_workspace_search` is the tool for that. It searches every repo in a named
**workspace** at once and tells you:

1. **Which repos are actually relevant to this request.**
2. **Which code in those repos is the entry point.**
3. **What changes need to land in each, and in what order.**

Those three questions are the *goal*. Don't jump to implementation before you can
answer all three with evidence.

There is no "current repo" here (Cowork has no opened working directory). A
workspace groups several indexed repos on a server; infer the task's **anchor
repo** from the request, and let the workspace supply the surrounding repos.

## First: which server hosts the workspace?

A cix connection may reach more than one **server** (a local box, a remote
corporate backend, …). Each server hosts its own workspaces and projects — a
workspace named `platform` on one server is unrelated to anything on another.

`cix_list_servers` lists the configured servers. **A workspace and all its repos
live on exactly one server.** Decide which server you're on, then be consistent:
pass the *same* `server` argument to every call in the flow
(`cix_list_workspaces`, `cix_workspace_search`, and the per-project `cix_search`
drill-downs). Mixing servers mid-workflow silently returns empty or wrong-repo
results, because the project doesn't exist on the other server.

**Rule:** use the default server (omit `server`) unless the user names one, or the
target workspace isn't on the default. Never guess a name — call
`cix_list_servers`. Once you know it, thread it through the whole workflow.

## When to reach for workspace search

| Signal in the user's request | What to do |
|---|---|
| Names a product / acronym you don't recognize | Workspace search the acronym — see where it lives |
| "Add X to the Y flow", "wire Z into A" | Workspace search Y or Z — likely cross-cutting |
| "Across services", "between repos", "end-to-end" | Workspace search the feature |
| Talks about an event / topic / contract / API endpoint | Workspace search the event name |
| References infra / deployment alongside code | Workspace search — the infra repo is probably in the workspace too |
| "How do I change X in production / staging" | Workspace search BUT look past top-1 — the answer is often a manifests/config/contract repo even when a code repo ranks higher (trust rule 7) |
| Plain change entirely inside one repo | **Don't** workspace search. Use the `cix` single-repo skill |
| User points at a specific symbol / file path | **Don't.** `cix_definitions` on the name, or Read the path |

If unsure, call `cix_list_workspaces` once to see whether a workspace covering
the task even exists. If not, this skill doesn't apply.

## The workflow

A goal-driven loop. Each step is fast — don't shortcut it.

### Step 0 — orient
- `cix_list_servers` — which servers exist? which is default?
- `cix_list_workspaces` — workspaces on the chosen server (id + name).
- `cix_list_workspace_projects(workspace)` — confirm the repos are indexed.

Lock in the server here, before searching. If a later response shows
`stale_fts_repos`, trust the dense ranking less (see references/troubleshooting.md).

### Step 1 — answer "which repos?"

Run `cix_workspace_search` with a **short, term-rich query**, not the full user
sentence:

- GOOD — short, term-rich: `cix_workspace_search(workspace, "rate-limit middleware")`
- BAD — a full sentence dilutes the literal-match signal with stopwords:
  `"Add a rate limit to every API endpoint"`

Why short: the hybrid algorithm fuses BM25 (literal token match) with dense
(semantic). BM25 carries the project-gating signal — repos that share zero
vocabulary with the query drop out. Common words ("add", "to", "a") match
everywhere and dilute that signal.

Read the response:
- **`projects[]` is the answer to Q1**, sorted by `project_score`. Each entry has
  `bm25_score` (literal overlap) and `dense_score` (semantic). Projects below the
  per-query threshold are already filtered out — you only see survivors.
- The top entry's `project_score` is your reference. 60–100% of top = core
  relevant; 40–60% = secondary.
- Include the **anchor repo** (the one the user's task is rooted in) even if it
  ranks low — infer it from the request. The other repos are its dependencies /
  consumers / providers.

Optional args: `top_projects` (default 5), `top_chunks` (default 10),
`min_score` (default 0.4 — pass `0` for intentional cross-cutting sweeps).

### Step 2 — answer "what code is relevant?" (sequential drill-down)

Workspace search is a **scoping** tool: it tells you which repos are in play and
shows a *capped teaser* of chunks (round-robin, ~few per repo). It is NOT where
you read the code.

**Drill into the relevant repos one at a time, in priority order** — do NOT fan
out, do NOT spawn sub-agents. For each repo from `projects[]`, starting with the
highest-ranked relevant one:

1. Take its `project_path` from the `projects[]` panel — that is the `host_path`.
2. Call `cix_search(project=<that host_path>, query="<focused query>", server=<same server>)`
   to get file-grouped, deeper results.
3. Read the results, decide whether this repo is actually in scope, then move to
   the next repo only if the task needs it.

For a natural-language drill-down query ("how does X work"), pass `min_score: 0`
— abstract queries can score in the 0.2–0.3 range the default rejects.

Investigate the strongest-signal repo fully before opening the next. Most tasks
resolve after drilling into the top 1–2 repos; you rarely need all of them.

### Step 3 — answer "what changes?"

This is the deliverable. Your per-repo reads feed the plan; you write it. For
each relevant repo:
- What needs to change (specific file:line, or a new file).
- Why (which step of the data flow this implements).
- Order constraints (e.g. "shared-models migration must deploy before the backend
  reads the new field").
- Tests that prove it works.

Confirm with the user before any of this lands. The plan is the deliverable of
this skill; implementation is a separate step.

### Throughout — ask, don't guess
Trigger a clarifying question when:
- Top-2 projects are at near-equal `project_score` with different labels — ask
  which repo the user means.
- `bm25_score` is 0 across all projects → either the FTS index is stale (see
  troubleshooting) or the term doesn't appear literally anywhere. Ask for the term
  the code actually uses ("we call it `Order` in code, not `Trade`").
- Your drill-down into a repo finds no clear entry point — surface that
  uncertainty, don't paper over it.

Don't ask if the answer is obvious from the chunks. The bar is "I have two
plausible interpretations and the wrong one costs real time."

## Reading the projects panel

```
project-a@main   0.500   5 hits   bm25 0.421   dense 0.556
project-b@main   0.412   5 hits   bm25 0.318   dense 0.498
project-c@main   0.288   3 hits   bm25 0.155   dense 0.362
```

- `project_score`: the blended candidacy in [0,1]. Top = strongest signal.
- `bm25_score` >> `dense_score`: relevant by literal token overlap (the term
  appears in code). Trust the surface area, verify semantic relevance.
- `dense_score` >> `bm25_score`: relevant by semantic similarity but the literal
  term isn't there (common when the user's word is a nickname not used in code).
- Both near zero: you're seeing it because nothing else cleared the gate either.
  Treat with skepticism.

## Trust rules (summary — full version in references)

Empirically calibrated. Apply before acting on a panel:

1. **`chunk.score >= 0.4`** is the trust threshold; below that is ~75% noise.
2. **`chunk.score == 0`** is a BM25-only (literal) hit — valuable for unique
   identifiers, noise for generic English words.
3. **Top-1 of `projects[]` is right ~70%** of the time on real tasks — **scan
   ranks 2–5 before reformulating.**
4. **"Change X in production"** usually lives in a manifests/config/contract repo,
   not the code repo that ranks #1 (look for `*-platform`, `*-manifests`,
   `*-config`, `openapi*` at ranks 3–5).
5. **Words ≠ change location.** Search ranks where the *words* live; your task is
   about where the *change* happens. They coincide ~70%, not always.

Read **references/trust-rules.md** before acting on a borderline panel — it has
all ten rules with examples, plus when to add a disambiguating token and when to
sweep with `min_score: 0`.

## Worked example — why this skill exists

The failure mode this guards against: running workspace search with a full
natural-language sentence. A pure-dense search returns the N nearest vectors
regardless of how far "nearest" is, so every repo surfaces — including repos with
zero literal mention of the feature. The hybrid + short-query approach fixes it:

1. Query with the **high-precision term** first (the acronym, the feature name,
   the unique symbol) — everything else is noise.
2. Trust the project gate: if a repo dropped out, it dropped out for a reason.
3. Never treat "this repo surfaced in search" as "this repo is in scope" —
   verify by drilling in (Step 2) and confirm with the user.

## Quick tool reference

- `cix_list_servers` — list servers; omit `server` for the default.
- `cix_list_workspaces` — workspaces on a server.
- `cix_list_workspace_projects(workspace)` — repos in a workspace, with status.
- `cix_workspace_search(workspace, query, [top_projects], [top_chunks], [min_score], [server])`
  — cross-repo search. Use a short, term-rich query.
- `cix_search(project, query, [min_score], [server], …)` — per-repo drill-down;
  `project` is a `host_path` from the `projects[]` panel.

## TL;DR

When the task plausibly spans more than one repo:

0. `cix_list_servers` → if more than one, pick the one hosting the workspace and
   thread the same `server` through every call.
1. `cix_list_workspaces` → find it, then `cix_list_workspace_projects` to confirm
   repos are indexed.
2. `cix_workspace_search` with a **short, term-rich** query.
3. Read `projects[]` → that's your scope (Q1).
4. For each repo in scope, drill in **sequentially** with `cix_search`
   (`project` = the host_path), one repo at a time. No fan-out, no sub-agents.
5. Synthesize the reads → a per-repo change plan with order constraints (Q2 + Q3).
6. Confirm scope and plan with the user before implementing.

If `bm25_score` is 0 across the board, the FTS index is stale — see
references/troubleshooting.md before trusting the result.
