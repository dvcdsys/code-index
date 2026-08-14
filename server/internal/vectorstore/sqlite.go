package vectorstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DBFileName is the SQLite file inside a vector-store namespace directory.
//
// One file per namespace (embedding provider + model), matching the directory
// namespacing the chromem layout used: the caller passes the namespace
// directory and the store owns everything inside it. A directory rather than a
// bare "<model>.db" because a live SQLite database is three files (.db, -wal,
// -shm); keeping them in their own directory means BaseDir() is still a
// directory, which is what the maintenance surface (namespace scanning, size
// accounting, "active namespace" protection) has always assumed.
const DBFileName = "vectors.db"

// pageSize is fixed at 8 KiB for every database this package creates.
//
// Two measured constraints meet here (see doc/VECTORSTORE.md):
//
//   - 4 KiB pages waste ~23% of every page: a 768-dim row is ~3.15 kB of
//     payload, so exactly one row fits per page.
//   - 16 KiB pages are one byte-class over modernc.org/memory's maxSlotSize
//     (16384). Every page-cache buffer above that line is served by a
//     dedicated mmap and released with munmap — measured at 24% of all CPU
//     during a scan. 8 KiB buffers stay in the slab free lists.
//
// It MUST be applied before the first write to a fresh database: the first
// statement that materialises page 1 freezes the page size for the life of the
// file.
const pageSize = 8192

// busyTimeoutMS bounds how long a writer waits for the WAL write lock before
// giving up. Indexing commits in small transactions and the file watcher can
// delete-by-file concurrently, so contention is short-lived; 10s is generous
// enough that a legitimate queue never surfaces as SQLITE_BUSY.
const busyTimeoutMS = 10000

// idleConnTimeout is how long an idle pooled connection is kept.
//
// This is the mechanism that makes idle RSS collapse. SQLite's per-connection
// page cache lives in modernc.org/memory arenas obtained by raw mmap — it is
// invisible to the Go heap and runtime.GC() cannot return it. Closing the
// connection is what frees it, so the pool is allowed to shrink back to
// nothing shortly after a burst of queries.
const idleConnTimeout = 30 * time.Second

// schemaSQL is the whole schema. Two tables, deliberately.
//
// `vectors` holds only what a scan reads: the metadata columns the `where`
// filter can constrain and the embedding itself. A row is ~3.2 kB, which fits
// inside an 8 KiB table-leaf cell (the local-payload limit is usable-35), so
// the scan reads two rows per page and never follows an overflow chain.
//
// `vector_contents` holds the chunk text. It is stored (duplicating chunks_fts
// on disk) so SearchResult.Content behaves identically with no cross-database
// wiring — disk is cheap, RAM was the problem. It lives in its own table
// because putting a multi-kilobyte TEXT column in `vectors` would push every
// row past the local-payload limit, and SQLite then keeps only ~1 kB of the
// row local and spills the REST — including the embedding — into an overflow
// chain. That would roughly double the pages a scan touches. Content is read
// only for the K winners, so it costs one extra btree lookup per result.
//
// idx_vec_coll_file serves both delete-by-file and, via its collection_id
// prefix, the scan (see scanSQL): walking the index visits only the
// collection's own rows regardless of how fragmented the table has become
// after watcher churn.
//
// migration_state records which legacy chromem collections have been imported.
// A row here means "this collection has been dealt with" — it is deliberately
// NOT removed when a collection is deleted, or the next boot would re-import
// the collection an admin just reclaimed.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS collections (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS vectors (
  collection_id INTEGER NOT NULL,
  doc_id        TEXT NOT NULL,
  file_path     TEXT NOT NULL,
  start_line    INTEGER NOT NULL,
  end_line      INTEGER NOT NULL,
  chunk_type    TEXT NOT NULL DEFAULT '',
  symbol_name   TEXT NOT NULL DEFAULT '',
  language      TEXT NOT NULL DEFAULT '',
  embedding     BLOB NOT NULL,
  PRIMARY KEY (collection_id, doc_id)
);
CREATE INDEX IF NOT EXISTS idx_vec_coll_file ON vectors(collection_id, file_path);
CREATE TABLE IF NOT EXISTS vector_contents (
  collection_id INTEGER NOT NULL,
  doc_id        TEXT NOT NULL,
  content       TEXT NOT NULL,
  PRIMARY KEY (collection_id, doc_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS migration_state (
  collection_name TEXT PRIMARY KEY,
  migrated_at     TEXT NOT NULL,
  docs            INTEGER NOT NULL
);
`

// pragmaConnector applies connection-scoped pragmas to every new connection.
//
// Why not `_pragma=` DSN parameters: modernc.org/sqlite sorts DSN pragmas
// LEXICOGRAPHICALLY instead of applying them in the order written. On a fresh
// database journal_mode(WAL) therefore always runs before page_size(8192), the
// first WAL statement materialises the file, and the page size is silently
// ignored. Applying them by hand — in an order we control, on a connection we
// hold — is the only way to be sure.
type pragmaConnector struct {
	drv     driver.Driver
	dsn     string
	pragmas []string
}

func (c *pragmaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("vectorstore: sqlite driver connection does not implement ExecerContext")
	}
	for _, p := range c.pragmas {
		if _, err := ex.ExecContext(ctx, p, nil); err != nil {
			conn.Close()
			return nil, fmt.Errorf("vectorstore: apply %q: %w", p, err)
		}
	}
	return conn, nil
}

func (c *pragmaConnector) Driver() driver.Driver { return c.drv }

// sqliteDriver returns the registered modernc driver. sql.Open is lazy — it
// opens no connection — so this is just a handle to the driver singleton,
// which the package exposes no other way.
func sqliteDriver() (driver.Driver, error) {
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer probe.Close()
	return probe.Driver(), nil
}

// openDB opens (creating if needed) the database at path and returns a pool
// with the pragmas applied on every connection.
func openDB(path string, mmapBytes int64) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("vectorstore: create %s: %w", filepath.Dir(path), err)
	}
	fresh := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fresh = true
	}

	drv, err := sqliteDriver()
	if err != nil {
		return nil, err
	}

	// cache_size is left at the driver default (2 MB). Measured: raising it
	// buys no latency for this workload — a collection's working set is far
	// larger than any realistic cache, so every query streams it regardless —
	// while it multiplies RSS by the number of open connections.
	pragmas := []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS),
		"PRAGMA synchronous=NORMAL",
	}
	if mmapBytes > 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA mmap_size=%d", mmapBytes))
	}

	db := sql.OpenDB(&pragmaConnector{drv: drv, dsn: "file:" + path, pragmas: pragmas})

	// One connection per core is plenty: a query runs a single scan and the
	// cap that actually matters is the global scan semaphore. Idle
	// connections are released so their page caches are returned to the OS.
	maxConns := runtime.NumCPU() + 2
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(idleConnTimeout)

	if err := initDatabase(db, fresh); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// initDatabase sets the file-scoped pragmas and creates the schema. page_size
// and journal_mode are properties of the FILE, not of a connection, so they
// are applied once, on one dedicated connection, before anything writes.
func initDatabase(db *sql.DB, fresh bool) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("vectorstore: connect: %w", err)
	}
	defer conn.Close()

	if fresh {
		// Must precede the first statement that materialises page 1.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA page_size=%d", pageSize)); err != nil {
			return fmt.Errorf("vectorstore: set page_size: %w", err)
		}
	}
	var journal string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return fmt.Errorf("vectorstore: set journal_mode: %w", err)
	}
	if fresh {
		var got int
		if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&got); err != nil {
			return fmt.Errorf("vectorstore: read page_size: %w", err)
		}
		if got != pageSize {
			return fmt.Errorf("vectorstore: page_size is %d, wanted %d", got, pageSize)
		}
	}
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("vectorstore: create schema: %w", err)
	}
	return nil
}
