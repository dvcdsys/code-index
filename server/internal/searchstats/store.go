// Package searchstats records how much each project is searched, and which
// files keep coming back in the results.
//
// It lives in its OWN SQLite file, deliberately. The reasons are specific to
// this server rather than general tidiness:
//
//  1. Search must keep serving during a database compaction. That is not an
//     aspiration — it is encoded in httpapi.readOnlyPostSuffixes, where
//     `/search` is listed as a POST that may proceed while writes are frozen.
//     A counter written into projects.db would either have to be exempted from
//     the freeze (writing into a snapshot that is about to be discarded) or
//     block on the compactor's held write transaction for the full
//     busy_timeout — measured at 5.06 s in dbmaint/freeze.go — while holding
//     one of the eight connections that pool allows. Eight of those and reads
//     stall too. The same hazard already forced Sessions.Touch to be skipped
//     while frozen (httpapi/middleware.go); this package sidesteps it entirely
//     by not being in that file.
//
//  2. The indexer writes chunks_fts and chunks_meta per file, in a transaction
//     per file. A full reindex holds the write lock in bursts, and analytics
//     writes — which have nothing to do with indexing — would queue behind it.
//
//  3. Counters are high-churn and the bucket tables are delete-heavy by
//     design. In projects.db that churn inflates the freelist, and the
//     freelist is what drives the dashboard's "time to compact" advice
//     (dbmaint/stats.go). Analytics would start recommending maintenance
//     windows for the main database. Here it is a file that can be vacuumed on
//     its own, or deleted outright — these are derived numbers, and losing
//     them costs a chart, never an answer.
//
// The cost of the split is that these tables cannot be JOINed against
// `projects`. That turned out not to matter: the caller has to resolve which
// projects the requester is allowed to see from the main database anyway
// (access.AccessibleProjectHostPaths), exactly as workspace search does, so a
// query here is always already parameterised by a set of project paths.
//
// # Two tiers
//
// Totals and buckets are separate tables rather than one bucketed table that
// gets summed, because the two have opposite retention needs and folding them
// together would mean choosing which one to break:
//
//   - search_totals / search_file_totals are cumulative and are NEVER pruned.
//     They are what "how much is this project searched" means. Their size is
//     bounded by how many distinct files have ever appeared in a result, not
//     by how long the server has been running — a quiet week adds nothing.
//   - search_buckets / search_file_buckets carry a rolling window at
//     bucketSeconds resolution and ARE pruned. They answer "what did the last
//     few days look like", which needs old rows gone.
//
// Deriving the totals by summing the buckets would mean the totals silently
// drop every time the window slides. Keeping the buckets forever would mean
// unbounded growth. Two tables, two retention policies, no compromise.
package searchstats

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DBFileName is the SQLite file this package owns. It is placed alongside
// projects.db (see PathBeside) so that a deployment which mounts a volume for
// the system database gets this one inside the same volume without having to
// configure a second path.
const DBFileName = "searchstats.db"

// BucketSeconds is the resolution of the windowed tier: 30 minutes.
//
// Chosen as the coarsest granularity that still shows the shape of a working
// day. It also bounds the window tier's row count directly — with a 7-day
// retention no key can have more than 7*24*2 = 336 rows, whatever the traffic.
const BucketSeconds = 1800

// WindowRetention is how far back the bucket tier keeps rows.
//
// Seven days rather than "forever" because the buckets exist to show recent
// shape, and the cumulative question they would otherwise be asked to answer
// is already answered exactly, and for all time, by the totals tables.
const WindowRetention = 7 * 24 * time.Hour

// busyTimeoutMS bounds how long a writer waits for the WAL write lock.
//
// Only two writers exist in this file — the recorder's flush and the prune
// that follows it, both on the same goroutine — so contention is effectively
// nil and this is a backstop rather than a tuning knob.
const busyTimeoutMS = 5000

// schemaVersion is stamped into PRAGMA user_version. It exists so that a
// future shape change can be detected; there is no upgrader yet, because
// there is nothing here worth migrating — a version this build does not
// recognise is handled by Open refusing rather than by rewriting data.
const schemaVersion = 1

// schemaSQL is the whole database.
//
// project_path is interned into projects_seen rather than repeated in every
// row. The strings are long — a local project's path is
// `local:{machine_id}:{abs_path}`, comfortably 80 bytes — and in a WITHOUT
// ROWID table the primary key IS the row, so repeating it would put those 80
// bytes into every one of potentially hundreds of thousands of file rows and
// into every index key a scan walks. An INTEGER costs 1-2 bytes instead.
//
// Interning buys a second thing: ON DELETE CASCADE. Discarding everything
// recorded for a project that has been removed is one DELETE against
// projects_seen, rather than four DELETEs that have to agree on the path
// spelling.
//
// AUTOINCREMENT on projects_seen.id is load-bearing, and for the same reason
// it is load-bearing in the vector store. A plain INTEGER PRIMARY KEY is the
// rowid, and SQLite hands the largest free rowid to the next inserted row —
// so deleting the highest-numbered project would give its id to the next
// project recorded. The recorder CACHES these ids in memory across exactly
// that window (see recorder.go), and a reused id would silently attribute one
// project's counters to another. AUTOINCREMENT makes ids monotonic, so a
// stale cached id can only ever point at nothing — and with foreign_keys ON,
// an insert carrying one fails loudly instead of committing an orphan.
//
// Every counter table is WITHOUT ROWID: each is a pure key-to-counter map
// addressed by its full primary key, so the rowid indirection would be a
// second btree holding nothing the first does not.
//
// `bucket` leads the primary key of both window tables. Pruning is
// `DELETE ... WHERE bucket < ?`, which against this key order is a contiguous
// range at the front of the btree rather than a full scan.
//
// That key order is wrong for every READ, though, and both window tables
// therefore carry a (project_id, bucket) index. Without one, EXPLAIN QUERY PLAN
// reports `SEARCH ... USING PRIMARY KEY (bucket>?)`: a per-project query walks
// the whole retained range across every project and filters afterwards.
// Retention caps how far back that range goes, not how wide it is — and one
// dashboard render at limit=25 issues about twenty-seven of these (the page,
// the count/footer wrapper of the same statement, and one top-files query per
// row), on a thirty-second refresh. search_file_buckets is the larger table by
// a wide margin, so it is the one that must not be missed.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS projects_seen (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_path TEXT    NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS search_totals (
  project_id INTEGER NOT NULL REFERENCES projects_seen(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  queries    INTEGER NOT NULL DEFAULT 0,
  results    INTEGER NOT NULL DEFAULT 0,
  last_seen  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, kind)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS search_file_totals (
  project_id INTEGER NOT NULL REFERENCES projects_seen(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  file_path  TEXT    NOT NULL,
  hits       INTEGER NOT NULL DEFAULT 0,
  last_seen  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, kind, file_path)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS search_buckets (
  bucket     INTEGER NOT NULL,
  project_id INTEGER NOT NULL REFERENCES projects_seen(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  queries    INTEGER NOT NULL DEFAULT 0,
  results    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, project_id, kind)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_buckets_project ON search_buckets(project_id, bucket);

CREATE TABLE IF NOT EXISTS search_file_buckets (
  bucket     INTEGER NOT NULL,
  project_id INTEGER NOT NULL REFERENCES projects_seen(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  file_path  TEXT    NOT NULL,
  hits       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, project_id, kind, file_path)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_file_buckets_project ON search_file_buckets(project_id, bucket);
`

// PathBeside returns the searchstats database path for a given system
// database path — the same directory, so one mounted volume covers both.
//
// An empty or in-memory system path yields an in-memory stats database:
// tests that ask for a throwaway system DB should not have a stats file
// appear in the working directory as a side effect.
func PathBeside(sqlitePath string) string {
	if sqlitePath == "" || sqlitePath == ":memory:" {
		return ":memory:"
	}
	return filepath.Join(filepath.Dir(sqlitePath), DBFileName)
}

// Store is the open stats database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if necessary) the stats database at path.
//
// Unlike the system database this one is disposable, and Open leans on that:
// a file whose user_version is from a FUTURE build is refused rather than
// touched, and the operator's remedy is to delete it. Refusing beats both
// alternatives — writing into a shape we do not understand corrupts somebody
// else's data, and rewriting it silently discards theirs.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("searchstats: create %s: %w", filepath.Dir(path), err)
		}
	}
	if err := seedReclaimMode(path); err != nil {
		return nil, err
	}

	v := url.Values{}
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "foreign_keys(ON)")
	v.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	// synchronous=NORMAL: on a WAL database this can lose the last commits on
	// a power cut but cannot corrupt the file. For counters that is the right
	// trade — the flush runs every few seconds and losing one interval of
	// analytics is not an event anybody needs to hear about.
	v.Add("_pragma", "synchronous(NORMAL)")

	dsn := path + "?" + v.Encode()
	if path != ":memory:" {
		dsn = "file:" + dsn
	}

	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("searchstats: open %s: %w", path, err)
	}
	// modernc holds pragmas per connection, and an in-memory database is a
	// different database on every connection — so a single connection is the
	// only correct pool for one.
	if path == ":memory:" {
		sdb.SetMaxOpenConns(1)
	} else {
		// The writer is a single goroutine; readers are dashboard requests,
		// which are rare and short. Four is generous.
		sdb.SetMaxOpenConns(4)
		sdb.SetMaxIdleConns(2)
	}

	if err := sdb.Ping(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("searchstats: ping %s: %w", path, err)
	}

	var version int
	if err := sdb.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("searchstats: read user_version of %s: %w", path, err)
	}
	if version > schemaVersion {
		_ = sdb.Close()
		return nil, fmt.Errorf(
			"searchstats: %s was written by a newer build (schema v%d, this build understands v%d) — "+
				"delete the file to start collecting again, nothing else depends on it",
			path, version, schemaVersion)
	}

	if _, err := sdb.Exec(schemaSQL); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("searchstats: create schema in %s: %w", path, err)
	}
	if version < schemaVersion {
		if _, err := sdb.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
			_ = sdb.Close()
			return nil, fmt.Errorf("searchstats: stamp schema version on %s: %w", path, err)
		}
	}

	return &Store{db: sdb, path: path}, nil
}

// seedReclaimMode puts a database this build is CREATING into incremental
// auto-vacuum mode, and leaves an existing file alone.
//
// It needs its own connection, before the pool, because auto_vacuum can only be
// moved off `none` while the header is unwritten — and setting journal_mode=WAL
// writes it. The main pool carries journal_mode in its DSN, so a pragma issued
// there is silently ignored however early it appears in the statement list.
// This is the same dance internal/db performs for the system database, and for
// the same reason.
//
// The mode matters more here than it does there. The bucket tables are
// delete-heavy by design, and without a reclaim mode their pages go onto the
// free list and stay there — the file would only ever grow. That is precisely
// the free-list inflation this package cites as a reason not to live inside
// projects.db, so leaving it unfixed here would be an odd place to land.
// Incremental rather than full so the reclaim happens on Prune's schedule
// rather than inside every DELETE.
//
// An error is not fatal to the caller's intent — the mode is a performance
// property — but it is returned rather than swallowed, because the only way to
// reach it is a filesystem problem that Open is about to hit anyway.
func seedReclaimMode(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return nil // an existing database keeps whatever mode it has
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("searchstats: stat %s: %w", path, err)
	}

	v := url.Values{}
	v.Add("_pragma", "auto_vacuum(INCREMENTAL)")
	v.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	seed, err := sql.Open("sqlite", "file:"+path+"?"+v.Encode())
	if err != nil {
		return fmt.Errorf("searchstats: open %s to set its reclaim mode: %w", path, err)
	}
	defer seed.Close()
	seed.SetMaxOpenConns(1)
	// Setting the journal mode is what forces the header — and with it the
	// reclaim mode — to disk while the database is still empty.
	if _, err := seed.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("searchstats: initialise %s: %w", path, err)
	}
	return nil
}

// DB exposes the pool for tests and for assertions that have to look past the
// access-scoped read API.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// BucketOf floors a time to the start of its bucket, as a Unix second.
func BucketOf(t time.Time) int64 {
	return t.Unix() - t.Unix()%BucketSeconds
}

// Prune deletes window rows older than the retention horizon. The totals
// tables are untouched by design — see the package comment.
//
// Returns the number of rows removed across both window tables.
func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	cutoff := BucketOf(now.Add(-WindowRetention))
	var removed int64
	for _, table := range []string{"search_file_buckets", "search_buckets"} {
		res, err := s.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE bucket < ?`, table), cutoff)
		if err != nil {
			return removed, fmt.Errorf("searchstats: prune %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	// Incremental auto-vacuum puts the emptied pages on the free list; this is
	// what hands them back to the filesystem. Without it the reclaim mode set
	// at creation would buy nothing and the file would only ever grow.
	//
	// Unbounded on purpose — the pages released here are exactly the ones this
	// prune just freed, and capping the step would leave a remainder that the
	// next run has to carry. Not fatal: failing to shrink a statistics file is
	// not a reason to report the prune as failed when the rows are gone.
	if removed > 0 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
			return removed, fmt.Errorf("searchstats: reclaim pruned pages: %w", err)
		}
	}
	return removed, nil
}

// Forget discards everything recorded for a project. One statement, because
// projects_seen is the parent of every counter table via ON DELETE CASCADE.
//
// Called when a project is deleted. It is not an error for the project to be
// unknown here — a project nobody ever searched has no row.
func (s *Store) Forget(ctx context.Context, projectPath string) error {
	if s == nil || projectPath == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM projects_seen WHERE project_path = ?`, projectPath); err != nil {
		return fmt.Errorf("searchstats: forget %s: %w", projectPath, err)
	}
	return nil
}

// Reset empties every table. Exposed so an admin can clear the numbers
// without stopping the server or hunting for the file.
func (s *Store) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("searchstats: reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// projects_seen last: the cascade does the rest, but being explicit keeps
	// this correct even if a future table forgets its foreign key.
	for _, table := range []string{
		"search_file_buckets", "search_buckets",
		"search_file_totals", "search_totals",
		"projects_seen",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return fmt.Errorf("searchstats: reset %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// ForgetAllExcept discards every project's counters except those named, and
// reports how many projects it dropped.
//
// This is the safety net under Forget. Deleting a project calls Forget from the
// HTTP handler, best-effort and after the delete has already committed — the two
// databases have no foreign key between them, so failing the delete over a
// counter is not on the table. That leaves a hole: if the call fails, or the
// process dies between the two, the counters survive their project forever,
// because search_totals is never pruned. The rows are invisible in the API
// (every read is scoped to projects the caller can see, and a deleted project is
// in nobody's set), so nothing would ever surface them.
//
// Sweeping on the prune schedule closes it: an orphan lives at most until the
// next run.
//
// An EMPTY live set is treated as "don't know" and does nothing. A server with
// genuinely zero projects does exist, and for it the sweep is technically
// correct — but so does a caller whose query failed and returned nothing, and
// between silently wiping every counter and keeping orphans for one more day,
// the orphans are the better outcome.
func (s *Store) ForgetAllExcept(ctx context.Context, live []string) (int64, error) {
	if s == nil || len(live) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(live))
	for _, p := range live {
		args = append(args, p)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM projects_seen WHERE project_path NOT IN (`+placeholders(len(live))+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("searchstats: sweep orphaned projects: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
