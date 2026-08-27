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

### `file_hits` and `results` are counted on different tiers

`results` is summed on the project tier, `file_hits` on the per-file tier, and
they normally agree. They are **not** a cross-check on each other, because two
deliberate behaviours separate them: the recorder drops per-file detail (never
query counts) when its pending buffer hits its cap, and an empty path is skipped
on the file tier while still counting toward `results`.

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

## What it costs a search

The counters sit on the critical path of the operation this server exists to
perform, and every search on the box takes the same mutex. Three separate
questions follow, and each has a measurement rather than an argument. The
benchmarks are committed:

```
go test -run XXX -bench . ./internal/searchstats/
go test -run XXX -bench . ./internal/httpapi/ -benchtime=4000x
CIX_SCALE_TEST=1 go test -run TestScaling -v -timeout 30m ./internal/searchstats/
```

Figures below are medians of 3 on a 14-core machine, Go 1.26, `-cpu=8`.

### 1. Does recording slow a search down?

| | recording off | recording on | delta |
|---|---|---|---|
| file search, sequential | 74.6 µs | 77.2 µs | **+2.6 µs (+3.5%)** |
| file search, 8 concurrent | 93.3 µs | 96.4 µs | **+3.1 µs (+3.3%)** |

`+3.3%` is measured against **the cheapest endpoint there is** — file search is
~75 µs, so a fixed few microseconds is a visible fraction of it. Against the
searches people actually wait for, on a 45-repo / 1.9M-chunk corpus where a
single-project semantic search takes ~1.4 s and a workspace search ~10.5 s, the
same few microseconds are **0.0002%**.

Workspace search is the case with the most recording to do — one `Record` per
project the fan-out scanned — so its cost scales with the *workspace*, not with
the repositories in it: 4 µs at 8 projects, 26 µs at 45, 57 µs at 100. Nothing
scales with repository size, because recording is driven by the result set,
which the caller's `limit` bounds.

### 2. Does it degrade when many searches run at once?

No. The relative overhead is the same sequential (+3.5%) and 8-way parallel
(+3.3%) — a constant per call, not contention that grows with load. From the
other side, `Record` itself:

| | ns/op | allocations |
|---|---|---|
| uncontended | 582 | 0 |
| 8 goroutines, one shared project | 748 | 0 |
| 8 goroutines, eight projects | 771 | 0 |
| 8 goroutines, flusher running every 2 ms | 755 | 0 |

Contention costs about 1.3×, and then stops — the one-project and eight-project
cases are within noise of each other, and a flusher running 200× more often than
production changes nothing.

### 3. Does a large statistics database reach the search response?

No, and this is the one worth being explicit about, because the intuition that
it *should* is reasonable: a bigger database means slower writes, and slower
writes usually mean slower responses.

They are decoupled here. `Record` writes to a map and returns; the database
write happens on a background goroutine, which takes the shared mutex only long
enough to swap two map pointers and does its I/O outside the lock. Measured
directly — a flush of 40,000 upserts into a 37 MB database, taking **284 ms**,
with 621,108 `Record` calls sampled entirely inside that window:

| | p50 | p99 | max |
|---|---|---|---|
| idle database | 458 ns | 87.6 µs | 331 µs |
| **during the 284 ms flush** | **375 ns** | **81.5 µs** | 600 µs |

Unchanged. The tail is mutex hand-off between eight goroutines, present with or
without a flush in flight.

The same holds across database sizes — `Record` is flat at 292 ns from 3,700
rows to 1.8 million, and a flush stays around 1 ms because an upsert into a
b-tree grows with its logarithm, not its size:

| rows | file size | `Record` | flush |
|---|---|---|---|
| 3.7 k | 0.3 MB | 292 ns | 0.5 ms |
| 90 k | 7.7 MB | 292 ns | 1.0 ms |
| 450 k | 37.5 MB | 292 ns | 0.9 ms |
| 1.8 M | 144.4 MB | 292 ns | 1.3 ms |

With a 10-second flush interval against a ~1 ms flush, there are four orders of
magnitude of headroom before the writer could fall behind its own schedule.

### 4. What the database size DOES affect: the dashboard

The statistics *page* is a different story, and the first measurement of it was
bad: the admin view aggregates every visible project, and an admin's scope is
every project on the server.

| rows | before | default sort | sorted by a file column |
|---|---|---|---|
| 3.7 k | 5.1 ms | 3.4 ms | 3.4 ms |
| 90 k | 109 ms | 36 ms | 63 ms |
| 450 k | 542 ms | 105 ms | 295 ms |
| 1.8 M | **2,267 ms** | **211 ms** | **1,176 ms** |

Two changes, both from reading the query plans rather than guessing:

- **The aggregate ran twice.** The row count and footer sums were a second
  statement wrapping the identical CTE chain. They are now window functions over
  the page's own result set — one pass, and the three figures still cannot
  disagree about what matched, because they are one query.
- **The per-file aggregate is now conditional.** Computing `file_hits`,
  `distinct_files` and `top_file_hits` for *every* scoped project is only
  necessary when the ORDER or a filter depends on them. The default view sorts
  by query count, which lives in `search_totals` at one row per project — so the
  file columns are computed for the ~25 projects on the page instead, in one
  statement. Sorting by a file column still pays the full cost, correctly.

The default view now scales with *projects on the page × files per project*
rather than with the size of the database.

**Sorting by a file column still pays the full cost**, and the third column
above is what that costs rather than an extrapolation: ~1.2 s at 1.8 M rows.
That is a reasonable price for a click — and it is *not* a reasonable price
every thirty seconds for a tab left open on that sort, so the dashboard's
auto-refresh backs off to two minutes whenever the sort or a filter is
file-derived. Poll frequency follows query cost.

Two tests keep this honest. One asserts the two query shapes return identical
numbers — across both tiers, with and without a kind filter, on a window that
actually excludes rows, and on a page that is a strict subset of the matched
set — so the fast path cannot drift from the slow one. The other pins
`fileDerivedSorts` against `sortColumns`, because the failure mode of adding a
file-derived sort key without declaring it is not an error but a page of
silently wrong numbers.

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
foreign key between them this is an explicit best-effort call made *after* the
delete has already succeeded — failing the delete over a chart would leave the
project half-removed.

That leaves a hole, and the prune task closes it. If the call fails, or the
process dies between the two, the counters outlive their project: no API read
can surface them (every read is scoped to projects the caller can see, and a
deleted project is in nobody's set) and the totals tier is never pruned, so they
would sit there forever. The nightly sweep drops counters for any project that
no longer exists, and logs when it finds some — reaching that line means a
delete did not clean up at the time, which is worth knowing even though the
sweep handled it.

An **empty** live-project list is treated as "don't know" and sweeps nothing. A
server with genuinely zero projects exists, but so does a failed query that
returns nothing, and keeping orphans one more day beats wiping every counter.

---

## Maintenance

`searchstats.prune` is a registered recurring task (default `20 3 * * *`,
adjustable at `/dashboard/server`). It drops window rows past the retention
horizon, reclaims the pages they freed, and sweeps counters whose project is
gone. It is scheduled rather than run on every flush, which would issue a
`DELETE` matching nothing several times a minute.

The database is **not** covered by `/dashboard/server` → Resources, and the
compaction and reclaim tasks act on `projects.db` only. It does not need them:
the file is created with `auto_vacuum=INCREMENTAL` (set on a dedicated
connection before the schema exists, because the mode can only be chosen while
the header is unwritten), and the prune runs `PRAGMA incremental_vacuum` after
deleting, so the space actually returns to the filesystem. `bucket` leads the
primary key of both window tables, so the delete itself is a contiguous range at
the front of the b-tree.

### What does not shrink on its own

`search_file_totals` is bounded by *(distinct files ever returned × kinds ×
projects)* and is never pruned — that is what makes the all-time numbers
all-time. It is small in practice, because only files that have actually
appeared in a result exist as rows, but it grows monotonically with how much of
each repository has ever been searched. If it ever matters, the remedies are
`POST /admin/search-stats/reset` or deleting the file.

To start over, `POST /admin/search-stats/reset`, or stop the server and delete
`searchstats.db` — nothing else refers to it.

---

## Turning it on

**Off by default.** A server that is upgraded does not quietly start collecting;
somebody has to ask for it. There are two ways to ask, and they are ordered:

1. **The switch on the statistics page** (admin only). Takes effect immediately —
   no restart. Enabling opens the database, creating it on first use; disabling
   flushes whatever is buffered and closes it. The decision is stored.
2. **`CIX_SEARCH_STATS_ENABLED=true` at deploy time.** This is the starting
   position for a fleet, for an operator provisioning servers that should
   collect from their first boot.

**A stored decision outranks the environment.** That ordering is the point: an
admin who turns collection on in the dashboard must not have it turned off again
by the next container start carrying the old environment. The environment gives a
server its starting position; it does not re-assert itself forever.

The page reports which of the three is speaking — `database` (an admin decided,
and when), `environment` (the variable is set and nobody has overridden it), or
`default` (nobody has asked, so it is off).

Switching collection off **keeps** what was already collected — the counters are
still there if it is switched back on. The table does not read them while
collection is off, because off closes the database; but
`POST /admin/search-stats/reset` works either way, and the dashboard keeps its
**Clear counters** button visible while off. Disposal must not require switching
collection back on: that would make "stop recording" and "delete what you
recorded" the same lever.

A server that never turns the feature on never creates `searchstats.db`. A
failure to open it is **not** fatal: the server logs a warning, runs without the
feature, and the page reports it as off.
