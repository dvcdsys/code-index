# Troubleshooting & search tuning

## Common issues

**Server refuses to start: `bootstrap auth: no users in database and the bootstrap admin env vars are not set`** → Set both `CIX_BOOTSTRAP_ADMIN_EMAIL` and `CIX_BOOTSTRAP_ADMIN_PASSWORD` in your `.env`, restart. Once you log in and change the password, you can drop the env vars (the user lives in the DB).

**`API key not set` from CLI**
```bash
cix config set api.key $(grep CIX_API_KEY /path/to/code-index/.env | cut -d= -f2)
# or mint a fresh one in the dashboard's API Keys page
```

**`connection refused`**
```bash
curl http://localhost:21847/health                    # is the server up?
docker compose up -d                                  # start (CPU)
docker compose -f docker-compose.cuda.yml up -d       # start (CUDA)
```

**`project not found`** — run `cix init /path/to/project`.

**Watcher not triggering reindex**
```bash
cix watch status
cat ~/.cix/logs/watcher.log
cix watch stop && cix watch /path/to/project
```

**Search returns no results**
- Check the project is indexed: `cix status`
- Lower the threshold: `cix search "query" --min-score 0.2` (default `0.4`)
- `cix list` to verify the project is registered

**Dashboard shows "Stale model" on every project after upgrade** → The runtime model was changed (or its version stamp shifted). Either reindex affected projects (`cix reindex --full` per project) or revert the model change in **Server → Runtime settings → Embedding model**.

**First boot after upgrading to 0.13 takes minutes and the server answers nothing** → It is importing your existing vectors from the legacy chromem files into the SQLite vector store. That runs before the HTTP listener binds — 17 s on a 312k-document index, longer on a slow disk or a cold cache. It logs progress at warn level (`migrating vector store …`) and is resumable, so an interrupted import picks up where it stopped rather than starting over. Nothing is re-embedded and no reindex is needed. See [`VECTORSTORE.md`](VECTORSTORE.md#migration-from-chromem-go).

**Every write answers `503` with a `Retry-After`, but reads work** → A database compaction is running. The server is deliberately read-only for the duration and then restarts itself to adopt the compacted file; `/health` keeps returning `200` (with `"maintenance": true`) so a container restart policy does not kill it mid-run. `GET /maintenance/status` and the dashboard banner report progress. See [`DATABASE_MAINTENANCE.md`](DATABASE_MAINTENANCE.md).

**Memory grew after setting `CIX_VECTOR_MMAP_SIZE`** → That is what it does: it trades resident memory for search latency by letting SQLite map the vector database into the process. A fan-out across a large index can then hold gigabytes rather than tens of megabytes. Unset it to go back, or size it deliberately under a memory ceiling. See [`VECTORSTORE.md`](VECTORSTORE.md#pragmas).

**Dashboard banner says an update is available** → A newer `server/v*` release is on GitHub. Click through to the release notes; bump your Docker tag / native build at a convenient time. Disable the poll with `CIX_VERSION_CHECK_ENABLED=false` if you don't want it. See [`UPDATES.md`](UPDATES.md).

**Workspace repo stuck in `cloning` or `indexing`** → Check **Workspaces → Jobs** in the dashboard or `GET /api/v1/jobs?status=running`. Common causes: PAT missing `repo` scope on a private repo, network not reaching github.com, sidecar not ready. See [`WORKSPACES.md`](WORKSPACES.md#troubleshooting).

**Forgot the admin password and there's no second admin** → Reset it offline on the server machine — no restart needed, the account is forced to change it on next login:
```bash
./server/scripts/reset-password.sh you@example.com                      # native install
docker exec -i <container> /cix-server -reset-password you@example.com  # Docker
```
Better long-term: keep at least two admin accounts so this never recurs. Details: [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md).

---

## Tuning search quality

`cix` defaults to `--min-score 0.4`, calibrated for **CodeRankEmbed-Q8_0** with
the path-aware embedding format. Typical score landscape on a real codebase:

| Match strength | Score range | Action |
|---|---|---|
| Exact symbol or filename match | 0.65 – 0.80 | rare; very high confidence |
| Strong path-aware concept match | 0.50 – 0.65 | typical "good" match |
| Weaker concept / partial path overlap | 0.40 – 0.50 | typical for ambiguous queries |
| Likely unrelated noise | < 0.40 | filtered out by default |

**When to lower the threshold:** sparse queries returning no results — try
`--min-score 0.25`. Exploring an unfamiliar codebase — `--min-score 0.2`. Rare
single-word identifiers.

**When to raise it:** agent context filling up with weak matches —
`--min-score 0.5` or `0.6`.

CodeRankEmbed is asymmetric: queries and passages live in different regions of
the embedding space, so cosine similarities are systematically lower than for
symmetric models. Don't compare these numbers to thresholds quoted for OpenAI /
Voyage / generic sentence-transformers. Full details — including hybrid
workspace scoring — in [`SEARCH_ALGORITHM.md`](SEARCH_ALGORITHM.md).

For noisy directories (vendored code, fixtures, legacy migrations),
`--exclude vendor --exclude bench/fixtures` works per-query, or add entries to
`.cixignore` to skip them at indexing time.
