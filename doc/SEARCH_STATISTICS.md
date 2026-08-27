# Search statistics

Per-project counters: how often each project is searched, and which of its
files keep coming back in the results. Visible at `/dashboard/search-stats`,
served by `GET /api/v1/search-stats`.

**Counters only.** No query text is stored, and there is no per-request log
anywhere — not on disk, not in memory beyond the flush interval. What is kept
is a set of integers keyed by (project, kind) and (project, kind, file).

---

## What is counted

Every successful search records one query against the project, plus one hit for
each file that appeared in the result.

| Kind | Endpoint |
|---|---|
| `semantic` | `POST /projects/{hash}/search` |
| `symbols` | `POST /projects/{hash}/search/symbols` |
| `definitions` | `POST /projects/{hash}/search/definitions` |
| `references` | `POST /projects/{hash}/search/references` |
| `files` | `POST /projects/{hash}/search/files` |
| `workspace` | one project's slice of `GET /workspaces/{id}/search` |

The kinds are stored separately rather than summed, because they cost wildly
different things: a semantic query embeds text and scans a vector collection,
while a definition lookup is one indexed `SELECT`. A single "searches" number
would average those together and mean nothing.

Only successful searches count. A request refused for access, rejected for a
malformed body, or failed at the embedding step did not search the project, and
counting it would make the numbers a measure of client bugs. A search that
legitimately returned **nothing** is counted, with no files — a project with
many queries and few file hits is one whose index has stopped answering, and
that is worth being able to see.

### A file's hit count is per SEARCH, not per match

A file counts once per search, however many of its chunks matched. This is what
keeps the two columns comparable: a file's hits can never exceed the project's
query count, so a row reads as *"this file came back in 42 of the project's 128
searches"*. Counting each matching chunk instead would let the number exceed the
number of searches, and the column would stop having an interpretation.

### Workspace attribution

One workspace query records a `workspace` query against **every project it
actually scanned** — the fan-out paid for that work whether or not the project
made the answer, and a repo carrying a busy workspace's traffic should not look
idle.

File hits come only from the projects that cleared the relevance threshold, and
specifically from the *surviving* set rather than the displayed panel. The panel
is truncated to the caller's `top_projects` parameter, and letting a request
parameter decide what gets counted would make the stored numbers a measurement
of how the caller configured their request.

---

## Two tiers, two retention policies

| Table | Retained | Answers |
|---|---|---|
| `search_totals`, `search_file_totals` | forever | "How much is this project searched?" |
| `search_buckets`, `search_file_buckets` | 7 days, 30-minute buckets | "What did the last few days look like?" |

They are separate tables rather than one bucketed table that gets summed,
because folding them together would mean choosing which one to break: deriving
the totals by summing the buckets makes the totals silently drop every time the
window slides, and keeping the buckets forever makes the file unbounded.

Neither table grows with the calendar. Rows exist only where there was
activity, so a quiet week costs nothing, and the window tier is additionally
capped at 336 buckets per key by its retention.

`last_seen` always comes from the cumulative tier, even in a windowed view:
`search_buckets` carries only a bucket floor, and rounding "last searched" to
the nearest half hour would be a worse answer than the exact one sitting in
`search_totals`.

---

## Why a separate SQLite file

`searchstats.db` sits next to `projects.db` and is opened independently. The
reasons are specific to this server rather than general tidiness:

1. **Search must keep serving during a database compaction.** That is not an
   aspiration — it is encoded in `httpapi.readOnlyPostSuffixes`, where `/search`
   is listed as a POST that may proceed while writes are frozen. A counter
   written into `projects.db` would either need an exemption from the freeze
   (writing into a snapshot about to be discarded) or would block on the
   compactor's held write transaction for the full `busy_timeout` — measured at
   5.06 s — while holding one of the eight connections that pool allows. Eight of
   those and reads stall too. The same hazard already forced `Sessions.Touch` to
   be skipped while frozen.
2. **The indexer owns the write lock in bursts.** `chunks_fts` and `chunks_meta`
   are written per file, in a transaction per file. Analytics writes have nothing
   to do with indexing and should not queue behind a full reindex.
3. **Churn drives the compaction advice.** The bucket tables are delete-heavy by
   design. In `projects.db` that churn inflates the free-list, and the free-list
   is what the dashboard's "time to compact" verdict is computed from —
   statistics would start recommending maintenance windows for the main database.
4. **Blast radius.** These are derived numbers about traffic already served.
   Losing them costs a chart, never an answer, so the file can be deleted.

The cost of the split is that the counters cannot be `JOIN`ed against
`projects`. That turned out not to matter: the endpoint has to resolve which
projects the caller may see from the system database anyway, so every query is
already parameterised by a set of project paths.

### Recording never blocks a search

Counters accumulate in memory and are flushed in one transaction every 10
seconds. `Record` takes a mutex and writes to a map; it touches no database and
returns no error. On a flush failure the batch is dropped rather than retried —
retrying would hold a failed batch in memory while new counters accumulate
behind it, turning a transient disk error into unbounded growth. The failure is
logged.

The pending per-file map is capped at 100,000 distinct keys. Past the cap file
detail is dropped and counted in the log while query counts keep accruing;
analytics is never allowed to be the reason the server runs out of memory.

---

## Access

Both read endpoints are **GroupRead**. A regular user sees only the projects
they can already search — their own, plus external projects shared to a
view-group they belong to. Admins see everything.

This matters more than for an ordinary list: the counters carry **file paths**
out of every project on the server, so an unscoped response would leak the
directory structure of repositories the caller cannot open. See
`docs/AUTH_REVIEW.md`.

`POST /api/v1/admin/search-stats/reset` is admin-only and empties both tiers.

Deleting a project discards its counters. Because the two databases have no
foreign key between them this is an explicit best-effort call after the delete
has already succeeded — failing the delete over a chart would leave the project
half-removed. A row whose project has gone is rendered with `exists: false`
rather than dropped, so a cleanup that did not run is visible.

---

## Maintenance

`searchstats.prune` is a registered recurring task (default `20 3 * * *`,
adjustable at `/dashboard/server`) that drops window rows past the retention
horizon. It is scheduled rather than run on every flush, which would issue a
`DELETE` matching nothing several times a minute.

The database is **not** covered by `/dashboard/server` → Resources, and the
compaction and reclaim tasks act on `projects.db` only. It needs no maintenance:
`bucket` leads the primary key of both window tables, so pruning is a contiguous
range at the front of the b-tree, and the file is created with
`auto_vacuum=INCREMENTAL`.

To start over, `POST /admin/search-stats/reset`, or stop the server and delete
`searchstats.db` — nothing else refers to it.

---

## Turning it off

`CIX_SEARCH_STATS_ENABLED=false` records nothing, serves 503 from the three
endpoints, and never creates the file. The dashboard page then explains that the
feature is switched off rather than showing zeroes.

A failure to open the database at boot is **not** fatal: the server logs a
warning and runs without the feature.
