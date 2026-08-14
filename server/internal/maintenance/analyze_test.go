package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fixture wires a Service over a real (temp-dir) vector store and an in-memory
// database. Everything the scanners read is real: the store writes an actual
// SQLite database, projects rows go through the actual schema. Faking any of
// it would defeat the purpose — the bug this feature exists to clean up was a
// mismatch between what the code believed was on disk and what actually was.
type fixture struct {
	svc     *Service
	db      *sql.DB
	store   *vectorstore.Store
	cfg     *config.Config
	dataDir string
	// legacyNS / vectorsNS are the two directories of the ACTIVE namespace:
	// the chromem tree it was imported from and the SQLite database.
	legacyNS  string
	vectorsNS string
	now       time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dataDir := t.TempDir()

	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	chromaDir := filepath.Join(dataDir, "chroma")
	vectorsDir := filepath.Join(dataDir, "vectors")
	// The live namespace and the chromem directory it would have been
	// imported from — the production pairing, so the scanners are exercised
	// against both trees.
	legacyNS := filepath.Join(chromaDir, "ollama", "test-model")
	if err := os.MkdirAll(legacyNS, 0o755); err != nil {
		t.Fatalf("mkdir legacy namespace: %v", err)
	}
	vectorsNS := filepath.Join(vectorsDir, "ollama", "test-model")
	store, err := vectorstore.OpenWith(vectorstore.Options{
		Dir:             vectorsNS,
		LegacyChromaDir: legacyNS,
	})
	if err != nil {
		t.Fatalf("open vector store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := &config.Config{
		SQLitePath:        filepath.Join(dataDir, "cix.db"),
		ChromaPersistDir:  chromaDir,
		VectorsDir:        vectorsDir,
		WorkspacesDataDir: dataDir,
		GGUFCacheDir:      filepath.Join(dataDir, "models"),
	}

	f := &fixture{
		db:        database,
		store:     store,
		cfg:       cfg,
		dataDir:   dataDir,
		legacyNS:  legacyNS,
		vectorsNS: vectorsNS,
		now:       time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
	}
	f.svc = New(Deps{
		DB:  database,
		Cfg: cfg,
		// A real queue service, never Start()ed: we only want its SQL, not
		// its worker pool.
		Jobs:                   jobs.New(database, jobs.Options{Concurrency: 1, PollEvery: time.Hour}),
		VectorStore:            store,
		Logger:                 nil,
		ActiveChromaComponents: func() []string { return []string{"ollama", "test-model"} },
		ActiveGGUFCacheDir:     func() string { return cfg.GGUFCacheDir },
		ActiveModelRepo:        func() string { return "acme/active-model" },
		Now:                    func() time.Time { return f.now },
	})
	return f
}

// index writes one chunk for a project path, which creates the collection.
func (f *fixture) index(t *testing.T, projectPath string) {
	t.Helper()
	err := f.store.UpsertChunks(context.Background(), projectPath,
		[]vectorstore.Chunk{{Content: "package main", FilePath: "main.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0, 0}},
	)
	if err != nil {
		t.Fatalf("upsert %s: %v", projectPath, err)
	}
}

// addProject inserts a real projects row.
func (f *fixture) addProject(t *testing.T, hostPath string) {
	t.Helper()
	if _, err := projects.Create(context.Background(), f.db, projects.CreateRequest{
		HostPath: hostPath,
	}); err != nil {
		t.Fatalf("create project %s: %v", hostPath, err)
	}
}

// reopenStore closes and reopens the vector store at the same two directories.
// That is the only way to trigger the legacy import, because production
// imports at open — so a test seeds the chromem tree first and reopens.
func (f *fixture) reopenStore(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err := vectorstore.OpenWith(vectorstore.Options{
		Dir:             f.vectorsNS,
		LegacyChromaDir: f.legacyNS,
	})
	if err != nil {
		t.Fatalf("reopen vector store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.store = store
	f.svc.d.VectorStore = store
}

// legacyChromemDoc / legacyChromemMeta mirror what chromem-go wrote, which is
// what the importer decodes. gob matches by field name, so writing them from
// here produces a tree the real importer accepts — the same trick the
// production decoder uses in reverse.
type legacyChromemMeta struct {
	Name     string
	Metadata map[string]string
}

type legacyChromemDoc struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string
}

// writeLegacyCollection writes one chromem collection directory for a project
// under the namespace dir, named the way chromem named them (the first 4 bytes
// of the collection name's SHA-256, hex).
func writeLegacyCollection(t *testing.T, nsDir, projectPath string, docs int) string {
	t.Helper()
	name := vectorstore.CollectionName(projectPath)
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(nsDir, hex.EncodeToString(sum[:4]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	writeGob(t, filepath.Join(dir, "00000000.gob"), legacyChromemMeta{
		Name:     name,
		Metadata: map[string]string{"hnsw:space": "cosine"},
	})
	for i := range docs {
		writeGob(t, filepath.Join(dir, fmt.Sprintf("%08d.gob", i+1)), legacyChromemDoc{
			ID:      fmt.Sprintf("doc%d", i),
			Content: fmt.Sprintf("chunk %d of %s", i, projectPath),
			Metadata: map[string]string{
				"file_path": "main.go", "start_line": "1", "end_line": "2",
				"chunk_type": "function", "symbol_name": "Main", "language": "go",
			},
			Embedding: []float32{1, 0, 0},
		})
	}
	return dir
}

func writeGob(t *testing.T, path string, v any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(v); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

func (f *fixture) analyze(t *testing.T) *Analysis {
	t.Helper()
	a, err := f.svc.Analyze(context.Background())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return a
}

func category(t *testing.T, a *Analysis, id CategoryID) Category {
	t.Helper()
	for _, c := range a.Categories {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("analysis has no category %q", id)
	return Category{}
}

func TestScanOrphanCollections_FindsOnlyDeadProjects(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.index(t, "/dead/project")
	f.addProject(t, "/live/project")

	c := category(t, f.analyze(t), CatOrphanCollections)
	if c.ItemCount != 1 {
		t.Fatalf("orphan collections = %d, want 1 (items: %+v)", c.ItemCount, c.Items)
	}
	if got, want := c.Items[0].Key, vectorstore.CollectionName("/dead/project"); got != want {
		t.Errorf("orphan key = %q, want %q", got, want)
	}
	if c.SizeBytes <= 0 {
		t.Errorf("orphan size = %d, want > 0 — the collection's rows should have been measured", c.SizeBytes)
	}
	if !c.DefaultSelected {
		t.Error("orphan collections should be pre-selected when no job is running")
	}
}

// The category is disabled wholesale while a clone/index job is active,
// because an orphan cannot be matched back to the job that might be writing
// to it (one hash is MD5, the other SHA-1, and the joining row is gone).
func TestScanOrphanCollections_DisabledWhileJobActive(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")
	insertJob(t, f.db, "index_repo", "running", "index:abc", f.now)

	a := f.analyze(t)
	c := category(t, a, CatOrphanCollections)
	if c.ItemCount != 1 {
		t.Fatalf("orphan collections = %d, want 1", c.ItemCount)
	}
	if c.DefaultSelected {
		t.Error("orphan collections must not be pre-selected while an index job is running")
	}

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	got := res.Categories[0]
	if got.DeletedCount != 0 || got.SkippedCount != 1 {
		t.Errorf("clean while job active = %+v, want 0 deleted / 1 skipped", got)
	}
}

func TestScanOrphanCollections_IgnoresForeignCollections(t *testing.T) {
	f := newFixture(t)
	// A collection this codebase did not create must never be touched, and
	// must be called out rather than silently ignored. Nothing in the server
	// can create one, so the store is stubbed for this single case.
	f.svc.d.VectorStore = foreignCollections{
		Maintainer: f.store,
		extra: []vectorstore.CollectionInfo{
			{Name: "someone_elses_data", Documents: 3, SizeBytes: 4096},
		},
	}
	a := f.analyze(t)
	c := category(t, a, CatOrphanCollections)
	if c.ItemCount != 0 {
		t.Errorf("orphan collections = %d, want 0 — a foreign collection is not ours to delete", c.ItemCount)
	}
	if len(a.Warnings) == 0 {
		t.Error("an unrecognised collection should surface a warning")
	}
}

// foreignCollections decorates a real store with collections it could not
// have created itself.
type foreignCollections struct {
	vectorstore.Maintainer
	extra []vectorstore.CollectionInfo
}

func (f foreignCollections) ListCollections() []vectorstore.CollectionInfo {
	return append(f.Maintainer.ListCollections(), f.extra...)
}

func TestScanOrphanRepos(t *testing.T) {
	f := newFixture(t)
	f.addProject(t, "/live/project")

	root := filepath.Join(f.dataDir, "repos")
	liveHash := projects.HashPath("/live/project")
	for _, name := range []string{liveHash, "deadbeefdeadbeef", "ecc2afe1-3a8d-4fbd-946e-dc6985205d7c"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "f.txt"), []byte("xxxx"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	c := category(t, f.analyze(t), CatOrphanRepos)
	if c.ItemCount != 2 {
		t.Fatalf("orphan repos = %d, want 2 (items: %+v)", c.ItemCount, c.Items)
	}
	keys := map[string]string{}
	for _, it := range c.Items {
		keys[it.Key] = it.Detail
	}
	if _, ok := keys[liveHash]; ok {
		t.Error("the live checkout was reported as an orphan")
	}
	if _, ok := keys["deadbeefdeadbeef"]; !ok {
		t.Error("a dead path_hash checkout was not reported")
	}
	// A legacy UUID-named directory cannot be a live checkout — this server
	// only ever looks a checkout up by path_hash — so it is reclaimable, and
	// labelled so the admin knows why it looks different.
	if detail, ok := keys["ecc2afe1-3a8d-4fbd-946e-dc6985205d7c"]; !ok {
		t.Error("a legacy UUID-named checkout was not reported")
	} else if detail == "" {
		t.Error("the legacy checkout should carry an explanatory detail")
	}
}

func TestScanStaleNamespaces_SkipsActiveAndItsAncestors(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")

	// An abandoned namespace from a previous model, holding a collection dir.
	stale := filepath.Join(f.cfg.ChromaPersistDir, "ollama", "old-model", "aabbccdd")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "doc.gob"), []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := category(t, f.analyze(t), CatStaleNamespaces)
	if c.ItemCount != 1 {
		t.Fatalf("stale namespaces = %d, want 1 (items: %+v)", c.ItemCount, c.Items)
	}
	if got := filepath.Base(c.Items[0].Key); got != "old-model" {
		t.Errorf("stale namespace = %q, want the old-model directory", c.Items[0].Key)
	}
	if c.SizeBytes != 10 {
		t.Errorf("stale namespace size = %d, want 10", c.SizeBytes)
	}
}

// Both trees leak a namespace per abandoned model: the live SQLite databases
// under VectorsDir and the pre-migration chromem directories. Both are
// scanned, and the label says which tree an item came from.
func TestScanStaleNamespaces_CoversBothTrees(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")

	staleChroma := filepath.Join(f.cfg.ChromaPersistDir, "ollama", "old-model", "aabbccdd")
	if err := os.MkdirAll(staleChroma, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleChroma, "doc.gob"), []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	staleVectors := filepath.Join(f.cfg.VectorsDir, "voyage", "voyage-code-3")
	if err := os.MkdirAll(staleVectors, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleVectors, "vectors.db"), make([]byte, 20), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := category(t, f.analyze(t), CatStaleNamespaces)
	if c.ItemCount != 2 {
		t.Fatalf("stale namespaces = %d, want 2 (items: %+v)", c.ItemCount, c.Items)
	}
	labels := map[string]bool{}
	for _, it := range c.Items {
		labels[it.Label] = true
		if it.Key == f.store.BaseDir() || it.Key == f.store.LegacyChromaDir() {
			t.Fatalf("the ACTIVE namespace %q was listed as stale", it.Key)
		}
	}
	if !labels["chroma/ollama/old-model"] || !labels["vectors/voyage/voyage-code-3"] {
		t.Errorf("labels = %v, want one from each tree", labels)
	}
	if c.SizeBytes != 30 {
		t.Errorf("stale size = %d, want 30 (10 + 20)", c.SizeBytes)
	}
}

// With no known active namespace every directory looks stale — including the
// live one. Refusing to classify is the only safe answer.
func TestScanStaleNamespaces_RefusesWhenActiveUnknown(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	// Nothing left to identify the live namespace with.
	f.svc.d.ActiveChromaComponents = func() []string { return nil }
	f.svc.d.VectorStore = nil

	a := f.analyze(t)
	c := category(t, a, CatStaleNamespaces)
	if c.ItemCount != 0 {
		t.Errorf("stale namespaces = %d, want 0 when the active namespace is unknown", c.ItemCount)
	}
	if len(a.Warnings) == 0 {
		t.Error("refusing to classify namespaces should surface a warning")
	}
}

// Embeddings can be off — the provider then reports no storage components at
// all, while the server has still opened a real namespace directory. The
// vector store itself has to be the source of truth, or the live directory
// lands on the deletion list.
func TestScanStaleNamespaces_UsesTheStoreWhenProviderIsSilent(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.svc.d.ActiveChromaComponents = func() []string { return nil }

	stale := filepath.Join(f.cfg.ChromaPersistDir, "ollama", "old-model", "aabbccdd")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	c := category(t, f.analyze(t), CatStaleNamespaces)
	if c.ItemCount != 1 {
		t.Fatalf("stale namespaces = %d, want 1 (items: %+v)", c.ItemCount, c.Items)
	}
	for _, it := range c.Items {
		if it.Key == f.store.BaseDir() || it.Key == f.store.LegacyChromaDir() {
			t.Fatalf("the ACTIVE namespace %q was listed as stale", it.Key)
		}
	}
}

// Booting with the wrong embedding model opens a fresh namespace and makes the
// real index look abandoned. It still gets listed — it genuinely is unused —
// but it must not come pre-ticked.
func TestScanStaleNamespaces_NotPreSelectedWhenBiggerThanActive(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")

	big := filepath.Join(f.cfg.ChromaPersistDir, "ollama", "the-real-index", "aabbccdd")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(big, "doc.gob"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := f.analyze(t)
	c := category(t, a, CatStaleNamespaces)
	if c.ItemCount != 1 {
		t.Fatalf("stale namespaces = %d, want 1", c.ItemCount)
	}
	if c.DefaultSelected {
		t.Error("a namespace larger than the active one must not be pre-selected")
	}
	if len(a.Warnings) == 0 {
		t.Error("expected a warning about the oversized inactive namespace")
	}
}

// --------------------------------------------------------------------------
// legacy chromem data
// --------------------------------------------------------------------------

// rollbackWarning is the sentence the category must carry verbatim. It is the
// only thing standing between an admin and an irreversible decision, so the
// test asserts on the text rather than on "the description is non-empty".
const rollbackWarning = "Deleting this data is irreversible and removes the ability to roll back to a " +
	"pre-SQLite-vector-store server version. Make sure the new version has been running fine before confirming."

func TestScanLegacyChromem_OffersAFullyMigratedNamespace(t *testing.T) {
	f := newFixture(t)
	writeLegacyCollection(t, f.legacyNS, "/legacy/one", 3)
	writeLegacyCollection(t, f.legacyNS, "/legacy/two", 2)
	f.reopenStore(t)

	c := category(t, f.analyze(t), CatLegacyChromem)
	if c.ItemCount != 1 {
		t.Fatalf("legacy chromem items = %d, want 1 (items: %+v)", c.ItemCount, c.Items)
	}
	it := c.Items[0]
	if it.Key != f.legacyNS {
		t.Errorf("item key = %q, want the legacy namespace directory %q", it.Key, f.legacyNS)
	}
	if it.Label != filepath.Join("chroma", "ollama", "test-model") {
		t.Errorf("item label = %q, want it relative to the data directory", it.Label)
	}
	if !strings.Contains(it.Detail, "2 collections") || !strings.Contains(it.Detail, "5 documents") {
		t.Errorf("item detail = %q, want the collection and document counts", it.Detail)
	}
	if it.SizeBytes <= 0 {
		t.Errorf("item size = %d, want the size of the gob tree", it.SizeBytes)
	}
	if c.DefaultSelected {
		t.Error("legacy chromem data must be opt-in — giving up the rollback path is not a default")
	}
	if c.EstimatedRAMBytes != 0 {
		t.Errorf("estimated RAM = %d, want 0 — the gob files are not in memory", c.EstimatedRAMBytes)
	}
	if !strings.Contains(c.Description, rollbackWarning) {
		t.Errorf("description does not carry the rollback warning:\n%s", c.Description)
	}
}

// The dangerous state: a migration is still running (or was interrupted), so
// part of the tree is the only copy of that data. Not offered at all — a
// disabled row would still advertise gigabytes that are not garbage yet.
func TestScanLegacyChromem_NotOfferedWhenACollectionIsUnmigrated(t *testing.T) {
	f := newFixture(t)
	writeLegacyCollection(t, f.legacyNS, "/legacy/one", 3)
	f.reopenStore(t)
	// A collection that appeared after the import — nothing recorded it.
	writeLegacyCollection(t, f.legacyNS, "/legacy/mid-flight", 2)

	a := f.analyze(t)
	c := category(t, a, CatLegacyChromem)
	if c.ItemCount != 0 {
		t.Fatalf("legacy chromem items = %d, want 0 while a collection is unmigrated", c.ItemCount)
	}
	if !warned(a, "1 of 2 collections") {
		t.Errorf("expected a warning naming the incomplete migration, got %v", a.Warnings)
	}
}

// A directory the importer could not read was never imported, so the tree
// still holds data that exists nowhere else.
func TestScanLegacyChromem_NotOfferedWithUnreadableCollections(t *testing.T) {
	f := newFixture(t)
	writeLegacyCollection(t, f.legacyNS, "/legacy/one", 3)
	f.reopenStore(t)
	if err := os.MkdirAll(filepath.Join(f.legacyNS, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := f.analyze(t)
	if got := category(t, a, CatLegacyChromem).ItemCount; got != 0 {
		t.Fatalf("legacy chromem items = %d, want 0 when part of the tree is unreadable", got)
	}
	if !warned(a, "not readable chromem collections") {
		t.Errorf("expected a warning about the unreadable directory, got %v", a.Warnings)
	}
}

// Without the database there is no proof the gob files are redundant — they
// may be the whole index.
func TestScanLegacyChromem_NotOfferedWhenTheVectorDatabaseIsMissing(t *testing.T) {
	f := newFixture(t)
	writeLegacyCollection(t, f.legacyNS, "/legacy/one", 3)
	f.reopenStore(t)
	f.svc.d.VectorStore = storeAtBase{Maintainer: f.store, base: t.TempDir()}

	a := f.analyze(t)
	if got := category(t, a, CatLegacyChromem).ItemCount; got != 0 {
		t.Fatalf("legacy chromem items = %d, want 0 without a vectors.db to prove the import", got)
	}
	if !warned(a, "to prove it has been imported") {
		t.Errorf("expected a warning about the missing database, got %v", a.Warnings)
	}
}

// The migration record is the whole proof; "cannot read it" must never be read
// as "nothing to preserve".
func TestScanLegacyChromem_NotOfferedWhenTheMigrationRecordIsUnknown(t *testing.T) {
	f := newFixture(t)
	writeLegacyCollection(t, f.legacyNS, "/legacy/one", 3)
	f.reopenStore(t)
	f.svc.d.VectorStore = unknownMigrationRecord{Maintainer: f.store}

	a := f.analyze(t)
	if got := category(t, a, CatLegacyChromem).ItemCount; got != 0 {
		t.Fatalf("legacy chromem items = %d, want 0 when the migration record is unreadable", got)
	}
	if !warned(a, "migration record could not be read") {
		t.Errorf("expected a warning about the unreadable migration record, got %v", a.Warnings)
	}
}

// A server that never had chromem data says nothing at all: an empty category
// and, crucially, no warning — "no legacy tree" is the normal state of every
// install made after the migration.
func TestScanLegacyChromem_SilentOnAFreshInstall(t *testing.T) {
	f := newFixture(t)
	a := f.analyze(t)
	if got := category(t, a, CatLegacyChromem).ItemCount; got != 0 {
		t.Fatalf("legacy chromem items = %d on a fresh install, want 0", got)
	}
	for _, w := range a.Warnings {
		if strings.Contains(w, "legacy chromem") {
			t.Errorf("fresh install warned about legacy data: %q", w)
		}
	}

	// …and the same once the directory is gone entirely (post-clean).
	if err := os.RemoveAll(f.legacyNS); err != nil {
		t.Fatal(err)
	}
	a = f.analyze(t)
	if got := category(t, a, CatLegacyChromem).ItemCount; got != 0 {
		t.Fatalf("legacy chromem items = %d with no directory at all, want 0", got)
	}
	for _, w := range a.Warnings {
		if strings.Contains(w, "legacy chromem") {
			t.Errorf("missing legacy directory warned: %q", w)
		}
	}
}

// storeAtBase reports a different namespace directory than the store actually
// opened — the shape of a vectors.db that has gone missing.
type storeAtBase struct {
	vectorstore.Maintainer
	base string
}

func (s storeAtBase) BaseDir() string { return s.base }

// unknownMigrationRecord is a store whose migration_state cannot be read.
type unknownMigrationRecord struct {
	vectorstore.Maintainer
}

func (unknownMigrationRecord) MigratedCollections() (map[string]int, bool) { return nil, false }

func warned(a *Analysis, substr string) bool {
	for _, w := range a.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestScanStaleJobs_OnlyOldTerminalRows(t *testing.T) {
	f := newFixture(t)
	old := f.now.Add(-60 * 24 * time.Hour)
	insertJob(t, f.db, "index_repo", "completed", "a", old)
	insertJob(t, f.db, "index_repo", "failed", "b", old)
	insertJob(t, f.db, "index_repo", "completed", "c", f.now.Add(-time.Hour))
	insertJob(t, f.db, "index_repo", "pending", "d", old)

	c := category(t, f.analyze(t), CatStaleJobs)
	if c.ItemCount != 1 {
		t.Fatalf("stale jobs categories = %d, want 1 aggregate item", c.ItemCount)
	}
	if c.Items[0].Detail != "2 rows" {
		t.Errorf("stale jobs detail = %q, want \"2 rows\" (recent and non-terminal rows must be excluded)", c.Items[0].Detail)
	}
}

func TestScanUnusedModels_KeepsActiveAndInFlight(t *testing.T) {
	f := newFixture(t)
	cache := f.cfg.GGUFCacheDir
	write := func(repo, file string, mod time.Time) string {
		dir := filepath.Join(cache, repo)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte("weights"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		return p
	}
	settled := f.now.Add(-time.Hour)
	write("acme__active-model", "m.gguf", settled)
	write("acme__old-model", "m.gguf", settled)
	write("acme__downloading", "m.gguf", f.now.Add(-time.Second))
	partial := write("acme__partial", "m.gguf", settled)
	if err := os.WriteFile(partial+".partial", []byte("x"), 0o644); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	c := category(t, f.analyze(t), CatUnusedModels)
	if c.ItemCount != 1 {
		t.Fatalf("unused models = %d, want 1 (items: %+v)", c.ItemCount, c.Items)
	}
	if c.Items[0].Label != "acme/old-model" {
		t.Errorf("unused model = %q, want acme/old-model", c.Items[0].Label)
	}
	if c.DefaultSelected {
		t.Error("unused models must not be pre-selected — re-downloading is expensive")
	}
	if !c.Destructive {
		t.Error("unused models must be flagged destructive so the dialog warns")
	}
}

func TestAnalyze_TotalsAndSample(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/one")
	f.index(t, "/dead/two")

	a := f.analyze(t)
	if a.ID == "" || a.ExpiresAt.IsZero() {
		t.Fatalf("analysis missing id/expiry: %+v", a)
	}
	var sum int64
	for _, c := range a.Categories {
		sum += c.SizeBytes
	}
	if a.TotalReclaimableBytes != sum {
		t.Errorf("total = %d, want the sum of categories %d", a.TotalReclaimableBytes, sum)
	}
	if len(a.Categories) != len(AllCategories) {
		t.Errorf("got %d categories, want all %d", len(a.Categories), len(AllCategories))
	}
}

// The schema declares items as a required array. Go serialises a nil slice as
// null, and a client that trusts the schema then calls .length on null and
// takes the page down — which is exactly what happened the first time this
// screen was opened in a browser. Empty categories are the common case, so
// this is the regression guard.
func TestAnalyze_EmptyCategoriesSerialiseAsArrays(t *testing.T) {
	f := newFixture(t)
	blob, err := json.Marshal(f.analyze(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Categories []struct {
			ID    string          `json:"id"`
			Items json.RawMessage `json:"items"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Categories) == 0 {
		t.Fatal("no categories in the serialised analysis")
	}
	for _, c := range decoded.Categories {
		if string(c.Items) == "null" {
			t.Errorf("category %q serialised items as null, want []", c.ID)
		}
	}
}

func insertJob(t *testing.T, db *sql.DB, jobType, status, id string, at time.Time) {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	var completed any
	if status == "completed" || status == "failed" {
		completed = ts
	}
	_, err := db.Exec(`
		INSERT INTO jobs (id, type, status, dedupe_key, payload, scheduled_at, completed_at, created_at)
		VALUES (?, ?, ?, NULL, '{}', ?, ?, ?)`,
		id, jobType, status, ts, completed, ts)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
}
