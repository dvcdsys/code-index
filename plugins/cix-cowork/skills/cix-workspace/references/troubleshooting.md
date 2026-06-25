# Workspace search — troubleshooting

Diagnostic lookups for unexpected `cix_workspace_search` responses.

## `bm25_score` is 0.000 on every project

The workspace was indexed before the full-text (BM25) mirror existed, so the
sparse half of the hybrid is empty. Hybrid degrades to pure-dense fan-out — the
failure mode where "no signal" can't be told apart from "weak signal", and every
repo surfaces.

The response includes `stale_fts_repos` listing the affected project_paths.
**Fix:** reindex each affected project — via the cix dashboard (project card →
reindex), or `cix init` / a reindex on the machine running the cix server. After
reindex, BM25 populates incrementally per file.

Until reindex completes, **don't trust the project gating** — verify project
relevance by drilling into each candidate with `cix_search` and reading the
actual code, rather than relying on the panel ranking.

## `status: "empty"` despite obviously-relevant repos

Either:
- The query terms don't appear literally in any repo AND dense similarity is
  below threshold for everything (the project gate dropped everyone). Re-phrase
  with the term the code actually uses, or lower `min_score` (try `0`).
- Every workspace repo is still indexing. Check `pending_repos` in the response.

## `status: "partial_failure"`

At least one repo errored out (`failed_repos` names them). Common cause: a corrupt
collection on the server. The remaining repos still returned results. Surface this
to the user; don't silently treat the result as complete.

## Top-2 projects are at near-equal candidacy

The algorithm isn't confident which repo is more relevant. Possible causes:
- The feature genuinely lives in both. Ask the user which they intended as primary
  scope.
- The query is too broad — both repos match generic vocabulary. Re-query with a
  more specific term.
- One repo is a fork or duplicate. Confirm with `cix_list_workspace_projects`.

## One project absolutely dominates everything else

Could be legit (the task is mostly contained in one repo that's dense with
relevant content). Or a single repo accidentally matching the user's stopwords
across many files. Spot-check: is the project's `bm25_score` driven by the
high-signal term (the product name) or by common words?

## Top-1 is wrong-layer (trust rules 7 / 10 in action)

The top-1 project contains the words but isn't where the change should land.
Classic example: "deploy X to staging" ranks the code repo for X at #1, but the
staging overlay lives in a manifests repo at rank #4. Or: "add API endpoint Y"
ranks the backend at #1, but the OpenAPI contract repo at #3 must be updated
first.

**Fix:** scan ranks 2–5 explicitly. Look for projects whose names hint at a
different layer (`*-platform`, `*-manifests`, `*-contracts`, `*-config`,
`*-infra`, `openapi*`). If you see one, that's probably the real target.

## Disambiguator backfired — the query lost its grip

You added a 3rd word to discriminate between two overloaded terms, and the
response is *worse* — top projects all have mediocre scores and the right repo
isn't among them. This usually happens when the added token belongs to a different
stack than your target (you guessed a transport / framework / library the
canonical repo doesn't use), so the extra token rotates the ranking toward
unrelated repos.

**Fix:** strip the guessed-stack token. Try a meta-token instead (`endpoint`,
`route`, `handler`, `manifest`, `migration`). Or run the 2-word query as-is, scan
the top-1 project's path patterns and language to see what stack it actually uses,
then refine.
