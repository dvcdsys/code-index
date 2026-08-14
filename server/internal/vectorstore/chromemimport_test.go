package vectorstore

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// chromem-go is a TEST-ONLY dependency now: the server binary no longer links
// it (the importer decodes the gob files through local mirror structs). It is
// kept here because a fixture written by the real chromem is the only evidence
// that the mirror structs still match what is on disk in production.

// legacyFixture writes a chromem-go persist directory and returns it along
// with the documents it holds, keyed by collection name.
func legacyFixture(t *testing.T, collections, docsPer, dim int) (dir string, want map[string][]chromem.Document) {
	t.Helper()
	dir = t.TempDir()
	db, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		t.Fatalf("chromem NewPersistentDB: %v", err)
	}
	embedStub := func(context.Context, string) ([]float32, error) {
		return nil, errors.New("must not be called")
	}
	r := rand.New(rand.NewSource(11))
	want = map[string][]chromem.Document{}
	for c := 0; c < collections; c++ {
		name := CollectionName(fmt.Sprintf("/legacy/project%d", c))
		col, err := db.GetOrCreateCollection(name, map[string]string{"hnsw:space": "cosine"}, embedStub)
		if err != nil {
			t.Fatalf("chromem GetOrCreateCollection: %v", err)
		}
		docs := make([]chromem.Document, docsPer)
		for i := range docs {
			file := fmt.Sprintf("src/f%02d.go", i%3)
			start, end := i*7+1, i*7+5
			docs[i] = chromem.Document{
				ID:      docID(file, start, end, i),
				Content: fmt.Sprintf("collection %d document %d body", c, i),
				Metadata: map[string]string{
					"file_path":   file,
					"start_line":  strconv.Itoa(start),
					"end_line":    strconv.Itoa(end),
					"chunk_type":  "function",
					"symbol_name": fmt.Sprintf("Sym%02d", i),
					"language":    "go",
				},
				Embedding: randNorm(r, dim),
			}
		}
		if err := col.AddDocuments(context.Background(), docs, 1); err != nil {
			t.Fatalf("chromem AddDocuments: %v", err)
		}
		want[name] = docs
	}
	return dir, want
}

// treeSnapshot lists every file under root with its size, so a test can prove
// the legacy store was not modified.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, fmt.Sprintf("%s %d", rel, info.Size()))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

func TestImportLegacyChromem(t *testing.T) {
	const (
		collections = 3
		docsPer     = 12
		dim         = 16
	)
	legacy, want := legacyFixture(t, collections, docsPer, dim)
	before := treeSnapshot(t, legacy)

	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	defer s.Close()

	got := s.ListCollections()
	if len(got) != collections {
		t.Fatalf("imported %d collections, want %d", len(got), collections)
	}
	for _, ci := range got {
		if ci.Documents != docsPer {
			t.Errorf("collection %s has %d documents, want %d", ci.Name, ci.Documents, docsPer)
		}
	}

	// Every document must round-trip: metadata, content, and the embedding
	// byte-for-byte (chromem stored these already normalised, so nothing may
	// have been rewritten on the way in).
	for name, docs := range want {
		assertDocsImportedExactly(t, s, name, docs)
	}

	if diff := treeSnapshot(t, legacy); !equalStrings(before, diff) {
		t.Errorf("legacy chromem store was modified by the import:\nbefore=%v\nafter=%v", before, diff)
	}
}

// A second open must not re-import anything: migration_state is the record
// that a collection has been dealt with.
func TestImportLegacyChromem_IsIdempotent(t *testing.T) {
	legacy, _ := legacyFixture(t, 2, 5, 8)
	dir := t.TempDir()

	s, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	firstState := migrationState(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	if second := migrationState(t, s2); !equalStrings(firstState, second) {
		t.Errorf("migration state changed on reopen:\nfirst=%v\nsecond=%v", firstState, second)
	}
	for _, ci := range s2.ListCollections() {
		if ci.Documents != 5 {
			t.Errorf("collection %s has %d documents after reopen, want 5 (re-imported?)", ci.Name, ci.Documents)
		}
	}
}

// TestImportLegacyChromem_ResumesAfterInterruption covers the crash path: the
// process dies part-way through, having finished some collections and possibly
// left rows behind for the one it was in the middle of.
func TestImportLegacyChromem_ResumesAfterInterruption(t *testing.T) {
	const collections = 4
	legacy, want := legacyFixture(t, collections, 6, 8)
	dir := t.TempDir()

	// Open with NO legacy dir so nothing is imported, then import exactly two
	// collections by hand — the state a crash would leave behind.
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)

	dirs := collectionDirsByName(t, legacy)
	ctx := context.Background()
	for _, n := range names[:2] {
		if _, err := s.importCollection(ctx, dirs[n], n); err != nil {
			t.Fatalf("pre-import %s: %v", n, err)
		}
	}
	// ...and half of a third one, with NO migration_state row: the collection
	// was interrupted mid-transaction in spirit, so the next run must redo it
	// from scratch without duplicating the rows it already wrote.
	interrupted := names[2]
	collID, err := s.ensureCollection(ctx, interrupted)
	if err != nil {
		t.Fatalf("ensure %s: %v", interrupted, err)
	}
	for _, d := range want[interrupted][:3] {
		if _, err := s.db.Exec(upsertVectorSQL, collID, d.ID, "stale.go", 0, 0, "", "", "",
			floatsBlob(d.Embedding)); err != nil {
			t.Fatalf("partial write: %v", err)
		}
	}
	stateBefore := migrationState(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen the way the server does — this is the resume.
	s2, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("resume open: %v", err)
	}
	defer s2.Close()

	got := s2.ListCollections()
	if len(got) != collections {
		t.Fatalf("after resume: %d collections, want %d", len(got), collections)
	}
	for _, ci := range got {
		if ci.Documents != 6 {
			t.Errorf("after resume: collection %s has %d documents, want 6", ci.Name, ci.Documents)
		}
	}
	// The interrupted collection's rows must have been rewritten, not merged
	// with the stale partial ones.
	var stale int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM vectors WHERE file_path = 'stale.go'`).Scan(&stale); err != nil {
		t.Fatalf("count stale: %v", err)
	}
	if stale != 0 {
		t.Errorf("%d rows from the interrupted attempt survived the resume", stale)
	}
	// The two collections that had completed must not have been touched again.
	stateAfter := migrationState(t, s2)
	for _, line := range stateBefore {
		if !contains(stateAfter, line) {
			t.Errorf("migration state for an already-imported collection changed: %q not in %v", line, stateAfter)
		}
	}
}

// The import decodes into a channel and writes in batches inside one
// transaction (see importBatchSize). A collection spanning several batches must
// come out identical to one that fits in a single batch — nothing dropped at a
// flush, nothing written twice.
func TestImportLegacyChromem_StreamsAcrossBatches(t *testing.T) {
	withImportBatchSize(t, 5)
	// 23 documents = four full batches and a partial one.
	legacy, want := legacyFixture(t, 2, 23, 8)

	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	defer s.Close()

	for name, docs := range want {
		assertDocsImportedExactly(t, s, name, docs)
	}
	for _, line := range migrationState(t, s) {
		if !strings.HasSuffix(line, " 23") {
			t.Errorf("migration_state records %q, want 23 documents", line)
		}
	}
}

// The flush at the end of the loop and the flush after it must not both write
// the final batch. A collection whose size is an exact multiple of the batch is
// where that double-write would show up.
func TestImportLegacyChromem_ExactBatchMultiple(t *testing.T) {
	for _, tc := range []struct{ batch, docs int }{
		{batch: 5, docs: 15}, // three full batches, nothing left over
		{batch: 6, docs: 6},  // exactly one batch
		{batch: 9, docs: 4},  // one partial batch, never reaches a flush point
	} {
		t.Run(fmt.Sprintf("batch%d_docs%d", tc.batch, tc.docs), func(t *testing.T) {
			withImportBatchSize(t, tc.batch)
			legacy, want := legacyFixture(t, 1, tc.docs, 8)

			s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
			if err != nil {
				t.Fatalf("OpenWith: %v", err)
			}
			defer s.Close()

			for name, docs := range want {
				assertDocsImportedExactly(t, s, name, docs)
			}
			state := migrationState(t, s)
			if len(state) != 1 || !strings.HasSuffix(state[0], fmt.Sprintf(" %d", tc.docs)) {
				t.Errorf("migration_state = %v, want one row recording %d documents", state, tc.docs)
			}
		})
	}
}

// The resume path with a collection that spans several batches: the batched
// writer must not turn a partially written collection into a duplicated one.
// The rows it already wrote are inside the transaction that never committed, so
// what a real crash leaves behind is the stale-row state built here by hand.
func TestImportLegacyChromem_ResumesMidCollectionAcrossBatches(t *testing.T) {
	withImportBatchSize(t, 4)
	const docsPer = 18 // four full batches and a partial one
	legacy, want := legacyFixture(t, 2, docsPer, 8)
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)

	// Two batches' worth of rows for the first collection, under a file_path
	// that does not exist in the fixture, and NO migration_state row.
	interrupted := names[0]
	ctx := context.Background()
	collID, err := s.ensureCollection(ctx, interrupted)
	if err != nil {
		t.Fatalf("ensure %s: %v", interrupted, err)
	}
	for _, d := range want[interrupted][:8] {
		if _, err := s.db.Exec(upsertVectorSQL, collID, d.ID, "stale.go", 0, 0, "", "", "",
			floatsBlob(d.Embedding)); err != nil {
			t.Fatalf("partial write: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("resume open: %v", err)
	}
	defer s2.Close()

	for name, docs := range want {
		assertDocsImportedExactly(t, s2, name, docs)
	}
	var stale int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM vectors WHERE file_path = 'stale.go'`).Scan(&stale); err != nil {
		t.Fatalf("count stale: %v", err)
	}
	if stale != 0 {
		t.Errorf("%d rows from the interrupted attempt survived the resume", stale)
	}
}

// Cancelling the import must unwind rather than deadlock: the decode workers
// block on a channel send, and nothing may be left waiting on a writer that has
// gone away. A cancelled collection also commits nothing, so the next boot
// redoes it.
func TestImportCollection_CancelUnwinds(t *testing.T) {
	withImportBatchSize(t, 1000) // no flush before the cancellation lands
	legacy, want := legacyFixture(t, 1, 200, 8)
	dirs := collectionDirsByName(t, legacy)

	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var name string
	for n := range want {
		name = n
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.importCollection(ctx, dirs[name], name)
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			// It beat the cancellation — legitimate, and then it committed a
			// complete collection.
			return
		}
		if state := migrationState(t, s); len(state) != 0 {
			t.Errorf("a cancelled import left migration_state %v, want nothing committed", state)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("importCollection did not return after its context was cancelled — the decode pipeline deadlocked")
	}
}

// A namespace with no legacy directory imports nothing and says nothing.
func TestImportLegacyChromem_FreshInstall(t *testing.T) {
	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if got := s.ListCollections(); len(got) != 0 {
		t.Errorf("fresh install imported %+v", got)
	}
}

// A directory that is not a chromem collection (no metadata gob) is skipped
// rather than failing the boot.
func TestImportLegacyChromem_SkipsUnreadableCollectionDir(t *testing.T) {
	legacy, _ := legacyFixture(t, 1, 3, 8)
	if err := os.MkdirAll(filepath.Join(legacy, "not-a-collection"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "not-a-collection", "junk.gob"), []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if got := s.ListCollections(); len(got) != 1 {
		t.Errorf("imported %+v, want exactly the one real collection", got)
	}
}

// Once the legacy tree has been reclaimed (the "Legacy chromem data" category
// on the Resources screen), the next boot must be a normal boot: the gob files
// are gone, migration_state still says every collection was dealt with, and
// nothing is re-imported or reported as an error.
func TestOpenWith_ToleratesADeletedLegacyDir(t *testing.T) {
	legacy, _ := legacyFixture(t, 3, 4, 8)
	dir := t.TempDir()

	s, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	before := migrationState(t, s)
	if len(before) != 3 {
		t.Fatalf("migration_state has %d rows after the import, want 3", len(before))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// …the admin reclaims the gob tree.
	if err := os.RemoveAll(legacy); err != nil {
		t.Fatalf("remove legacy tree: %v", err)
	}

	s2, err := OpenWith(Options{Dir: dir, LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("open after the legacy tree was reclaimed: %v", err)
	}
	defer s2.Close()

	if after := migrationState(t, s2); !equalStrings(before, after) {
		t.Errorf("migration state changed:\nbefore=%v\nafter=%v", before, after)
	}
	cols := s2.ListCollections()
	if len(cols) != 3 {
		t.Fatalf("%d collections after reopen, want 3 — the imported data must be untouched", len(cols))
	}
	for _, ci := range cols {
		if ci.Documents != 4 {
			t.Errorf("collection %s has %d documents, want 4", ci.Name, ci.Documents)
		}
	}
	if migrated, ok := s2.MigratedCollections(); !ok || len(migrated) != 3 {
		t.Errorf("MigratedCollections = %v, %v; want 3 entries and ok", migrated, ok)
	}
}

func TestMigratedCollections(t *testing.T) {
	legacy, want := legacyFixture(t, 2, 5, 8)
	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, ok := s.MigratedCollections()
	if !ok {
		t.Fatal("MigratedCollections reported unknown on a healthy store")
	}
	if len(got) != len(want) {
		t.Fatalf("MigratedCollections = %v, want %d entries", got, len(want))
	}
	for name := range want {
		if got[name] != 5 {
			t.Errorf("collection %s recorded %d documents, want 5", name, got[name])
		}
	}

	// A closed store must answer "unknown", not "nothing was migrated" — the
	// caller deciding whether to delete 2.5 GB of gob files reads an empty map
	// as "everything is imported".
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := s.MigratedCollections(); ok {
		t.Error("a closed store reported a usable migration record")
	}
}

func TestLegacyMigrationStatus(t *testing.T) {
	legacy, _ := legacyFixture(t, 3, 4, 8)
	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	migrated, ok := s.MigratedCollections()
	if !ok {
		t.Fatal("migration record unavailable")
	}

	st, err := LegacyMigrationStatus(legacy, migrated)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Complete() {
		t.Fatalf("status = %+v, want complete after a full import", st)
	}
	if st.Collections != 3 || st.Migrated != 3 || st.Documents != 12 {
		t.Errorf("status = %+v, want 3/3 collections and 12 documents", st)
	}

	// A collection that appeared after the import — the shape a mid-flight
	// migration has — makes the whole namespace ineligible.
	writeCollectionMeta(t, filepath.Join(legacy, "aabbccdd"), "project_notyet")
	st, err = LegacyMigrationStatus(legacy, migrated)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Complete() || st.Collections != 4 || st.Migrated != 3 {
		t.Errorf("status = %+v, want 3 of 4 collections migrated and not complete", st)
	}

	// A directory the importer could not read is not a collection it took, so
	// it holds data that exists nowhere else.
	fresh, _ := legacyFixture(t, 1, 2, 8)
	freshStore, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: fresh})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer freshStore.Close()
	freshMigrated, _ := freshStore.MigratedCollections()
	if err := os.MkdirAll(filepath.Join(fresh, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err = LegacyMigrationStatus(fresh, freshMigrated)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Complete() || len(st.Unreadable) != 1 {
		t.Errorf("status = %+v, want an unreadable directory and not complete", st)
	}

	// An empty namespace directory is not "complete": nothing was migrated
	// from it, so there is nothing there whose deletion is safe by that proof.
	empty := t.TempDir()
	st, err = LegacyMigrationStatus(empty, migrated)
	if err != nil {
		t.Fatalf("status on an empty dir: %v", err)
	}
	if st.Complete() || st.Collections != 0 {
		t.Errorf("status = %+v, want empty and not complete", st)
	}

	if _, err := LegacyMigrationStatus(filepath.Join(t.TempDir(), "gone"), migrated); err == nil {
		t.Error("a missing legacy directory should be an error, not a silent complete")
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// assertDocsImportedExactly checks that every document of a chromem collection
// survived the import verbatim: metadata, chunk text, and the embedding
// byte-for-byte. It also pins the document COUNT, so a streaming import that
// dropped or duplicated a batch fails here rather than silently.
func assertDocsImportedExactly(t *testing.T, s *Store, name string, docs []chromem.Document) {
	t.Helper()
	collID, ok, err := s.collectionID(context.Background(), name)
	if err != nil || !ok {
		t.Fatalf("collection %s missing after import (err=%v)", name, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vectors WHERE collection_id = ?`, collID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", name, err)
	}
	if n != len(docs) {
		t.Fatalf("collection %s holds %d rows, want %d", name, n, len(docs))
	}
	for _, d := range docs {
		var (
			filePath, chunkType, symbolName, language, content string
			startLine, endLine                                 int
			blob                                               []byte
		)
		err := s.db.QueryRow(`
			SELECT v.file_path, v.start_line, v.end_line, v.chunk_type, v.symbol_name,
			       v.language, v.embedding, c.content
			  FROM vectors v JOIN vector_contents c
			    ON c.collection_id = v.collection_id AND c.doc_id = v.doc_id
			 WHERE v.collection_id = ? AND v.doc_id = ?`, collID, d.ID).
			Scan(&filePath, &startLine, &endLine, &chunkType, &symbolName, &language, &blob, &content)
		if err != nil {
			t.Fatalf("document %s of %s: %v", d.ID, name, err)
		}
		if filePath != d.Metadata["file_path"] || chunkType != d.Metadata["chunk_type"] ||
			symbolName != d.Metadata["symbol_name"] || language != d.Metadata["language"] {
			t.Fatalf("document %s metadata mismatch: %q/%q/%q/%q", d.ID, filePath, chunkType, symbolName, language)
		}
		if strconv.Itoa(startLine) != d.Metadata["start_line"] || strconv.Itoa(endLine) != d.Metadata["end_line"] {
			t.Fatalf("document %s lines = %d-%d, want %s-%s", d.ID,
				startLine, endLine, d.Metadata["start_line"], d.Metadata["end_line"])
		}
		if content != d.Content {
			t.Fatalf("document %s content = %q, want %q", d.ID, content, d.Content)
		}
		if wantBlob := floatsBlob(d.Embedding); string(blob) != string(wantBlob) {
			t.Fatalf("document %s embedding is not byte-exact", d.ID)
		}
	}
}

// withImportBatchSize shrinks the import write batch for one test. Exercising
// batch boundaries at the production size would mean fixtures of thousands of
// gob files per case; the boundary logic is the same at any size.
func withImportBatchSize(t *testing.T, n int) {
	t.Helper()
	prev := importBatchSize
	importBatchSize = n
	t.Cleanup(func() { importBatchSize = prev })
}

// writeCollectionMeta creates a collection directory holding nothing but the
// metadata gob — enough for the tree to name it, which is all the migration
// check reads.
func writeCollectionMeta(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	f, err := os.Create(filepath.Join(dir, chromemMetadataFile))
	if err != nil {
		t.Fatalf("create metadata gob: %v", err)
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(chromemCollectionMeta{Name: name}); err != nil {
		t.Fatalf("encode metadata gob: %v", err)
	}
}

func migrationState(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT collection_name, migrated_at, docs FROM migration_state ORDER BY collection_name`)
	if err != nil {
		t.Fatalf("read migration_state: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, at string
		var docs int
		if err := rows.Scan(&name, &at, &docs); err != nil {
			t.Fatalf("scan migration_state: %v", err)
		}
		out = append(out, fmt.Sprintf("%s %s %d", name, at, docs))
	}
	return out
}

// collectionDirsByName maps chromem collection names to their directories by
// reading each directory's metadata gob — the same way the importer does.
func collectionDirsByName(t *testing.T, legacy string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(legacy)
	if err != nil {
		t.Fatalf("read %s: %v", legacy, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(legacy, e.Name())
		name, err := readCollectionName(dir)
		if err != nil {
			continue
		}
		out[name] = dir
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
