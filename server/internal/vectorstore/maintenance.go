package vectorstore

import (
	"context"
	"fmt"
)

// CollectionInfo describes one collection held by the store.
type CollectionInfo struct {
	Name      string // collection name, e.g. "project_<md5hex>"
	Documents int    // stored document count
	// SizeBytes is the logical size of the collection's rows: embeddings,
	// chunk text and metadata as stored. It excludes index entries and page
	// slack, so it is a floor on the disk the collection occupies, not an
	// exact figure — SQLite does not attribute pages to a WHERE clause.
	SizeBytes int64
}

// Maintainer is the admin-only maintenance surface over the vector store.
//
// Deliberately NOT part of Interface. Interface is the hot path (upsert /
// search / delete-by-file) consumed by the indexer, repojobs and the search
// handlers, and its doc comment promises it "mirrors *Store exactly". These
// methods address collections by their raw collection name rather than by
// project path — meaningless to every Interface consumer, and folding them in
// would force any future fake to stub extra methods it never calls. Handlers
// type-assert instead; in production the wired value is a *Holder, which
// implements this, so the assertion always succeeds.
type Maintainer interface {
	// ListCollections returns every collection with its document count and
	// logical size. NOT free: it aggregates over the stored rows. Under
	// chromem this was a walk of an in-memory map, and callers that ran it
	// per request should cache the result (maintenance.Service already does).
	ListCollections() []CollectionInfo
	// DeleteCollectionByName drops a collection and its documents. Deleting a
	// name the store does not hold is a no-op, not an error. Pages go to the
	// freelist only — call ReclaimFreePages once after a batch of deletes to
	// shrink the file.
	DeleteCollectionByName(name string) error
	// ReclaimFreePages returns freed pages to the filesystem (incremental
	// vacuum). Call once after a batch of DeleteCollectionByName calls, not
	// per delete. Best effort.
	ReclaimFreePages(ctx context.Context)
	// CollectionSizeBytes is the logical size of one project's collection,
	// and whether that project has a collection at all.
	CollectionSizeBytes(projectPath string) (int64, bool)
	// DBPath is the SQLite file backing the store.
	DBPath() string
	// BaseDir is the namespace directory the store was opened at.
	BaseDir() string
	// LegacyChromaDir is the chromem-go directory this store imported from,
	// or "" when there was none. It still holds the pre-migration gob files:
	// they are the rollback path and must never be classified as garbage
	// while this namespace is the active one.
	LegacyChromaDir() string
	// MigratedCollections returns the legacy chromem collections already
	// imported into this store, keyed by collection name, valued by the
	// document count the import recorded.
	//
	// ok is false when the store cannot answer — no store, closed, or the
	// query failed. Callers MUST read that as "unknown", never as "nothing has
	// been migrated": the difference decides whether a legacy gob tree may be
	// deleted, and an empty map would say "yes, all zero of them".
	MigratedCollections() (map[string]int, bool)
}

var (
	_ Maintainer = (*Store)(nil)
	_ Maintainer = (*Holder)(nil)
)

// sizeExprVectors / sizeExprContents are the per-row logical byte counts.
const sizeExprVectors = `LENGTH(v.embedding) + LENGTH(v.doc_id) + LENGTH(v.file_path) +
	LENGTH(v.chunk_type) + LENGTH(v.symbol_name) + LENGTH(v.language) + 16`

const listCollectionsSQL = `
SELECT c.name,
       COUNT(v.rowid),
       COALESCE(SUM(` + sizeExprVectors + `), 0)
  FROM collections c
  LEFT JOIN vectors v ON v.collection_id = c.id
 GROUP BY c.id
 ORDER BY c.name`

const contentSizeSQL = `
SELECT c.name, COALESCE(SUM(LENGTH(vc.content) + LENGTH(vc.doc_id)), 0)
  FROM collections c
  LEFT JOIN vector_contents vc ON vc.collection_id = c.id
 GROUP BY c.id`

// q8SizeSQL accounts for the compact scan copy. It is a third of the float32
// bytes and it is real disk, so leaving it out would make the Resources screen
// under-report the store by ~25% — the same kind of quiet mismatch the WAL
// high-water mark used to cause.
// sizeExprQ8 is the compact copy's per-row logical byte count, in the same
// shape as sizeExprVectors and pasted in no more places than it is.
//
// Reading it walks the compact table's leaf pages — about a fifth of what the
// same question costs over `vectors`, and behind the maintenance service's TTL
// cache either way, so it is answered rarely. If that stops being true, the
// cheap replacement is recording the total at backfill completion rather than
// making the aggregate faster.
const sizeExprQ8 = `LENGTH(q.embedding) + LENGTH(q.doc_id) + LENGTH(q.language) + 16`

const q8SizeSQL = `
SELECT c.name, COALESCE(SUM(` + sizeExprQ8 + `), 0)
  FROM collections c
  LEFT JOIN vectors_q8 q ON q.collection_id = c.id
 GROUP BY c.id`

// ListCollections implements Maintainer. Results are sorted by name so callers
// (and their tests) see a stable order.
func (s *Store) ListCollections() []CollectionInfo {
	if !s.acquire() {
		return nil
	}
	defer s.release()

	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, listCollectionsSQL)
	if err != nil {
		s.logger.Error("vectorstore: list collections", "err", err)
		return nil
	}
	defer rows.Close()

	var out []CollectionInfo
	for rows.Next() {
		var ci CollectionInfo
		if err := rows.Scan(&ci.Name, &ci.Documents, &ci.SizeBytes); err != nil {
			s.logger.Error("vectorstore: list collections", "err", err)
			return nil
		}
		out = append(out, ci)
	}
	if err := rows.Err(); err != nil {
		s.logger.Error("vectorstore: list collections", "err", err)
		return nil
	}

	// Chunk text and the compact scan copy live in their own tables (see
	// schemaSQL); fold each into the reported size with its own aggregate
	// rather than joins that would multiply the row counts.
	for _, q := range []struct {
		what string
		sql  string
	}{
		{"contents", contentSizeSQL},
		{"scan copy", q8SizeSQL},
	} {
		sizes, err := s.sizesByCollection(ctx, q.sql)
		if err != nil {
			s.logger.Error("vectorstore: list collection "+q.what, "err", err)
			return out
		}
		for i := range out {
			out[i].SizeBytes += sizes[out[i].Name]
		}
	}
	return out
}

// sizesByCollection runs one name -> bytes aggregate.
func (s *Store) sizesByCollection(ctx context.Context, query string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sizes := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		sizes[name] = n
	}
	return sizes, rows.Err()
}

// CollectionSizeBytes implements Maintainer.
func (s *Store) CollectionSizeBytes(projectPath string) (int64, bool) {
	if !s.acquire() {
		return 0, false
	}
	defer s.release()

	ctx := context.Background()
	collID, ok, err := s.collectionID(ctx, collectionName(projectPath))
	if err != nil || !ok {
		return 0, false
	}
	var vecBytes, contentBytes, q8Bytes int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(`+sizeExprVectors+`), 0) FROM vectors v WHERE v.collection_id = ?`,
		collID).Scan(&vecBytes); err != nil {
		return 0, false
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(LENGTH(content) + LENGTH(doc_id)), 0)
		  FROM vector_contents WHERE collection_id = ?`, collID).Scan(&contentBytes); err != nil {
		return 0, false
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(`+sizeExprQ8+`), 0)
		  FROM vectors_q8 q WHERE q.collection_id = ?`, collID).Scan(&q8Bytes); err != nil {
		return 0, false
	}
	return vecBytes + contentBytes + q8Bytes, true
}

// DeleteCollectionByName implements Maintainer.
//
// The migration_state row for the name is deliberately KEPT. It records that
// the legacy chromem collection has already been dealt with; removing it would
// make the next boot re-import from the gob files the admin just reclaimed.
func (s *Store) DeleteCollectionByName(name string) error {
	if !s.acquire() {
		return ErrClosed
	}
	defer s.release()

	ctx := context.Background()
	collID, ok, err := s.collectionID(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	for _, stmt := range []string{
		`DELETE FROM vector_contents WHERE collection_id = ?`,
		`DELETE FROM vectors_q8 WHERE collection_id = ?`,
		`DELETE FROM q8_state WHERE collection_id = ?`,
		`DELETE FROM vectors WHERE collection_id = ?`,
		`DELETE FROM collections WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, collID); err != nil {
			return fmt.Errorf("vectorstore delete collection %q: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vectorstore delete collection %q: %w", name, err)
	}
	s.forgetCollection(name)
	s.forgetQ8(collID)
	return nil
}

// ReclaimFreePages hands freed pages back to the filesystem. Implements
// Maintainer.
//
// Deleting a collection only moves its pages to the freelist: the database file
// keeps its size and `df` never confirms the number the Resources screen just
// reported as reclaimed. auto_vacuum=INCREMENTAL (set at creation, or by the
// v2 rebuild) makes them movable; this pragma is what actually moves them and
// shortens the file. In WAL mode the shrink lands in the WAL and the file is
// physically truncated at the next checkpoint.
//
// Deliberately a SEPARATE call rather than a side effect of
// DeleteCollectionByName: the vacuum walks the freelist and shuffles tail
// pages under the write lock (~170 ms per 20k-doc collection, measured), so a
// bulk clean over N collections would re-shuffle the tail N times while the
// indexer waits on busy_timeout. Callers delete everything first, then reclaim
// once. Also deliberately NOT called from DeleteByFile: the watcher deletes
// and reinserts a file's chunks continuously, and those pages are reused by
// the very next insert — measured on the previous schema: 100 delete+reinsert
// cycles over 72k rows grew the file by 3.8 MB and ended with an empty
// freelist.
//
// Best effort. Failing to shrink a file is not a reason to report a successful
// delete as failed.
func (s *Store) ReclaimFreePages(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		s.logger.Warn("vectorstore: incremental vacuum after delete", "err", err)
	}
}

// BaseDir implements Maintainer.
func (s *Store) BaseDir() string { return s.dir }

// LegacyChromaDir implements Maintainer.
func (s *Store) LegacyChromaDir() string { return s.legacyDir }

// MigratedCollections implements Maintainer.
func (s *Store) MigratedCollections() (map[string]int, bool) {
	if !s.acquire() {
		return nil, false
	}
	defer s.release()

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT collection_name, docs FROM migration_state`)
	if err != nil {
		s.logger.Error("vectorstore: read migration state", "err", err)
		return nil, false
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var docs int
		if err := rows.Scan(&name, &docs); err != nil {
			s.logger.Error("vectorstore: read migration state", "err", err)
			return nil, false
		}
		out[name] = docs
	}
	if err := rows.Err(); err != nil {
		s.logger.Error("vectorstore: read migration state", "err", err)
		return nil, false
	}
	return out, true
}

// ---------------------------------------------------------------------------
// Holder proxies
// ---------------------------------------------------------------------------

// ListCollections proxies to the active store; nil store yields nil.
func (h *Holder) ListCollections() []CollectionInfo {
	s := h.current()
	if s == nil {
		return nil
	}
	return s.ListCollections()
}

// DeleteCollectionByName proxies to the active store.
func (h *Holder) DeleteCollectionByName(name string) error {
	s := h.current()
	if s == nil {
		return errNotInitialised
	}
	return s.DeleteCollectionByName(name)
}

// ReclaimFreePages proxies to the active store; nil store is a no-op.
func (h *Holder) ReclaimFreePages(ctx context.Context) {
	if s := h.current(); s != nil {
		s.ReclaimFreePages(ctx)
	}
}

// CollectionSizeBytes proxies to the active store; nil store yields (0,false).
func (h *Holder) CollectionSizeBytes(projectPath string) (int64, bool) {
	s := h.current()
	if s == nil {
		return 0, false
	}
	return s.CollectionSizeBytes(projectPath)
}

// DBPath proxies to the active store; nil store yields "".
func (h *Holder) DBPath() string {
	s := h.current()
	if s == nil {
		return ""
	}
	return s.DBPath()
}

// BaseDir proxies to the active store; nil store yields "".
func (h *Holder) BaseDir() string {
	s := h.current()
	if s == nil {
		return ""
	}
	return s.BaseDir()
}

// LegacyChromaDir proxies to the active store; nil store yields "".
func (h *Holder) LegacyChromaDir() string {
	s := h.current()
	if s == nil {
		return ""
	}
	return s.LegacyChromaDir()
}

// MigratedCollections proxies to the active store; nil store yields "unknown".
func (h *Holder) MigratedCollections() (map[string]int, bool) {
	s := h.current()
	if s == nil {
		return nil, false
	}
	return s.MigratedCollections()
}
