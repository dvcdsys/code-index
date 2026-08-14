// Package vectorstore is the persistent vector store: chunk embeddings on
// disk in SQLite, searched by a streamed brute-force scan.
//
// # Why SQLite and not an in-memory index
//
// The store used to be chromem-go, which eagerly loads every document of every
// collection into the Go heap at open and never evicts. On a real 312k-document
// index that cost 2.2 GB of resident memory and a 47-second cold boot before
// the first query could be answered — the memory was proportional to the index,
// not to the work. Here the embeddings live as BLOBs in one SQLite file per
// embedding namespace and a query streams the collection past a dot product
// with a top-K heap. Opening is one file open (sub-millisecond), resident
// memory is a couple of page-cache buffers per active query, and the cost is
// search latency: roughly 3x chromem's on the same data.
//
// # Frozen contracts
//
// Collection naming (project_<md5hex(projectPath)>) and the document ID format
// are compatibility contracts shared with the archived Python backend and with
// every index already on disk. They are unchanged, which is what lets the
// legacy chromem gob files be imported verbatim (see chromemimport.go).
package vectorstore

import (
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// upsertBatchSize is how many documents share one write transaction.
const upsertBatchSize = 500

// Chunk is the input unit for UpsertChunks.
// Mirrors the metadata keys stored by the Python VectorStoreService.
type Chunk struct {
	Content    string
	FilePath   string
	StartLine  int
	EndLine    int
	ChunkType  string
	SymbolName string
	Language   string
}

// SearchResult mirrors the Python SearchResultItem schema returned by /search.
type SearchResult struct {
	FilePath   string
	StartLine  int
	EndLine    int
	Content    string
	Score      float32 // cosine similarity in [0,1], rounded to 4 decimal places
	ChunkType  string
	SymbolName string
	Language   string
}

// Options configures Open.
type Options struct {
	// Dir is the namespace directory. The database is Dir/vectors.db.
	Dir string
	// LegacyChromaDir is the chromem-go persist directory for the SAME
	// embedding namespace. When it exists, its collections are imported on
	// open (once, recorded in migration_state). Nothing under it is ever
	// modified or removed — it is the rollback path.
	LegacyChromaDir string
	// MMapBytes maps PRAGMA mmap_size. 0 (the default) leaves it off.
	// Enabling it trades resident memory for roughly 40% lower latency:
	// mapped database pages are clean and reclaimable, but they count in RSS
	// and every connection maps the file.
	MMapBytes int64
	// Logger receives migration progress. Defaults to a discarding logger.
	Logger *slog.Logger
}

// Store is a vector store backed by one SQLite file.
type Store struct {
	db     *sql.DB
	dir    string // namespace directory
	dbPath string // dir/vectors.db
	// legacyDir is the chromem namespace this store was migrated from. Kept
	// so maintenance can recognise it as belonging to the ACTIVE namespace
	// and never offer it for deletion.
	legacyDir string
	logger    *slog.Logger

	// closeMu guards closed. Every operation holds it for reading, Close
	// takes it for writing, so Close waits for in-flight work to finish
	// before the pool goes away. That is what makes it safe to close a store
	// that has just been swapped out from under concurrent searches.
	closeMu sync.RWMutex
	closed  bool

	// collMu guards collIDs, a cache of collection name -> rowid. Names are
	// never reused for a different collection and ids never change, so the
	// only invalidation needed is on delete.
	collMu  sync.Mutex
	collIDs map[string]int64
}

// ErrClosed is returned by every method once Close has run.
var ErrClosed = errors.New("vectorstore: store is closed")

// Open opens (creating if needed) a vector store in the namespace directory
// dir, with no legacy import. Kept as the simple form used by tests and tools.
func Open(dir string) (*Store, error) {
	return OpenWith(Options{Dir: dir})
}

// OpenWith opens a vector store and, when o.LegacyChromaDir holds a chromem-go
// layout, imports any collection that has not been imported yet.
func OpenWith(o Options) (*Store, error) {
	if o.Dir == "" {
		return nil, errors.New("vectorstore: empty namespace directory")
	}
	dir := filepath.Clean(o.Dir)
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	dbPath := filepath.Join(dir, DBFileName)
	db, err := openDB(dbPath, o.MMapBytes)
	if err != nil {
		return nil, fmt.Errorf("vectorstore open %q: %w", dbPath, err)
	}
	s := &Store{
		db:        db,
		dir:       dir,
		dbPath:    dbPath,
		legacyDir: strings.TrimSuffix(filepath.Clean(o.LegacyChromaDir), string(os.PathSeparator)),
		logger:    logger,
		collIDs:   map[string]int64{},
	}
	if o.LegacyChromaDir == "" {
		s.legacyDir = ""
	}
	if err := s.importLegacyChromem(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle. It waits for in-flight operations, so a
// store swapped out of a Holder can be closed straight away. Idempotent.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// acquire blocks new work while Close is running and reports whether the store
// is still usable. Callers must release() on every path they acquire.
func (s *Store) acquire() bool {
	s.closeMu.RLock()
	if s.closed {
		s.closeMu.RUnlock()
		return false
	}
	return true
}

func (s *Store) release() { s.closeMu.RUnlock() }

// collectionName mirrors Python: f"project_{md5hex(project_path)}"
func collectionName(projectPath string) string {
	h := md5.Sum([]byte(projectPath))
	return fmt.Sprintf("project_%x", h)
}

// CollectionName is the exported alias for the per-project collection
// identifier. FROZEN: existing indexes on disk (including those imported from
// the prior Python backend and from chromem-go) are keyed by it.
func CollectionName(projectPath string) string { return collectionName(projectPath) }

// docID format: "{md5hex(filePath)[:12]}:{startLine}-{endLine}:{idx}"
//
// The positional `idx` is required because overlapping-window or repeated
// chunkers can emit two chunks with identical (filePath, startLine, endLine);
// without idx the second silently overwrites the first.
//
// `h[:6]` gives 12 hex characters. Format is frozen — existing prod indexes
// reference these ids on disk; changing the shape requires a full reindex.
func docID(filePath string, startLine, endLine, idx int) string {
	h := md5.Sum([]byte(filePath))
	return fmt.Sprintf("%x:%d-%d:%d", h[:6], startLine, endLine, idx)
}

// collectionID resolves a collection name to its rowid without creating it.
// ok is false when the collection does not exist.
func (s *Store) collectionID(ctx context.Context, name string) (id int64, ok bool, err error) {
	s.collMu.Lock()
	id, cached := s.collIDs[name]
	s.collMu.Unlock()
	if cached {
		return id, true, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM collections WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("vectorstore: look up collection %q: %w", name, err)
	}
	s.collMu.Lock()
	s.collIDs[name] = id
	s.collMu.Unlock()
	return id, true, nil
}

// ensureCollection resolves a collection name to its rowid, creating the row
// when it is missing.
func (s *Store) ensureCollection(ctx context.Context, name string) (int64, error) {
	if id, ok, err := s.collectionID(ctx, name); err != nil || ok {
		return id, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO collections(name) VALUES(?)`, name); err != nil {
		return 0, fmt.Errorf("vectorstore: create collection %q: %w", name, err)
	}
	id, ok, err := s.collectionID(ctx, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("vectorstore: collection %q vanished after insert", name)
	}
	return id, nil
}

// forgetCollection drops a name from the id cache (after a delete).
func (s *Store) forgetCollection(name string) {
	s.collMu.Lock()
	delete(s.collIDs, name)
	s.collMu.Unlock()
}

const upsertVectorSQL = `INSERT INTO vectors
  (collection_id, doc_id, file_path, start_line, end_line, chunk_type, symbol_name, language, embedding)
  VALUES (?,?,?,?,?,?,?,?,?)
  ON CONFLICT(collection_id, doc_id) DO UPDATE SET
    file_path=excluded.file_path, start_line=excluded.start_line, end_line=excluded.end_line,
    chunk_type=excluded.chunk_type, symbol_name=excluded.symbol_name, language=excluded.language,
    embedding=excluded.embedding`

const upsertContentSQL = `INSERT INTO vector_contents (collection_id, doc_id, content)
  VALUES (?,?,?)
  ON CONFLICT(collection_id, doc_id) DO UPDATE SET content=excluded.content`

// UpsertChunks stores or overwrites chunks with their pre-computed embeddings.
// chunks and embeddings must be the same length.
//
// Embeddings are stored L2-normalised (search is a plain dot product), using
// the same tolerance chromem-go used, so an already-normalised vector is
// written byte-for-byte as the caller supplied it.
func (s *Store) UpsertChunks(ctx context.Context, projectPath string, chunks []Chunk, embeddings [][]float32) error {
	if len(chunks) != len(embeddings) {
		return fmt.Errorf("vectorstore: chunks(%d) and embeddings(%d) length mismatch", len(chunks), len(embeddings))
	}
	if !s.acquire() {
		return ErrClosed
	}
	defer s.release()
	if len(chunks) == 0 {
		// Still create the collection, matching the previous behaviour where
		// an upsert of nothing left an (empty) collection behind.
		_, err := s.ensureCollection(ctx, collectionName(projectPath))
		return err
	}

	collID, err := s.ensureCollection(ctx, collectionName(projectPath))
	if err != nil {
		return err
	}

	for start := 0; start < len(chunks); start += upsertBatchSize {
		end := min(start+upsertBatchSize, len(chunks))
		if err := s.upsertBatch(ctx, collID, chunks[start:end], embeddings[start:end], start); err != nil {
			return fmt.Errorf("vectorstore upsert batch [%d:%d]: %w", start, end, err)
		}
	}
	return nil
}

// upsertBatch writes one transaction. offset is the index of chunks[0] in the
// caller's slice — the docID carries the position, so batching must not
// renumber it.
func (s *Store) upsertBatch(ctx context.Context, collID int64, chunks []Chunk, embeddings [][]float32, offset int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	vecStmt, err := tx.PrepareContext(ctx, upsertVectorSQL)
	if err != nil {
		return err
	}
	defer vecStmt.Close()
	contentStmt, err := tx.PrepareContext(ctx, upsertContentSQL)
	if err != nil {
		return err
	}
	defer contentStmt.Close()

	for i, c := range chunks {
		emb := embeddings[i]
		if !isNormalized(emb) {
			emb = normalizeVector(emb)
		}
		id := docID(c.FilePath, c.StartLine, c.EndLine, offset+i)
		if _, err := vecStmt.ExecContext(ctx, collID, id, c.FilePath, c.StartLine, c.EndLine,
			c.ChunkType, c.SymbolName, c.Language, floatsBlob(emb)); err != nil {
			return err
		}
		if _, err := contentStmt.ExecContext(ctx, collID, id, c.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteByFile removes all chunks for a given file within a project.
func (s *Store) DeleteByFile(ctx context.Context, projectPath, filePath string) error {
	if !s.acquire() {
		return ErrClosed
	}
	defer s.release()

	collID, ok, err := s.collectionID(ctx, collectionName(projectPath))
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM vector_contents
		WHERE collection_id = ? AND doc_id IN (
			SELECT doc_id FROM vectors WHERE collection_id = ? AND file_path = ?)`,
		collID, collID, filePath); err != nil {
		return fmt.Errorf("vectorstore delete contents for %q: %w", filePath, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM vectors WHERE collection_id = ? AND file_path = ?`, collID, filePath); err != nil {
		return fmt.Errorf("vectorstore delete by file %q: %w", filePath, err)
	}
	return tx.Commit()
}

// DeleteCollection removes the entire vector collection for a project.
func (s *Store) DeleteCollection(projectPath string) error {
	return s.DeleteCollectionByName(collectionName(projectPath))
}

// Count returns the number of chunks stored for a project.
func (s *Store) Count(projectPath string) int {
	if !s.acquire() {
		return 0
	}
	defer s.release()

	ctx := context.Background()
	collID, ok, err := s.collectionID(ctx, collectionName(projectPath))
	if err != nil || !ok {
		return 0
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vectors WHERE collection_id = ?`, collID).Scan(&n); err != nil {
		return 0
	}
	return n
}

// DBPath is the SQLite file backing this store.
func (s *Store) DBPath() string { return s.dbPath }

// round4 rounds f to 4 decimal places, matching Python's round(score, 4).
func round4(f float32) float32 {
	return float32(math.Round(float64(f)*10000) / 10000)
}

// atoiOrZero parses a decimal metadata value, tolerating junk.
func atoiOrZero(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
