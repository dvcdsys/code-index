# Database maintenance

SQLite does not shrink a file when rows are deleted. The pages go on a freelist
and are reused by later writes, so a database that has lost more data than it
has since gained stays large and mostly empty. The instance that prompted this
feature was **8.86 GB with 48% of the file on the freelist** — 4.3 GB of
nothing.

Server → Resources → Database reports that and offers two ways to act on it.

## The two actions

| | What it does | Cost |
|---|---|---|
| **Reclaim now** | Returns free pages to the filesystem in bounded chunks | Milliseconds per chunk, no window, no restart |
| **Compact now** | Rebuilds the database into a fresh file and replaces it | A read-only window, then a restart |

Reclaim folds the write-ahead log back into the database file as part of its
work, because in WAL mode the file does not actually shrink until that has
happened. There is no separate control for it: SQLite checkpoints the log
automatically once it reaches 1000 pages — 4 MB, which is where it sits — so a
button offering to reclaim those 4 MB from a multi-gigabyte database would be
duplicating the automatic behaviour in the ordinary case and unavailable in
the one case it would matter, since a log that has grown large is a log some
reader is holding open.

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


## Running it on a schedule

Both operations can run on a **crontab expression**, in the server's local
timezone, and each has its own on/off switch:

| Task | Default | On by default |
|---|---|---|
| `db.reclaim` | `0 3 * * *` | only on a database already in incremental mode |
| `db.compact` | `0 4 * * 0` | never |

An interval would have been the smaller change and the wrong one. "Every 24
hours" is measured from the last run, so a single manual compaction at 18:00
moves every subsequent nightly run to 18:00 and it drifts from there. cron is
anchored to the clock, which is what "every night at midnight" means.

The schedule says *when to look*; the thresholds say *whether it is worth it*.
A due run still does nothing unless the waste is over **both** 25% and 256 MB —
a percentage alone nags on a small database where 40% of 12 MB is not worth the
work, an absolute figure alone nags on a large one where 500 MB of slack is
ordinary headroom. Indexing in flight also defers a run, including a CLI push,
which holds no row in the jobs table.

Defaults depend on the database's own mode. A file created by a recent build
can reclaim incrementally, so nightly reclaim is on. A database carried over
from an older install cannot, and the only thing automation could do for it is
the expensive rebuild — so it stays off until an admin opts in. **An upgrade
never starts blocking anybody's server on its own.**

### What crontab means here, exactly

- **A missed slot is not queued up.** A run that overruns its own schedule
  loses the slots it ran through rather than firing a burst afterwards.
- **A slot missed while the server was down** is skipped for `db.compact` —
  noticing at 09:00 would mean a read-only window in the middle of the working
  day — and caught up for `db.reclaim`, which costs milliseconds and would
  otherwise never run at all on a laptop that is asleep every night.
- **The next run is computed from the clock**, never from when the previous one
  finished, so a slow run cannot make the schedule drift.
- **Daylight saving is wall-clock.** On the spring forward, an expression
  naming an hour that does not exist that day runs at the first valid instant
  after the jump — 03:00 in Kyiv fires at 04:00 — which is what vixie cron does
  and the only alternative to silently missing a night once a year. On the
  autumn repeat it fires once, not twice.
- **An expression that can never match is refused**, not accepted and silently
  never run. `0 0 30 2 *` is a configuration error.

The dashboard shows the next three runs beside the field, computed on the
server by the same parser that fires them — a second cron implementation in the
browser could only ever disagree with the first.

### Configuration

Set in the dashboard, or by environment for deployments nobody opens a
dashboard for.

| Variable | Meaning |
|---|---|
| `CIX_DB_MAINTENANCE_CRON` | Default schedule for the database tasks |
| `CIX_DB_MAINTENANCE_MIN_FREE_PERCENT` | Waste threshold, percent of the file |
| `CIX_DB_MAINTENANCE_MIN_FREE_BYTES` | Waste threshold, absolute |

An invalid expression is refused at startup rather than at the first tick. A
schedule saved in the dashboard overrides the environment.

### The scheduler underneath

`internal/schedule` is a general registry, not a database feature: a table of
named tasks, one timer armed at the earliest of them, and a handler called
in-process when a task is due. Polling and cleanup can hang off the same
machinery.

It sleeps until the next armed run rather than polling — a server with two
daily tasks has no reason to wake every thirty seconds to be told it is not
time yet — with the wait capped at five minutes, because a suspended laptop
does not advance the monotonic clock and a timer armed for eight hours can come
back arbitrarily late.

The one recurring job still outside it is the update check, which keeps its own
ticker: its period is `CIX_VERSION_CHECK_INTERVAL`, a released duration-valued
variable, and a duration does not survive the trip through crontab — `6h` maps
cleanly, `7h` does not exist at all. Moving it means either breaking that
variable or carrying both forms, which is a decision of its own rather than a
tidy-up.

It is deliberately **not** a job queue. The server already has one — the `jobs`
table, with retries, dedupe and a worker — and a second persistence model beside
it would mean two places to look when something did not run. A task that wants
durable, retryable work enqueues it into `jobs`; that is the seam. Compaction is
the reason it could not simply live in the queue in the first place: it drains
that queue as part of taking the server read-only, so a trigger inside it would
be draining itself.

The slot is claimed on disk **before** the handler runs. That is correctness,
not bookkeeping: compaction re-executes the process as its final step, and a
slot still marked due when the new process starts would fire it again, and
again.

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
left alone: the mode is set once, on a file this server is creating, and never
again.

That is deliberately narrower than it first appears it needs to be. SQLite
ignores the pragma on a populated database only when honouring it would mean
moving pages — going to or from `none`. Between `full` and `incremental` it
applies immediately, so setting it on every connection would have converted a
database somebody had deliberately put in full auto-vacuum, on nothing more
than an upgrade. The reclaim mode has a switch of its own; nothing else gets to
move it.

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
