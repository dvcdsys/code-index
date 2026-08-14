package vectorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v1SchemaSQL is the schema as it shipped BEFORE the v2 rebuild, reproduced
// verbatim. It is deliberately a frozen literal and not derived from schemaSQL:
// its whole job is to be what is actually on disk in the one installation that
// predates v2, so it must not follow schemaSQL when that changes again.
//
// The differences that matter: collections.id is a plain rowid (no
// AUTOINCREMENT, so ids are reused), and neither child table declares a
// foreign key.
const v1SchemaSQL = `
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
CREATE INDEX IF NOT EXISTS idx_vec_coll ON vectors(collection_id);
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

// writeV1Database builds a database the way the pre-v2 code would have: 8 KiB
// pages, WAL, auto_vacuum off, no user_version stamp, v1 schema. fill runs
// against the open handle to populate it.
func writeV1Database(t *testing.T, path string, fill func(*sql.DB)) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open v1 fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA page_size=%d", pageSize)); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if _, err := db.Exec(v1SchemaSQL); err != nil {
		t.Fatalf("v1 schema: %v", err)
	}
	if fill != nil {
		fill(db)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v1 fixture: %v", err)
	}
}

// insertV1Collection writes one collection and n documents with the given
// explicit collection id.
func insertV1Collection(t *testing.T, db *sql.DB, id int64, name string, n int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO collections(id, name) VALUES(?,?)`, id, name); err != nil {
		t.Fatalf("insert collection %s: %v", name, err)
	}
	insertV1Rows(t, db, id, name, n)
}

// insertV1Rows writes n documents for a collection id, without touching the
// collections table — which is how an orphan is made.
func insertV1Rows(t *testing.T, db *sql.DB, id int64, tag string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		doc := fmt.Sprintf("%s-doc-%03d", tag, i)
		if _, err := db.Exec(`INSERT INTO vectors
			(collection_id, doc_id, file_path, start_line, end_line, chunk_type, symbol_name, language, embedding)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, doc, fmt.Sprintf("src/%s.go", tag), i, i+3, "function", "Sym", "go",
			floatsBlob([]float32{1, 0, 0, 0})); err != nil {
			t.Fatalf("insert vector: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO vector_contents (collection_id, doc_id, content) VALUES (?,?,?)`,
			id, doc, "body of "+doc); err != nil {
			t.Fatalf("insert content: %v", err)
		}
	}
}

func pragmaInt(t *testing.T, db *sql.DB, pragma string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&v); err != nil {
		t.Fatalf("read PRAGMA %s: %v", pragma, err)
	}
	return v
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Opening a v1 file must rebuild it into v2 in place: same path, same data,
// new guarantees. This is the only upgrade the one existing installation will
// ever run, so it is checked property by property.
func TestUpgradeSchema_RebuildsV1InPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)

	const (
		liveName  = "project_aaaa"
		otherName = "project_bbbb"
		orphanID  = 99
	)
	writeV1Database(t, path, func(db *sql.DB) {
		insertV1Collection(t, db, 1, liveName, 40)
		insertV1Collection(t, db, 7, otherName, 25)
		// Exactly the leak v2 exists to stop: rows whose collection row is
		// gone. A v1 file may well hold some, and they cannot be carried into a
		// database that enforces the constraint.
		insertV1Rows(t, db, orphanID, "orphan", 12)
		if _, err := db.Exec(
			`INSERT INTO migration_state(collection_name, migrated_at, docs) VALUES(?,?,?)`,
			liveName, "2026-01-01T00:00:00Z", 40); err != nil {
			t.Fatalf("insert migration_state: %v", err)
		}
	})
	// A -wal left behind by a crash describes a file that will not exist after
	// the rename; the upgrade has to clear it or the next open reads garbage.
	if err := os.WriteFile(path+"-wal", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if v := userVersionOf(t, path); v != 0 {
		t.Fatalf("fixture user_version = %d, want 0 (unstamped, as v1 shipped)", v)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open a v1 database: %v", err)
	}
	defer s.Close()

	if got := pragmaInt(t, s.db, "user_version"); got != schemaVersion {
		t.Errorf("user_version = %d, want %d", got, schemaVersion)
	}
	if got := pragmaInt(t, s.db, "auto_vacuum"); got != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (INCREMENTAL)", got)
	}
	if got := pragmaInt(t, s.db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d, want 1", got)
	}
	if got := pragmaInt(t, s.db, "page_size"); got != pageSize {
		t.Errorf("page_size = %d, want %d", got, pageSize)
	}

	// AUTOINCREMENT is visible two ways, and both matter: the declared SQL is
	// what makes new inserts monotonic, and sqlite_sequence is what remembers
	// how far the id space already got.
	var ddl string
	if err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='collections'`).Scan(&ddl); err != nil {
		t.Fatalf("read collections DDL: %v", err)
	}
	if !strings.Contains(ddl, "AUTOINCREMENT") {
		t.Errorf("collections DDL after upgrade lacks AUTOINCREMENT:\n%s", ddl)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='collections'`).Scan(&seq); err != nil {
		t.Fatalf("read sqlite_sequence: %v", err)
	}
	if seq != 7 {
		t.Errorf("sqlite_sequence for collections = %d, want 7 (the highest id the v1 file handed out)", seq)
	}

	// Data intact — the two real collections, none of the orphan.
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM collections`); got != 2 {
		t.Errorf("collections after upgrade = %d, want 2", got)
	}
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM vectors WHERE collection_id = 1`); got != 40 {
		t.Errorf("%s holds %d vectors after upgrade, want 40", liveName, got)
	}
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM vector_contents WHERE collection_id = 7`); got != 25 {
		t.Errorf("%s holds %d contents after upgrade, want 25", otherName, got)
	}
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM vectors WHERE collection_id = ?`, orphanID); got != 0 {
		t.Errorf("%d orphan vectors survived the upgrade, want 0", got)
	}
	if got := countRows(t, s.db,
		`SELECT COUNT(*) FROM vectors v LEFT JOIN collections c ON c.id = v.collection_id WHERE c.id IS NULL`); got != 0 {
		t.Errorf("%d vectors have no collection after the upgrade", got)
	}
	if got := countRows(t, s.db, `SELECT docs FROM migration_state WHERE collection_name = ?`, liveName); got != 40 {
		t.Errorf("migration_state for %s = %d docs, want 40 — the record must survive the rebuild", liveName, got)
	}
	var content string
	if err := s.db.QueryRow(
		`SELECT content FROM vector_contents WHERE collection_id = 1 AND doc_id = ?`,
		liveName+"-doc-000").Scan(&content); err != nil {
		t.Fatalf("read a document that existed before the upgrade: %v", err)
	}
	if want := "body of " + liveName + "-doc-000"; content != want {
		t.Errorf("content = %q, want %q", content, want)
	}

	// The rebuild leaves nothing behind.
	if _, err := os.Stat(path + upgradeTempSuffix); !os.IsNotExist(err) {
		t.Errorf("the rebuild temp file survived: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), upgradeTempSuffix) {
			t.Errorf("leftover upgrade artefact %q", e.Name())
		}
	}
}

// An id the v1 file already handed out must never be handed out again, even
// after the collection holding it is deleted. Under v1 it would have been: the
// rowid allocator reuses the largest free value, so the next project created
// would have inherited the deleted one's id — and any row that outlived the
// delete with it.
func TestUpgradeSchema_IDsAreNotReusedAfterUpgrade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)
	writeV1Database(t, path, func(db *sql.DB) {
		insertV1Collection(t, db, 1, "project_aaaa", 3)
		insertV1Collection(t, db, 2, "project_bbbb", 3)
	})

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Delete the HIGHEST id — the one v1's allocator would have recycled.
	if err := s.DeleteCollectionByName("project_bbbb"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ctx := context.Background()
	id, err := s.ensureCollection(ctx, "project_cccc")
	if err != nil {
		t.Fatalf("create after delete: %v", err)
	}
	if id <= 2 {
		t.Errorf("new collection got id %d, reusing an id the store had already handed out", id)
	}
}

// Re-opening a database that is already at schemaVersion must not rebuild it:
// the rebuild is the whole file, and doing it on every boot would be minutes of
// I/O for nothing. The inode is the evidence — a rebuild replaces the file.
func TestUpgradeSchema_SkipsAFileAlreadyAtVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.UpsertChunks(context.Background(), "/p",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, DBFileName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	drv, err := sqliteDriver()
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeSchema(drv, path, nil); err != nil {
		t.Fatalf("upgradeSchema on a v2 file: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("a database already at the current schema version was rebuilt anyway")
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.Count("/p"); got != 1 {
		t.Errorf("Count after reopen = %d, want 1", got)
	}
}

// The race the foreign key exists for: a caller holds a cached collection id
// (Store.collIDs, and through it the indexer) across an admin deleting that
// collection. The write must fail rather than commit rows nothing can see.
func TestUpsertChunks_StaleCollectionID(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	const project = "/racing/project"
	name := CollectionName(project)
	if err := s.UpsertChunks(ctx, project,
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	staleID, ok, err := s.collectionID(ctx, name)
	if err != nil || !ok {
		t.Fatalf("resolve collection id: ok=%v err=%v", ok, err)
	}

	if err := s.DeleteCollectionByName(name); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// (a) the in-flight indexer path: the batch writer still holds the id it
	// resolved before the delete.
	err = s.upsertBatch(ctx, staleID,
		[]Chunk{{Content: "late", FilePath: "b.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}, 0)
	if err == nil {
		t.Fatal("an upsert against a deleted collection succeeded — the rows are now invisible to ListCollections and leak disk")
	}
	if !isCollectionGone(err) {
		t.Errorf("upsert failed with %v, want a foreign-key violation", err)
	}

	// (b) the same thing through the public API, with the cache still holding
	// the stale id the way it would in the real race.
	s.collMu.Lock()
	s.collIDs[name] = staleID
	s.collMu.Unlock()
	err = s.UpsertChunks(ctx, project,
		[]Chunk{{Content: "late", FilePath: "b.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}})
	if !errors.Is(err, ErrCollectionDeleted) {
		t.Errorf("UpsertChunks = %v, want it to wrap ErrCollectionDeleted", err)
	}

	// Nothing leaked, and the stale id was dropped so the store is usable again.
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM vectors`); got != 0 {
		t.Errorf("%d orphan vector rows survived the failed upserts, want 0", got)
	}
	if got := countRows(t, s.db, `SELECT COUNT(*) FROM vector_contents`); got != 0 {
		t.Errorf("%d orphan content rows survived the failed upserts, want 0", got)
	}
	s.collMu.Lock()
	_, cached := s.collIDs[name]
	s.collMu.Unlock()
	if cached {
		t.Error("the stale collection id is still cached — every later upsert would fail the same way")
	}
	if err := s.UpsertChunks(ctx, project,
		[]Chunk{{Content: "fresh", FilePath: "c.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert after the cache was invalidated: %v", err)
	}
}

// Deleting a collection must actually hand the pages back, not just move them
// to the freelist: the Resources screen reports the bytes as reclaimed and
// nothing on the filesystem confirmed that before auto_vacuum was on.
func TestDeleteCollectionByName_ReclaimsPages(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	const n = 2000
	chunks := make([]Chunk, n)
	embeddings := make([][]float32, n)
	for i := range chunks {
		chunks[i] = Chunk{
			Content:   strings.Repeat("x", 512),
			FilePath:  fmt.Sprintf("src/f%03d.go", i%50),
			StartLine: i, EndLine: i + 5,
		}
		vec := make([]float32, 128)
		vec[i%128] = 1
		embeddings[i] = vec
	}
	if err := s.UpsertChunks(ctx, "/big", chunks, embeddings); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Keep a second, small collection so the file is not simply emptied.
	if err := s.UpsertChunks(ctx, "/small",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert small: %v", err)
	}

	before := pragmaInt(t, s.db, "page_count")
	if err := s.DeleteCollectionByName(CollectionName("/big")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// The delete itself only moves pages to the freelist — reclaiming is a
	// separate, batched call so a clean over N collections shuffles the file
	// tail once, not N times. This mirrors how maintenance.Clean drives it.
	if free := pragmaInt(t, s.db, "freelist_count"); free == 0 {
		t.Errorf("freelist_count = 0 right after delete — expected freed pages awaiting ReclaimFreePages")
	}
	s.ReclaimFreePages(ctx)
	after := pragmaInt(t, s.db, "page_count")
	free := pragmaInt(t, s.db, "freelist_count")

	if after >= before {
		t.Errorf("page_count %d -> %d: the database did not shrink after delete + ReclaimFreePages", before, after)
	}
	if free != 0 {
		t.Errorf("freelist_count = %d after the incremental vacuum, want 0 — those pages are disk the admin was told they got back", free)
	}
	// …and the survivor is untouched.
	if got := s.Count("/small"); got != 1 {
		t.Errorf("Count(/small) = %d after vacuuming, want 1", got)
	}
}

// userVersionOf reads PRAGMA user_version straight from a file on disk.
func userVersionOf(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	return pragmaInt(t, db, "user_version")
}

// A zero-byte vectors.db is what a kill between the driver creating the file
// and the schema being written leaves behind (OOM, docker stop during first
// boot, a full disk). It must be treated as a fresh database, not sent to the
// upgrader — which would ATTACH an empty file and die on its first SELECT,
// turning a self-healing state into a fatal boot error the operator can only
// fix with rm.
func TestOpen_TreatsAZeroByteDatabaseAsFresh(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DBFileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open over a zero-byte database: %v", err)
	}
	defer s.Close()
	if err := s.UpsertChunks(context.Background(), "/p",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := pragmaInt(t, s.db, "user_version"); got != schemaVersion {
		t.Errorf("user_version = %d, want %d", got, schemaVersion)
	}
	if got := pragmaInt(t, s.db, "auto_vacuum"); got != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (incremental) — the fresh-file pragmas were skipped", got)
	}
}

// The other shape of the same crash window: PRAGMA journal_mode ran (page 1
// exists, with the DEFAULT page size frozen into it) but schemaSQL never did.
// Size-based emptiness checks miss this file, and it cannot be adopted either
// — its frozen 4096 page_size would fail initDatabase's verification. The only
// correct move is to detect "no tables" by content and recreate the file,
// which an empty sqlite_master proves is lossless.
func TestOpen_RecreatesAHeaderOnlyDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var journal string
	if err := raw.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		t.Fatalf("materialise page 1: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("fixture is not header-only (size=%v err=%v) — the test would not exercise the content check", fi, err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open over a header-only database: %v", err)
	}
	defer s.Close()
	if err := s.UpsertChunks(context.Background(), "/p",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := pragmaInt(t, s.db, "page_size"); got != pageSize {
		t.Errorf("page_size = %d, want %d — the header-only file was adopted instead of recreated", got, pageSize)
	}
	if got := pragmaInt(t, s.db, "user_version"); got != schemaVersion {
		t.Errorf("user_version = %d, want %d", got, schemaVersion)
	}
}

// A file stamped by a NEWER binary must keep its stamp. upgradeSchema already
// leaves version >= ours alone; the trap was initDatabase restamping OUR
// version unconditionally afterwards — one old-binary run against a new data
// dir would remark a v3 file as v2, and the next v3 binary would then run its
// upgrade against a file that is already v3.
func TestOpen_DoesNotRestampANewerSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.UpsertChunks(context.Background(), "/p",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, DBFileName)
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatalf("stamp future version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen of a newer-version file: %v", err)
	}
	defer s2.Close()
	if got := pragmaInt(t, s2.db, "user_version"); got != int64(schemaVersion+1) {
		t.Errorf("user_version = %d after a v%d binary opened a v%d file, want %d — the downgrade guard was defeated by the restamp",
			got, schemaVersion, schemaVersion+1, schemaVersion+1)
	}
}
