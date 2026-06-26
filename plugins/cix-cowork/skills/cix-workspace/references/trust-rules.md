# Workspace search — trust rules

These ten rules were derived from a calibration eval (113 synthetic queries +
5 real engineering tasks against a mixed-domain workspace). Apply them before
acting on `cix_workspace_search` output. The numbers are empirical, not vibes.

## Rule 1 — `chunk.score >= 0.4` is the trust threshold

Chunks with `score < 0.4` are noise about 75% of the time (rank-inversion and
weak-signal false positives from the relative project gate). Skim them only when
the higher-scored chunks don't answer the question. With the default
`min_score: 0.4` you usually won't see them at all; if you passed `min_score: 0`
(intentional broad sweep), apply this rule yourself.

## Rule 2 — `chunk.score == 0` is a BM25-only hit, not low confidence

The chunk's project matched the literal query tokens via full-text search but the
embedding side didn't surface it. These are valuable when the query carries
project-specific identifiers (CamelCase symbols, file names, acronyms). Discount
them when the query is a generic English word (`error`, `data`, `config`) —
common-word literal hits are noise.

## Rule 3 — Top-1 of `projects[]` is correct ~70% of the time in real tasks

The synthetic eval measured 91% on single-target queries; real engineering tasks
hit ~70% because real queries often span layers (see rule 7). When the top-1
project doesn't match your task's intent, **scan ranks 2–5 before reformulating**
— the right repo is usually there. The `projects[]` panel answers "where do the
words live", not "where should the change happen".

## Rule 4 — Drop to per-project search for depth

When `projects[]` shows the target at rank 1 with a clear lead (`project_score`
≥ 1.5× the next), switch to per-project search:
`cix_search(project=<host_path>, query="<query>")` (pass the same `server` when
the workspace is on a non-default server). You get file-grouped, deeper results
without the cross-project round-robin cap. `project` takes the exact
`project_path` from `projects[]` (e.g. `github.com/owner/repo@branch`).

## Rule 5 — `min_score: 0` for intentional cross-project sweeps

Default workspace `min_score` is 0.4. For queries that should legitimately span
many repos ("authentication", "configuration loading", "Kafka consumers"), pass
`min_score: 0` explicitly. Expect `projects[]` to list 5–8 entries — that's the
feature, not a bug. Ignore rule 1 in this mode: many real positives sit below 0.4
in genuine cross-cutting queries.

## Rule 6 — Add a 3rd disambiguating token, carefully

If two query words are each domain-overloaded (e.g. "client SDK" could be the
generated API client, the shared library, or a model type), add a third word.
**Prefer meta-tokens** (`endpoint`, `route`, `handler`, `manifest`, `migration`,
`config file`) over tech-stack guesses (`grpc`, `kafka`, `terraform`) — wrong
stack guesses actively rotate the ranking away from the right answer. If unsure of
the stack, run the query without a disambiguator first, read the top-1 project's
language/path patterns, then refine.

## Rule 7 — "Change X in production" → manifests repo, not code repo

For tasks framed as deploying / configuring / overriding a feature, the answer
usually lives in a manifests / config / contract repo (K8s overlays, Helm charts,
OpenAPI specs, environment-specific yaml). Workspace search ranks by token
frequency, so the code repo typically wins. Look at `projects[]` for repos with
**manifests, config, platform, deploy, contract, openapi, infra** in their names
— those are often the right targets even at rank 3–5.

## Rule 8 — When top-1 doesn't fit, scan first, reformulate second

If you think top-1 is wrong:
1. First, scan ranks 2–5. The right project is there ~80% of the time when a
   layer mismatch caused rule 3 to fail.
2. Only after scanning, reformulate. Reformulating before scanning wastes a
   round-trip and risks the new query introducing fresh layer confusion.

## Rule 9 — For per-project NL drill-down, pass `min_score: 0` explicitly

When dropping from workspace to per-project `cix_search` with a natural-language
query (e.g. "how does X work"), pass `min_score: 0` to be safe. The per-project
default (0.2) is light and usually fine, but abstract semantic queries can score
in the 0.2–0.3 range that the default still rejects.

## Rule 10 — Words ≠ change location (the intent-vs-tokens watchword)

Workspace search ranks projects by *where the words live*. Your task is usually
about *where the change should happen*. These coincide ~70% of the time, not 91%.
When in doubt: read the chunks in ranks 2–5 before committing to a target repo.

## Quick example — when rules 7 and 10 save you

> User: "Change the database timeout for the staging environment of the order
> service."

Workspace search ranks the **order-service code repo** at #1 (it's where the word
"database" appears most). But the change needs to land in the
**environment-platform manifests repo** at rank #4. If you stopped at top-1 you'd
edit the wrong file. Rules 7 and 10 remind you to scan further.
