# Database maintenance

SQLite does not shrink a file when rows are deleted. The pages go on a freelist
and are reused by later writes, so a database that has lost more data than it
has since gained stays large and mostly empty. The instance that prompted this
feature was **8.86 GB with 48% of the file on the freelist** — 4.3 GB of
nothing.

Server → Resources → Database reports that and offers three ways to act on it.

## The three actions

| | What it does | Cost |
|---|---|---|
| **Checkpoint log** | Folds the write-ahead log back into the database file | Seconds, no window |
| **Reclaim now** | Returns free pages to the filesystem in bounded chunks | Milliseconds per chunk, no window, no restart |
| **Compact now** | Rebuilds the database into a fresh file and replaces it | A read-only window, then a restart |

Reclaim needs the database to be in **incremental** auto-vacuum mode. That is a
**setting**, not an action, and it lives on its own two-way switch. Compaction
never changes it: asking for space back and asking to change a setting are
different requests, and one must not quietly do the other.

Moving the switch does cost a rebuild, in either direction, because rebuilding
the file is the only way SQLite can change the mode of a populated database.
Moving it to the position it is already in costs nothing and does nothing.

Reclaim returns space but does not defragment. Compaction rebuilds the file and
improves read locality, so it stays useful — just rarely, rather than as the
only tool available.

## What a compaction actually does to the server

**For the copy — roughly a minute per 8 GB on a warm SSD — the server is
read-only, not down.**

- Search, browsing and every read keep working.
- New logins and every change are refused with `503` and a `Retry-After`.
- Indexing and scheduled polling pause. The CLI watcher already retries and
  re-indexes pending changes on recovery, so a refused write is a delay rather
  than a loss.
- Existing sessions and API keys keep working. Their last-seen timestamps stop
  being refreshed for the duration, which is invisible against a 14-day
  session lifetime.

**Then the server restarts itself** and is unavailable until it has finished
starting up.

The restart is the mechanism, not a fallback. Thirteen long-lived services hold
the database handle and none of them can be repointed at a new file, so the
swap is performed at boot, before anything opens it. The process re-executes
itself rather than exiting, so a container keeps its PID 1 and no restart
policy or supervisor is involved.

### Why writes have to stop

Compaction is built on `VACUUM INTO`, which produces a **snapshot**: the copy's
contents are fixed at the moment its read transaction opens. Measured on a
clone of the real 8.9 GB database, 18 200 rows were written during a 76-second
copy and 850 of them reached the copy. Adopting such a copy would have silently
discarded the rest.

The freeze is three layers deep:

1. **A route gate** refuses the endpoints that write, in microseconds. It is
   classified per route, never by HTTP method — search is a `POST` and has to
   keep working.
2. **Background work is stopped and drained**, not merely asked to stop.
3. **The compactor holds a write transaction** for the duration, so anything
   that slips past the first two is refused by SQLite itself.

The gate leads because the lock alone would be a disaster: a refused write sits
in SQLite's busy handler for the full timeout — measured at 5.06 s — holding one
of eight pool connections, and eight of those stall reads too.

## If the machine dies mid-operation

Nothing is lost, and nothing needs to be resumed by hand.

Progress is journalled to `maintenance.json` beside the database, written
atomically, with an append-only trail in `maintenance.log`. On the next start,
every combination of that journal and the files actually present on disk maps
to exactly one recovery action. An interrupted copy is discarded; an
interrupted swap is carried forward or rolled back. The original is deleted
last, so no interruption can leave the server without a database.

A compacted copy is only adopted after it has been proved to be this database:
row counts taken from the source under the freeze are re-checked against the
copy, together with the header's own claim about the file's length. A copy that
fails either is discarded and the original is kept.

Databases under 512 MB additionally get a `PRAGMA quick_check`. Larger ones do
not, and that is a measured decision rather than an omission: on a real 4.5 GB
copy the check did not finish inside a 30-second budget, and since it runs at
boot before the listener binds, it was buying thirty seconds of extra downtime
and then discarding its own result.

The state lives in a file rather than a table because the database is the thing
being replaced: a row describing the operation could not be written during the
read-only window, could not survive the swap, and could not be read back during
the restart.

## What a real run looked like

Against a copy of a production database, 8.25 GB with 47% waste:

```
00:00  compaction requested                 202, server goes read-only
00:00  reads 200 · writes 503 · health 200  throughout
01:35  copy complete, 4.4 GB, verified
01:35  server re-executes itself
02:07  listening again
       8.25 GB → 4.18 GB, 4.07 GB returned to the filesystem
       48 projects, 297 563 chunks, 2 users — unchanged
```

The read-only window was 95 seconds; full unavailability was the ~30 seconds
of restart after it.


## Automatic reclaim

A schedule fires only when everything lines up at once: enabled, the interval
has elapsed, the waste is over **both** thresholds, the clock is inside the
window if one is set, no clone or index job is in flight, and — for a full
compaction — there is room for the copy. Anything else and the tick is silent
and tries again next hour.

Two thresholds rather than one: a percentage alone nags on a small database
where 40% of 12 MB is not worth a window; an absolute figure alone nags on a
large one where 500 MB of slack is normal headroom.

Defaults depend on the database's own mode. A file created by a recent build
can reclaim incrementally, so daily reclaim is on. A database carried over from
an older install cannot, and the only thing automation could do for it is the
expensive rebuild — so it stays off until an admin opts in. **An upgrade never
starts blocking anybody's server on its own.**

Full compaction is allowed on a schedule but is never the default.

### Configuration

Set in the dashboard, or by environment for deployments nobody opens a
dashboard for. The dashboard shows where each effective value came from.

| Variable | Meaning |
|---|---|
| `CIX_DB_MAINTENANCE_ENABLED` | `true` / `false` |
| `CIX_DB_MAINTENANCE_MODE` | `incremental` or `full` |
| `CIX_DB_MAINTENANCE_INTERVAL_HOURS` | Minimum hours between runs |
| `CIX_DB_MAINTENANCE_MIN_FREE_PERCENT` | Waste threshold, percent of the file |
| `CIX_DB_MAINTENANCE_MIN_FREE_BYTES` | Waste threshold, absolute |
| `CIX_DB_MAINTENANCE_WINDOW_START_HOUR` | Local hour, 0–23; set both or neither |
| `CIX_DB_MAINTENANCE_WINDOW_END_HOUR` | Local hour, 0–23; may wrap past midnight |

A dashboard setting overrides the environment; the environment overrides the
derived default.

## Incremental auto-vacuum, and what it costs

Incremental mode maintains pointer-map pages so free pages can be moved to the
end of the file and truncated away. Every page allocated or freed therefore
carries an extra write, and this server's hot path is bulk-inserting chunk and
symbol rows.

Measured on an indexing-shaped workload — 120 000 wide rows inserted in batched
transactions, then a bulk delete:

```
none           insert 1.651s   delete 129ms   file 71.7 MB
incremental    insert 1.676s   delete 133ms   file 71.7 MB
```

**+1.5% on insert, no measurable difference elsewhere.** New databases are
created in incremental mode on the strength of that. Existing databases are
left alone — the pragma is silently ignored on a file that already has tables,
so upgrading changes nothing until an admin asks for it.

If that 1.5% matters more than being able to reclaim space without a rebuild,
the switch turns off as readily as it turns on.

## Monitoring

`GET /maintenance/status` is public and reads only the state file, so it keeps
answering while sessions are unwritable and again the moment a restarted server
is listening. It cannot answer *during* the restart itself — the listener binds
at the end of startup — so a poller should render that gap as "reconnecting"
rather than as an error. The dashboard banner does exactly that.

`/health` returns `200` with `"maintenance": true` while frozen, without
touching the database. It has to: the container healthcheck runs every 30 s
with three retries and a restart policy acts on the result, so a failing probe
here would kill the compaction it was reporting on.
