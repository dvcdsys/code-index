---
name: cix-workspace
description: Cross-project semantic search via cix workspaces. Use when a question spans multiple repositories — microservices that talk to each other, frontend + backend living apart, or any time the answer isn't in a single repo. Two-stage retrieval (community routing → chunk ranking) keeps the context budget tight.
user-invocable: true
---

# Code Index Workspaces (`cix workspace`) — Cross-Project Semantic Search

You have access to `cix workspace`, a layer on top of `cix` that searches
*across* a group of related repositories with one query. Reach for it
when the codebase isn't a single repo — microservice clusters, frontend
+ backend split, monolithic systems carved into N repos, or anything
where a feature traces through multiple checkouts.

## The mental model

A **workspace** is a named group of GitHub repositories the cix-server
keeps indexed together. Behind the scenes the server runs Louvain
community detection on the combined call graph; each community gets a
single centroid embedding in a shared table. Two-stage search:

1. **Stage 1 (community routing).** Your query embedding hits the
   centroid table → top-N functionally-related communities. Communities
   from different repos compete on equal footing, so cross-project
   answers surface naturally.
2. **Stage 2 (chunk ranking).** Within those communities, chunks from
   each member repo are ranked against the query and merged globally.

This means you get a **focused, cross-project answer** without flooding
your context window with chunks from every repo.

## When to use `cix workspace` vs `cix`

**Use `cix workspace search` when:**
- The question spans multiple repos ("how does auth flow from the
  frontend to the user service?", "which services consume the orders
  topic?")
- You want cross-project recall but don't want to grep N repos
- You're a few clicks deep in unfamiliar microservice territory and
  need a guided entry point

**Use plain `cix search` when:**
- The question lives in one repo and you know which one — workspace
  search adds latency you don't need
- The repo isn't part of any workspace
- You're inside a single repo's `cd` already and just want to find a
  function

If `cix workspace search` returns `communities_not_built`, the
workspace exists but its centroid index hasn't finished computing
yet. Either wait ~30s after the last repo finished indexing
(debounced rebuild) or fall back to per-repo `cix search`.

---

## Commands

The grammar is **name-first**: every workspace-scoped verb takes the
workspace identifier (id OR case-insensitive name) as the *first*
positional argument. The verb follows. This reads the way operators
think about workspaces ("for THIS workspace, do THAT").

### Discover what's available
```bash
cix ws                       # default: list workspaces
cix ws list                  # alternate form
cix ws list --verbose        # include repo counts per workspace
cix ws list --json           # machine-readable
```
Prints `<id>  <name>  — <description>` per line. The id is the
opaque ULID you'd use in scripts; the name works for ad-hoc shell use.

### Describe one workspace (start here when exploring)
```bash
cix ws platform              # describe — shows attached repos + status
cix ws platform describe     # explicit verb
cix ws platform --json       # JSON for piping
```
Output bundles the workspace metadata with every attached repo,
each with `✓` / `✗` / `…` status, branch, project_path, last
indexed timestamp, and last error if any. **Use this before
searching** — it tells you whether the workspace's indexes are
actually built (`✓` count vs total).

### List attached repos only
```bash
cix ws platform list         # list of indexed projects inside workspace
cix ws platform repos        # alias for `list`
cix ws platform list --verbose   # adds project_path, last_indexed_at, last_error
cix ws platform list --json
```

### Search a workspace
```bash
cix ws platform search "JWT validation"
cix ws platform search "rate limiting" --top-communities 8 --top-chunks 30
cix ws platform search "config loading" --json
```

**Flags (apply to any verb):**
- `--top-communities N` — search only: fan out to N centroids
  (default 5, max 50). Increase for very broad questions; decrease
  for tight queries to reduce stage-2 fanout.
- `--top-chunks K` — search only: return at most K chunks (default
  20, max 200).
- `--json` — emit raw JSON for any verb.
- `--verbose` / `-v` — list / describe only: extra columns.

---

## Output anatomy

```
Top communities:
  [0.832] auth, login, ValidateToken, IssueToken, RefreshToken  — 24 members across github.com/acme/api@main, github.com/acme/web@main
  [0.671] middleware, handler, RequestLogger  — 18 members across github.com/acme/api@main

Top chunks:
  [0.812] internal/auth/middleware.go:42-67
         project: github.com/acme/api@main
         symbol:  ValidateToken
         community: auth, login, ValidateToken, IssueToken, RefreshToken
  ...
```

- The **community line** is your "this is where the answer lives" map.
  Scan the labels to confirm you're in the right neighborhood before
  reading individual chunks.
- The **chunk line** is the actual answer. `project:` tells you which
  repo the chunk came from — useful when you need to `Read` the file at
  the listed path inside the right local checkout.

---

## Patterns

### Discovery-first workflow (the right reflex)

When you don't already know which workspace to search, **start wide,
narrow inward**:

```bash
# 1. What's available?
cix ws

# 2. What's in this one?
cix ws platform

# 3. Now I know the repos and that they're indexed — go.
cix ws platform search "user login flow"
```

The `cix ws <name>` describe step is cheap (one API call, ~50 ms)
and answers the two questions you'd otherwise hit `communities_not_built`
or "wrong workspace" on:
- Are repos actually indexed yet? (look for `✓`)
- Is the feature I'm hunting plausibly in this workspace's repos?

### Tracing a cross-repo feature
```bash
# "How does login work end-to-end across our microservices?"
cix ws search platform "user login authentication flow"

# Read the top chunks across both api and web — same query, no flag-tweaking,
# no juggling per-repo searches.
```

### Finding consumers of a shared event / topic
```bash
cix ws search ingest "OrderCreated event consumer"
# Returns chunks from every service that subscribes — even when each
# service spells the handler slightly differently.
```

### Drilling down with single-repo cix after a workspace hit
```bash
cix ws search platform "rate limiting"
# Notes that the top community lives in github.com/acme/api@main.
# Switch to that checkout locally and use the normal cix verbs:
cd ~/src/api
cix def TokenBucket
cix refs RateLimiter --limit 50
```

### Tight context budget (good agent reflex)
```bash
# Get a small first pass, look at community labels, only THEN fetch
# more context for the relevant ones.
cix ws search platform "config loading" --top-communities 3 --top-chunks 5

# If the top community is what you want, expand:
cix ws search platform "config loading" --top-communities 3 --top-chunks 20
```

The two-stage architecture means scaling `--top-chunks` from 5 to 20
re-uses the same stage-1 result — cheap.

---

## What this can't (yet) do

- **Cross-repo call edges.** PR4's call-graph extraction is intra-repo.
  Communities form by structural cohesion *within* each repo, then
  compete on shared centroid space — which is enough to surface
  cross-repo answers via embedding similarity. Explicit "service A
  calls service B" links are not modeled.
- **Recompute on demand.** The compute_workspace_communities job is
  debounced 30s after the last index_repo. If you've JUST added a
  repo, give it a beat before searching.
- **Manual graph viz.** No built-in cytoscape rendering — communities
  live in the SQL tables (`communities`, `community_members`) and the
  Chroma collection (`ws_{md5}_centroids`). The dashboard's workspace
  detail page renders the basics; deeper inspection is SQL today.

---

## Quick troubleshooting

- **`communities_not_built`**: workspace exists, no centroid index yet.
  Wait ~30s after the last repo finishes indexing, then retry.
- **Empty `chunks` array**: communities exist but no chunks scored
  above the implicit threshold. Lower-quality match — broaden the
  query or fall back to per-repo `cix search`.
- **`workspaces feature is disabled`** (503): operator hasn't set
  `CIX_WORKSPACES_ENABLED=true` on the server. Per-repo `cix search`
  still works.
- **`workspace … not found`**: ran `cix workspace list` and the name's
  right? It's case-insensitive on name but the id is opaque.

---

## Defaults that matter

| Knob | Default | When to change |
|---|---|---|
| `--top-communities` | 5 | Up to 8–10 for very broad exploration; 2–3 for tight known-answer queries |
| `--top-chunks` | 20 | Bigger when you need wide context (refactor scoping); smaller (5–10) when you're rough-locating |
| Server-side debounce | 30s | Operator-set — informational only |

If a query doesn't return what you need with defaults, **prefer
re-phrasing the query over raising the limits**. The two-stage
architecture is more sensitive to query quality than to fanout size.
