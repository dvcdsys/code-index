# Vector store

cix stores chunk embeddings in SQLite and searches them with a streamed
brute-force scan. This document describes the layout on disk, the one-time
migration from the previous engine, and the environment variables that tune it.

## Why it changed

The previous engine was [chromem-go](https://github.com/philippgille/chromem-go),
an in-memory vector database with gob-file persistence. It loads **every
document of every collection into the process heap at startup and never
evicts**. Measured on a real 312,334-document / 47-collection index, against
this implementation on the same data:

| | chromem-go | SQLite store |
|---|---|---|
| Resident memory, idle | **2209 MB** | **19 MB** |
| Resident memory after a fan-out over all 47 collections | 2209 MB+ | **26 MB** |
| Time from process start to first answerable query | **47 s** | **≈1 ms** |
| Search latency, 74k-doc collection, k=10 / k=500 | 34 / 38 ms | 139 / 146 ms |
| Fan-out over all 312k documents, warm | ~150 ms (estimated, all in RAM) | 510 ms |
| Disk | 2.5 GB of gob files | 1.86 GB (chunk text included) |
| Import of the whole index | — | 17 s |

Memory was proportional to the index rather than to the work. It is now
proportional to the work: nothing is loaded at open, and a query costs a few
page-cache buffers that are returned to the OS when the connection goes idle.
The trade is search latency — roughly 4x slower, and nearly unchanged by the
result limit (k=500 costs 5% more than k=10, against 11% for chromem).

## Layout on disk

```
<data>/
  chroma/                        # legacy chromem-go tree, read-only, never modified
    ollama/<model-slug>/
      <8-hex>/00000000.gob …     # one gob per document
  vectors/                       # live vector store
    ollama/<model-slug>/
      vectors.db                 # + -wal, -shm
```

One SQLite database per **embedding namespace** — the provider kind, model slug
and optional variant, exactly the components chromem was namespaced by
(`Config.VectorDirFor` mirrors `Config.ChromaDirFor`). Vectors of different
dimensions can therefore never share a database.

The two trees are siblings rather than nested so the legacy files stay
untouched as a rollback path, and so reclaiming them later is one directory
removal that cannot touch a live database.

Each namespace gets its own *directory* holding `vectors.db`, because a live
SQLite database is three files (`.db`, `-wal`, `-shm`) and the maintenance
surface — namespace scanning, size accounting, active-namespace protection —
works in terms of directories.

## Schema

```sql
CREATE TABLE collections (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE vectors (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  doc_id        TEXT NOT NULL,
  file_path     TEXT NOT NULL,
  start_line    INTEGER NOT NULL,
  end_line      INTEGER NOT NULL,
  chunk_type    TEXT NOT NULL DEFAULT '',
  symbol_name   TEXT NOT NULL DEFAULT '',
  language      TEXT NOT NULL DEFAULT '',
  embedding     BLOB NOT NULL,            -- little-endian float32, dim = len/4
  PRIMARY KEY (collection_id, doc_id)
);
CREATE INDEX idx_vec_coll      ON vectors(collection_id);
CREATE INDEX idx_vec_coll_file ON vectors(collection_id, file_path);

CREATE TABLE vector_contents (
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  doc_id        TEXT NOT NULL,
  content       TEXT NOT NULL,
  PRIMARY KEY (collection_id, doc_id)
) WITHOUT ROWID;

CREATE TABLE migration_state (
  collection_name TEXT PRIMARY KEY,
  migrated_at     TEXT NOT NULL,
  docs            INTEGER NOT NULL
);
```

Collection names (`project_<md5hex(project_path)>`) and document IDs
(`<md5hex(file_path)[:12]>:<start>-<end>:<idx>`) are **frozen compatibility
contracts** shared with the archived Python backend and with every index
already on disk. That is what lets the gob files be imported verbatim.

**Why the id guards.** A collection id is *cached* by callers — `Store.collIDs`,
and through it the indexer — across a window in which an admin can delete the
collection. Both guards close a hole that window opens:

- Without `AUTOINCREMENT`, `collections.id` is the rowid and SQLite reuses the
  largest free one. Deleting the highest-numbered collection hands its id to the
  *next* collection created, so any row that outlived the delete is silently
  adopted by an unrelated project and search answers one project's query with
  another project's chunks.
- Without the foreign keys, a late upsert commits rows whose `collection_id` has
  no `collections` row. `ListCollections` joins **from** `collections`, so those
  rows are invisible to every count, every size figure and the orphan sweep —
  they simply hold disk forever. With the constraint the upsert fails loudly
  instead; see [Schema versions](#schema-versions).

## Schema versions

`PRAGMA user_version` carries the schema version, and `openDB` upgrades an older
file before the connection pool is created.

| version | shape |
|---|---|
| 0 | The original schema. Nothing stamped `user_version`, so a v1 file reports 0. Plain rowid collection ids, no foreign keys, `auto_vacuum` off. |
| 2 | `AUTOINCREMENT` ids, `ON DELETE CASCADE` foreign keys, `auto_vacuum=INCREMENTAL`. |

None of those three can be reached with `ALTER TABLE`: `AUTOINCREMENT` and
`REFERENCES` live in the table's declared SQL, and `auto_vacuum` is only
honoured on a database with no tables yet or after a full `VACUUM`. So the
upgrade is a **rebuild**: a sibling temp file gets the v2 schema and the v2 file
pragmas, the data is copied in (`ATTACH` + `INSERT … SELECT`), the file is
fsynced and renamed over the original, and the stale `-wal`/`-shm` are removed.
One pass delivers all three — a rebuild *is* the vacuum. Free space is checked
first; the peak requirement is one extra copy of the file. Measured: a 152 MB
database rebuilds in **0.32 s**, so the 1.86 GB reference index is a few
seconds of one-time boot delay, logged at `warn`.

A v1 file may already hold orphan rows — that is the leak v2 exists to stop. They
cannot be copied into a database that enforces the constraint, so the rebuild
filters them out and logs how many it dropped. Nothing loses visible data: those
rows were already unreachable through `collections`.

**When a collection is deleted mid-upsert**, `UpsertChunks` now returns an error
wrapping `ErrCollectionDeleted` instead of leaking rows. It does not retry: the
stale id is dropped from the cache so a later call resolves it afresh, but
whether to re-create the collection is the caller's decision. Rows committed by
earlier batches of the same call are already gone — cascaded away with the
`collections` row.

**Why chunk text lives in its own table.** Storing it duplicates `chunks_fts`
on disk, deliberately: it keeps the package self-contained and
`SearchResult.Content` unchanged with no cross-database wiring. But it cannot
live in `vectors`. A multi-kilobyte `TEXT` column pushes a row past SQLite's
local-payload limit, and SQLite then keeps only ~1 kB of the row in the table
page and spills the rest — *including the embedding* — into an overflow chain,
roughly doubling the pages a scan touches. Kept apart, a 768-dim `vectors` row
is ~3.2 kB and two of them share an 8 KiB page. Content is read only for the K
winners of a search: one extra lookup per result.

**Why the scan reads a second copy of every vector.** The paragraph above stops
being true once the model is bigger than 1024 dimensions. A 2048-dim float32
embedding is 8192 bytes on its own, past the 8157-byte local-payload limit, so
every `vectors` row spills into an overflow page and the scan is back to the
layout splitting out the content was meant to avoid. Measured with `dbstat`
over 400 rows, bytes a full scan must read per vector:

| dimensions | representation | leaf | overflow | bytes/vector |
|---|---|---|---|---|
| 768 | float32 | 200 | 0 | 4096 |
| 1024 | float32 | 400 | 0 | 8192 |
| 2048 | float32 | 50 | 400 | 9216 |
| 768 | int8 | 40 | 0 | 819 |
| 1024 | int8 | 58 | 0 | 1188 |
| 2048 | int8 | 134 | 0 | 2744 |

The pathological line is 1024, not 2048: nothing overflows there and the scan
still reads 8192 bytes to obtain 4096, because two 4.1 kB rows cannot share an
8 KiB page. Halving `output_dimension` to save time bought half the vector
quality for 89% of the I/O.

`vectors_q8` removes the whole step function by scanning one byte per component
instead of four. `TestScanPackingEfficiency` and `TestScanBytesPerVectorBudget`
assert those numbers — as pages, not milliseconds, so they mean the same thing
in CI, on a laptop, and on the production box.

## Search

```
SELECT doc_id, scale, embedding FROM vectors_q8 INDEXED BY idx_q8_coll
 WHERE collection_id = ?  [AND language = ?]
```

Rows stream past an integer dot product into a top-K min-heap that rejects a
losing row with one comparison. The heap is wider than the caller's limit — the
int8 ranking chooses a shortlist, it does not produce the answer. The shortlist
is then rescored against the exact float32 vectors in `vectors`, and metadata
and chunk text are fetched for the winners only.

**What the approximation costs.** Measured on 60k vectors of the load-test
fixture's largest collection (`ziglang/zig`, voyage-code-3 @2048) against 50
real query-side embeddings, recall of the exact float32 top-K:

| shortlist | k=10 | k=20 |
|---|---|---|
| 20 | 0.998 | 0.994 |
| 40 | 0.998 | 0.999 |
| **60** | **1.000** | **1.000** |
| 200 | 1.000 | 1.000 |

Without rescoring at all the int8 ranking alone gives 0.994 at both k — the
quantisation misorders near-ties, it does not lose the documents, which is
exactly why re-reading a few dozen exact vectors recovers all of them.
`q8Shortlist` therefore uses a floor of 64 and 4x the limit above it. Scan CPU
in the same run: 127 ms per query float32, 42 ms int8 (3.0x).

Scores returned to callers are always the exact cosine, never the int8
estimate. That is load-bearing beyond cosmetics: `min_score` thresholds on it,
the workspace fan-out normalises across projects with it, and hybrid search
blends it with BM25 — an approximate score would move results between projects
in a way no single-project test would catch. `TestSearchScoresAreExact` pins it.

**Building and rebuilding the copy.** Writes maintain `vectors_q8` in the same
transaction as `vectors`, so a collection created by this code is complete by
construction, and `q8_state` records that at creation — the readiness check is
a primary-key lookup, never a `COUNT`. A store written before the table existed
is converted by a background pass at open, largest collection first, in 2000-row
transactions at a 50% duty cycle; until a collection is covered its searches
take the float32 scan, which is correct and simply slower. Nothing is ever
marked complete before it is: the flag is written in the same transaction as
the batch that proves it. Set `CIX_VECTOR_SCAN_QUANT=false` to opt out — the
copy is roughly a quarter of the float32 bytes on top of an already large
store, so an operator short of disk needs a way to say no. Turning it off also
withdraws the completion flag from anything written while it is off, so turning
it back on rebuilds rather than trusting a stale copy.

`INDEXED BY` is not an optimisation hint, it is a guarantee, and *which* index
matters. Measured on the real index, scanning its largest (74k-row) collection:

| driven by | | |
|---|---|---|
| `idx_vec_coll` | **137 ms** | keys are `(collection_id, rowid)` |
| `idx_vec_coll_file` | 244 ms | keys are `(collection_id, file_path, rowid)` |
| no index | 267 ms | walks all 312k rows and discards 76% of them |

Delete-by-file followed by reinsert — what the file watcher does on every save
— appends the new rows at the end of the table, so a collection's rows stop
being contiguous and a plain table scan degrades without bound. Both indexes
fix that (they visit only the collection's own rows), but SQLite appends the
rowid to every index key, so `idx_vec_coll` also hands the rows back in *table*
order and the row lookups stay sequential. The file-path index scatters them
across the collection's whole rowid span, for 1.8x the time.
`TestScanUsesCollectionIndex` pins the plan.

The metadata filter (`where`) mirrors chromem's semantics exactly, including
the two odd cases: an unknown key with a non-empty value matches nothing, and
an unknown key with an empty value matches everything.

`TestQ8ScanUsesCollectionIndex` pins the same guarantee for the compact table:
`idx_q8_coll`'s keys are `(collection_id, rowid)` for the same reason, and the
language filter must not change the driving index.

**Concurrency.** One scan per query, and a process-wide semaphore caps
concurrent scans at `NumCPU`. Splitting a single query across workers was
measured to buy nothing in the low-memory configuration (109 ms at 1 worker vs
110 ms at 4) — the scan is bound by per-row streaming cost, not arithmetic.
What needs bounding is fan-out: thirty concurrent agent queries must queue on a
handful of scanners rather than spawn a hundred threads and a hundred page
caches.

## Pragmas

Applied explicitly on each new connection, never through `_pragma=` DSN
parameters: **`modernc.org/sqlite` sorts DSN pragmas lexicographically** rather
than applying them in the order written, so on a fresh database
`journal_mode(WAL)` always runs before `page_size(...)`, the first WAL
statement materialises the file, and the page size is silently ignored.

| pragma | value | why |
|---|---|---|
| `page_size` | 8192 (fresh databases only) | 4 KiB wastes ~23% of every page (one row per page). 16 KiB is one byte-class over `modernc.org/memory`'s slab limit, so every page buffer costs an `mmap` + `munmap` — measured at 24% of all CPU during a scan. |
| `journal_mode` | WAL | Readers do not block the indexer. |
| `synchronous` | NORMAL | |
| `busy_timeout` | 10 s | |
| `journal_size_limit` | 64 MB | A checkpoint rewinds the WAL but by default leaves the FILE at its high-water mark forever. The legacy import commits a whole collection at once — measured a permanent 159 MB `-wal` beside a 158 MB database — and that sidecar is counted in the "Vector store" row of the Resources screen. With a limit the checkpoint truncates it back. Steady-state indexing commits every 500 chunks and never reaches the cap. |
| `cache_size` | driver default (2 MB) | Measured: raising it buys no latency (the working set dwarfs any realistic cache) and it is **per connection**, so it multiplies resident memory. |
| `mmap_size` | off | Opt-in, see below. |
| `foreign_keys` | ON | Per **connection**, and off by default in SQLite — a declared `REFERENCES` clause that is never enabled is just a comment. It is what stops an upsert holding a stale collection id from committing rows no `collections` row joins to. |
| `auto_vacuum` | INCREMENTAL (set at creation) | The Resources screen reports bytes reclaimed when a collection is deleted; without this the pages only reach the freelist, the file never shrinks, and `df` never confirms the claim. INCREMENTAL rather than FULL because the reclaim is driven explicitly (`PRAGMA incremental_vacuum` after a collection delete) and never on the watcher's delete-and-reinsert path — measured there: 100 delete+reinsert cycles over 72k rows grew the file by 3.8 MB and ended with an empty freelist, i.e. free page recycling already handles it. |

Idle pooled connections are closed after 30 seconds. This is the mechanism that
makes idle memory collapse: SQLite's page cache lives in `modernc.org/memory`
arenas obtained by raw `mmap`, outside the Go heap, so `runtime.GC()` cannot
return it — closing the connection can.

## Environment variables

| variable | default | meaning |
|---|---|---|
| `CIX_VECTORS_DIR` | sibling of `CIX_CHROMA_PERSIST_DIR`, i.e. `<...>/vectors` | Container for the per-namespace databases. The default follows the chroma container so a deployment that only overrides `CIX_CHROMA_PERSIST_DIR` still lands its vectors on the same persistent volume. |
| `CIX_VECTOR_MMAP_SIZE` | `0` (off) | `PRAGMA mmap_size` in bytes. Cuts search latency by roughly 40% and costs resident memory: every connection maps the database file and mapped pages count in RSS (measured 1.0–3.2 GB under fan-out). The pages are clean and instantly reclaimable, so this is a reasonable trade on a memory-rich host — and not compatible with a tight memory ceiling. |
| `CIX_CHROMA_PERSIST_DIR` | `<data>/chroma` | Still read: it is where the legacy gob files live and where the one-time import reads from. |

## Migration from chromem-go

On startup, for the ACTIVE namespace only, the store imports every collection
of the matching chromem directory that is not already recorded in
`migration_state`. Behaviour:

- **Fresh install** — no chromem directory, nothing logged, nothing done.
- **Existing install** — one transaction per collection, which also writes the
  collection's `migration_state` row. An interrupted import therefore redoes
  exactly the collection it was in the middle of and never duplicates a
  finished one. Progress is logged at `warn` every 10 collections:
  `migrating vector store collections=12/47 docs=…`. Warn, not info, because
  production runs at warn level and the HTTP listener only comes up once the
  store is open — at info the operator watches a server that answers nothing
  and says nothing for the whole import, which has repeatedly been read as
  "it is down" and answered with a restart.
- **Streamed, not buffered** — decode workers feed a channel and the writer
  drains it into the transaction 2000 documents at a time. Decoding a whole
  collection first cost ~9 kB of live heap per document (268 MB peak for a 30k
  document collection, ~1.8 GB for a 200k monorepo): the first boot after the
  upgrade demanded exactly the memory this store exists to give back. Peak heap
  now scales with the batch, not with the collection.
- **Reference figures** — 312,334 documents across 47 collections imported in
  17 s, producing a 1.86 GB database from a 2.5 GB gob tree.
- **Before starting**, free space is checked at 0.9x the gob tree: 0.74x is the
  measured database (1.86 GB from 2.5 GB) and the rest is the WAL. Every page a
  transaction touches sits in the WAL until it commits, so a per-collection
  transaction still means a WAL of roughly one collection whatever the write
  batch is — `journal_size_limit` truncates it afterwards, but the import has to
  fit through the peak. An obviously impossible import fails the boot with a
  clear message rather than half-writing.
- **Nothing under the chromem directory is ever modified or removed.** It is
  the rollback path: downgrading to a build that uses chromem finds its data
  exactly as it left it.
- A collection deleted afterwards (an orphan reclaimed from the Resources
  screen) keeps its `migration_state` row, so the next boot does not re-import
  what an admin just removed.
- Switching the embedding provider or model at runtime opens the new
  namespace's database and imports that namespace's gob files the same way.

The server does not link chromem-go at all. The importer decodes the gob files
through local mirror structs; chromem is a test-only dependency, kept because a
fixture written by the real thing is the only evidence that those structs still
match what is on disk.

### Reclaiming the legacy files

Two categories on the admin **Resources** screen cover the gob tree, and the
split matters:

- **Abandoned provider namespaces** (`stale_namespaces`) takes the namespaces
  of models that are no longer in use, in either tree. The ACTIVE namespace is
  deliberately protected there, in both trees — its gob files are the rollback
  path.
- **Legacy chromem data** (`legacy_chromem`) is how that protection is
  released, on purpose. It offers the active namespace's chromem directory —
  2.5 GB on the reference install — once every collection in it is provably in
  `vectors.db`.

The rules of the second one:

- A namespace is listed only when it is **fully imported**: every collection
  directory in the tree has a `migration_state` row in that namespace's
  database. Anything less — a migration still running, a directory the importer
  could not read, a missing or unreadable `vectors.db` — and the namespace is
  not offered at all (not offered-and-disabled: a disabled row would still
  advertise gigabytes that are not garbage yet). The analysis warnings say
  which case it was.
- It is **never pre-selected**, and the description states plainly that this is
  irreversible and gives up the ability to roll back to a pre-SQLite server
  version.
- Disk only. Nothing about the legacy tree is in memory — the new store loads
  nothing at open — so `estimated_ram_bytes` stays zero. Nothing sets that field
  any more at all; it is deprecated and omitted from the wire, kept only so an
  older dashboard build does not break on it.
- The full-migration check is repeated immediately before the delete. Between
  the analysis and the confirm, an embedding-model switch can reopen this
  namespace and start a fresh import; the item is then skipped rather than
  deleted.
- A running index or clone job does **not** hold the category back, unlike
  orphaned collections. This binary never writes the gob tree — indexing writes
  `vectors.db` and nothing else — so a job cannot make these files matter
  again. The only thing that can is an in-flight import, which the re-check
  asks about directly.

Deleting the tree is one recursive directory removal and changes nothing else:
`migration_state` rows are kept, so the next boot finds no legacy directory,
imports nothing, and starts normally.
