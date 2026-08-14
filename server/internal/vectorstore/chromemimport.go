package vectorstore

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// One-time import of the legacy chromem-go store.
//
// chromem persisted one gob file per document under a per-collection directory
// named hash2hex(collectionName) — the first 4 bytes of its SHA-256, hex
// encoded — plus one 00000000.gob holding the collection's own name and
// metadata. Nothing here links against chromem-go: the two structs below
// mirror what it wrote, and gob matches fields by name, so decoding needs no
// dependency. The whole point is that the legacy tree stays readable and
// untouched — it is the rollback path.
// ---------------------------------------------------------------------------

// chromemDoc mirrors chromem-go's Document as persistToFile wrote it
// (gob, uncompressed, unencrypted).
type chromemDoc struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string
}

// chromemCollectionMeta mirrors the struct chromem wrote to 00000000.gob.
type chromemCollectionMeta struct {
	Name     string
	Metadata map[string]string
}

// chromemMetadataFile is the fixed name of the per-collection metadata gob.
const chromemMetadataFile = "00000000.gob"

// importDBFactor is how much space the finished database is assumed to need,
// relative to the gob tree it reads. Measured on the real 2.5 GB gob tree: the
// finished SQLite file is 1.86 GB, i.e. 0.74x. (An earlier 0.45x figure came
// from a prototype that did not store chunk content; the shipping schema does
// — see vector_contents in schemaSQL — so it is not the number to size a disk
// guard with.)
const importDBFactor = 0.74

// importWALFactor sizes the transient WAL peak, relative to the LARGEST single
// collection — not the whole tree. The import commits one transaction per
// collection so that an interruption redoes exactly one collection, and every
// page a transaction touches sits in the WAL until it commits — so the WAL
// peaks at roughly one collection's worth of pages, whatever the write batch
// size is. A flat share of the total would under-provision whenever one
// collection dominates the tree (a monorepo). PRAGMA journal_size_limit (see
// sqlite.go) truncates the file back to its cap at the next checkpoint, so
// this is a transient peak rather than a permanent high-water mark, but the
// import has to fit through it.
const importWALFactor = 0.8

// importLogEvery throttles progress logging to one line per N collections.
const importLogEvery = 10

// importBatchSize is how many decoded documents the writer holds in memory at
// once. It is the whole point of the streaming pipeline: decoding a collection
// into one big slice before opening the transaction cost ~9 kB of live heap per
// document (measured 268 MB peak HeapAlloc for a 30k-document collection, so
// ~650 MB for 74k and ~1.8 GB for a 200k-document monorepo) — the first boot
// after the upgrade demanded exactly the memory this store exists to give back,
// and OOM-killed containers sized for the new ~28 MB idle footprint. Peak heap
// now scales with this constant instead of with the collection.
//
// A var, not a const, so tests can exercise batch boundaries cheaply.
var importBatchSize = 2000

// importLegacyChromem imports every collection of s.legacyDir that is not yet
// recorded in migration_state. Each collection is one transaction that also
// writes its migration_state row, so an interrupted import redoes exactly the
// collection it was in the middle of and never duplicates a finished one.
//
// A fresh install (no legacy directory) returns immediately and silently.
func (s *Store) importLegacyChromem(ctx context.Context) error {
	if s.legacyDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.legacyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("vectorstore: read legacy chroma dir %q: %w", s.legacyDir, err)
	}

	done, err := s.migratedCollections(ctx)
	if err != nil {
		return err
	}

	type pending struct {
		dir  string // absolute collection directory
		name string // chromem collection name
	}
	var todo []pending
	var totalBytes, maxBytes int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.legacyDir, e.Name())
		name, err := readCollectionName(dir)
		if err != nil {
			// A directory without a readable metadata gob is not a chromem
			// collection (or is a half-written one). Skipping is right: we
			// cannot name it, and inventing a name would create a collection
			// no project can ever address.
			s.logger.Warn("vectorstore: skipping unreadable legacy collection directory",
				"dir", dir, "err", err)
			continue
		}
		if _, ok := done[name]; ok {
			continue
		}
		todo = append(todo, pending{dir: dir, name: name})
		b := dirBytes(dir)
		totalBytes += b
		maxBytes = max(maxBytes, b)
	}
	if len(todo) == 0 {
		return nil
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].name < todo[j].name })

	need := int64(float64(totalBytes)*importDBFactor + float64(maxBytes)*importWALFactor)
	if err := checkFreeSpace(s.dir, need); err != nil {
		return err
	}

	started := time.Now()
	// WARN, not INFO, for all three migration lines. Production runs at warn
	// level and the HTTP listener only comes up after the store opens, so at
	// info the operator watches a server that answers nothing and says nothing
	// for the whole import. That exact silence has repeatedly been read as
	// "the server is down" and answered with a restart — which throws away the
	// collection in flight and starts the wait again.
	s.logger.Warn("migrating vector store: importing legacy chromem collections",
		"collections", len(todo), "source", s.legacyDir, "target", s.dbPath)

	var totalDocs int64
	for i, p := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := s.importCollection(ctx, p.dir, p.name)
		if err != nil {
			return fmt.Errorf("vectorstore: import legacy collection %q: %w", p.name, err)
		}
		totalDocs += int64(n)
		if (i+1)%importLogEvery == 0 || i == len(todo)-1 {
			s.logger.Warn("migrating vector store",
				"collections", fmt.Sprintf("%d/%d", i+1, len(todo)),
				"docs", totalDocs,
				"elapsed", time.Since(started).Round(time.Second).String())
		}
	}
	// The last collection's transaction leaves its pages sitting in the WAL
	// until some future checkpoint — on a freshly migrated idle server that is
	// ~100+ MB of dead disk counted in the "Vector store" usage row. Truncate
	// it now, while nothing else is writing. Best effort: a busy WAL just
	// waits for the ordinary checkpoint cadence instead.
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		s.logger.Warn("vectorstore: post-import wal checkpoint", "err", err)
	}
	s.logger.Warn("vector store migration complete",
		"collections", len(todo), "docs", totalDocs,
		"elapsed", time.Since(started).Round(time.Millisecond).String(),
		"legacy_store_kept_at", s.legacyDir)
	return nil
}

// LegacyStatus describes how much of one legacy chromem namespace directory
// has been imported into the SQLite store that shadows it. It is the evidence
// the maintenance surface needs before it may offer the gob tree for deletion:
// the tree is only garbage once every collection in it is also in the database.
type LegacyStatus struct {
	// Collections is how many directories under the namespace are readable
	// chromem collections.
	Collections int
	// Migrated is how many of those have a migration_state row.
	Migrated int
	// Documents is the total the import recorded for the migrated ones.
	Documents int64
	// Unreadable names the directories that are NOT readable chromem
	// collections. The import skipped them (it cannot name a collection whose
	// metadata gob it cannot decode), so whatever they hold exists nowhere
	// else, and their presence is what stops the tree being called migrated.
	Unreadable []string
}

// Complete reports whether the whole namespace has been imported: at least one
// collection, every one of them recorded, and nothing in the tree the import
// could not read. Deliberately false for an empty directory — "there was never
// anything here" is not the same claim as "everything here is safely in SQLite".
func (st LegacyStatus) Complete() bool {
	return st.Collections > 0 && st.Migrated == st.Collections && len(st.Unreadable) == 0
}

// LegacyMigrationStatus inspects the chromem namespace directory legacyDir
// against the migration record of the store that imported it (the map returned
// by Maintainer.MigratedCollections).
//
// It reads only the per-collection metadata gobs — one small file per
// collection — so it is cheap enough to run on every analysis. Loose FILES at
// the namespace level are ignored: chromem kept everything in per-collection
// directories, so a stray .DS_Store is not data the import could have taken
// and must not veto reclaiming 2 GB of gob files.
func LegacyMigrationStatus(legacyDir string, migrated map[string]int) (LegacyStatus, error) {
	var st LegacyStatus
	if legacyDir == "" {
		return st, errors.New("vectorstore: empty legacy chroma directory")
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return st, fmt.Errorf("vectorstore: read legacy chroma dir %q: %w", legacyDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(legacyDir, e.Name())
		name, err := readCollectionName(dir)
		if err != nil {
			st.Unreadable = append(st.Unreadable, dir)
			continue
		}
		st.Collections++
		docs, ok := migrated[name]
		if !ok {
			continue
		}
		st.Migrated++
		st.Documents += int64(docs)
	}
	return st, nil
}

// migratedCollections reads the set of collection names already imported.
func (s *Store) migratedCollections(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT collection_name FROM migration_state`)
	if err != nil {
		return nil, fmt.Errorf("vectorstore: read migration state: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("vectorstore: read migration state: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// importCollection imports one chromem collection directory in a single
// transaction and returns the number of documents written.
//
// Decoding and writing are pipelined: NumCPU workers decode gob files into a
// channel and this goroutine drains it into the transaction importBatchSize
// documents at a time, dropping each batch's references before pulling the
// next. Live heap is therefore bounded by the batch and the channel buffer, not
// by the collection — see importBatchSize for the numbers that motivate it.
//
// It is still ONE transaction per collection, which is what makes an
// interrupted import resumable: either the collection and its migration_state
// row are both there, or neither is, so the next boot redoes exactly the
// collection that was in flight and never duplicates a finished one.
func (s *Store) importCollection(ctx context.Context, dir, name string) (int, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if f.IsDir() || f.Name() == chromemMetadataFile || filepath.Ext(f.Name()) != ".gob" {
			continue
		}
		paths = append(paths, filepath.Join(dir, f.Name()))
	}
	sort.Strings(paths)

	// Producers are cancelled on every exit path, so a writer error or a
	// cancelled import never leaves decode workers blocked on a send.
	decodeCtx, stopDecoding := context.WithCancel(ctx)
	defer stopDecoding()
	docs := decodeDocs(decodeCtx, paths, s.logger)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collections(name) VALUES(?)`, name); err != nil {
		return 0, err
	}
	var collID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM collections WHERE name = ?`, name).Scan(&collID); err != nil {
		return 0, err
	}
	vecStmt, err := tx.PrepareContext(ctx, upsertVectorSQL)
	if err != nil {
		return 0, err
	}
	defer vecStmt.Close()
	contentStmt, err := tx.PrepareContext(ctx, upsertContentSQL)
	if err != nil {
		return 0, err
	}
	defer contentStmt.Close()

	written := 0
	batch := make([]*chromemDoc, 0, importBatchSize)
	flush := func() error {
		for _, d := range batch {
			if len(d.Embedding) == 0 {
				continue
			}
			emb := d.Embedding
			if !isNormalized(emb) {
				emb = normalizeVector(emb)
			}
			if _, err := vecStmt.ExecContext(ctx, collID, d.ID,
				d.Metadata["file_path"],
				atoiOrZero(d.Metadata["start_line"]),
				atoiOrZero(d.Metadata["end_line"]),
				d.Metadata["chunk_type"], d.Metadata["symbol_name"], d.Metadata["language"],
				floatsBlob(emb)); err != nil {
				return err
			}
			if _, err := contentStmt.ExecContext(ctx, collID, d.ID, d.Content); err != nil {
				return err
			}
			written++
		}
		// clear() before reslicing: the backing array outlives the batch, and
		// leaving stale pointers in it would pin a whole batch of documents
		// (embedding plus chunk text) for the lifetime of the collection.
		clear(batch)
		batch = batch[:0]
		return nil
	}

	for d := range docs {
		batch = append(batch, d)
		if len(batch) < importBatchSize {
			continue
		}
		if err := flush(); err != nil {
			return 0, err
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	// The producers stop early on cancellation, which looks exactly like a
	// finished collection from here — so ask the context, not the channel.
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO migration_state(collection_name, migrated_at, docs) VALUES(?,?,?)
		 ON CONFLICT(collection_name) DO UPDATE SET migrated_at=excluded.migrated_at, docs=excluded.docs`,
		name, time.Now().UTC().Format(time.RFC3339), written); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return written, nil
}

// decodeDocs decodes gob files in parallel and streams the results. Decoding is
// the expensive half of the import, so it runs on NumCPU workers; the channel
// is what keeps the decoded documents from piling up ahead of the writer.
//
// The returned channel is closed when every worker has finished — on success,
// on cancellation, or after a file that could not be decoded. Documents arrive
// in completion order, not in `paths` order: nothing downstream depends on the
// order (rows are keyed by doc_id), and imposing one would mean buffering.
func decodeDocs(ctx context.Context, paths []string, logger *slog.Logger) <-chan *chromemDoc {
	out := make(chan *chromemDoc, importBatchSize)
	workers := min(runtime.NumCPU(), len(paths))
	if workers < 1 {
		close(out)
		return out
	}

	idx := make(chan string, workers*4)
	go func() {
		defer close(idx)
		for _, p := range paths {
			select {
			case idx <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range idx {
				d, err := readChromemDoc(p)
				if err != nil {
					// A file that is not a Document is either the collection
					// metadata under an unexpected name or something foreign.
					// Neither is worth failing a whole namespace over.
					logger.Warn("vectorstore: skipping undecodable legacy document",
						"file", p, "err", err)
					continue
				}
				select {
				case out <- d:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func readChromemDoc(path string) (*chromemDoc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := &chromemDoc{}
	if err := gob.NewDecoder(f).Decode(d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		return nil, errors.New("document has no ID")
	}
	return d, nil
}

// readCollectionName maps a chromem collection DIRECTORY back to the logical
// collection name. The directory is named after a hash of the name and is not
// invertible, so the name is read from the metadata gob chromem wrote next to
// the documents.
func readCollectionName(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, chromemMetadataFile))
	if err != nil {
		return "", err
	}
	defer f.Close()
	m := &chromemCollectionMeta{}
	if err := gob.NewDecoder(f).Decode(m); err != nil {
		return "", err
	}
	if m.Name == "" {
		return "", errors.New("collection metadata has no name")
	}
	return m.Name, nil
}

// dirBytes sums the file sizes directly inside dir. Best effort — it feeds the
// free-space estimate, not a correctness decision.
func dirBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// checkFreeSpace refuses to start an import that obviously cannot finish.
// Running out of disk mid-import is recoverable (the interrupted collection is
// simply redone) but it fails the boot, so it is worth catching up front.
// Platforms where free space cannot be read skip the check.
func checkFreeSpace(dir string, need int64) error {
	free, ok := freeSpaceBytes(dir)
	if !ok || need <= 0 {
		return nil
	}
	if free < uint64(need) {
		return fmt.Errorf(
			"vectorstore: not enough free space to import the legacy vector store: need ~%d MB on %s, %d MB available",
			need/(1<<20), dir, free/(1<<20))
	}
	return nil
}
