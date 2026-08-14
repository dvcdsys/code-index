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
CREATE TABLE collections (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);

CREATE TABLE vectors (
  collection_id INTEGER NOT NULL,
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
  collection_id INTEGER NOT NULL,
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

**Why chunk text lives in its own table.** Storing it duplicates `chunks_fts`
on disk, deliberately: it keeps the package self-contained and
`SearchResult.Content` unchanged with no cross-database wiring. But it cannot
live in `vectors`. A multi-kilobyte `TEXT` column pushes a row past SQLite's
local-payload limit, and SQLite then keeps only ~1 kB of the row in the table
page and spills the rest — *including the embedding* — into an overflow chain,
roughly doubling the pages a scan touches. Kept apart, a `vectors` row is
~3.2 kB and two of them share an 8 KiB page. Content is read only for the K
winners of a search: one extra lookup per result.

## Search

```
SELECT rowid, embedding FROM vectors INDEXED BY idx_vec_coll
 WHERE collection_id = ?  [AND <metadata filters>]
```

Rows stream past a dot product (embeddings are stored L2-normalised, so cosine
similarity *is* the dot product) into a top-K min-heap that rejects a losing
row with one comparison. Metadata and chunk text are fetched afterwards, for
the winners only.

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
| `cache_size` | driver default (2 MB) | Measured: raising it buys no latency (the working set dwarfs any realistic cache) and it is **per connection**, so it multiplies resident memory. |
| `mmap_size` | off | Opt-in, see below. |
| `auto_vacuum` | off | Measured: 100 delete+reinsert cycles over 72k rows grew the file by 3.8 MB and ended with an empty freelist. Pages are recycled by the next insert. |

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
  finished one. Progress is logged at `info` (the default level) every 10
  collections: `migrating vector store collections=12/47 docs=…`.
- **Reference figures** — 312,334 documents across 47 collections imported in
  17 s, producing a 1.86 GB database from a 2.5 GB gob tree.
- **Before starting**, free space is checked (the database comes out at roughly
  half the size of the gob tree); an obviously impossible import fails the boot
  with a clear message rather than half-writing.
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

Not automated yet. Abandoned namespaces of both trees show up under **Abandoned
provider namespaces** on the Resources screen, but the ACTIVE namespace's gob
files are deliberately protected there — they are the rollback path. Removing
them is a manual `rm -rf <data>/chroma/<kind>/<model-slug>` once the new store
has proven itself.
