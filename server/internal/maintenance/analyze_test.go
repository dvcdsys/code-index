package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fixture wires a Service over a real (temp-dir) vector store and an in-memory
// database. Everything the scanners read is real: chromem writes actual files,
// projects rows go through the actual schema. Faking any of it would defeat
// the purpose — the bug this feature exists to clean up was a mismatch between
// what the code believed was on disk and what actually was.
type fixture struct {
	svc     *Service
	db      *sql.DB
	store   *vectorstore.Store
	cfg     *config.Config
	dataDir string
	now     time.Time
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
	activeNS := filepath.Join(chromaDir, "ollama", "test-model")
	if err := os.MkdirAll(activeNS, 0o755); err != nil {
		t.Fatalf("mkdir namespace: %v", err)
	}
	store, err := vectorstore.Open(activeNS)
	if err != nil {
		t.Fatalf("open vector store: %v", err)
	}

	cfg := &config.Config{
		SQLitePath:        filepath.Join(dataDir, "cix.db"),
		ChromaPersistDir:  chromaDir,
		WorkspacesDataDir: dataDir,
		GGUFCacheDir:      filepath.Join(dataDir, "models"),
	}

	f := &fixture{
		db:      database,
		store:   store,
		cfg:     cfg,
		dataDir: dataDir,
		now:     time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
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

// index writes one chunk for a project path, which makes chromem create the
// collection and its directory.
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
		t.Errorf("orphan size = %d, want > 0 — the collection directory should have been measured", c.SizeBytes)
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
	// A directory that is not a project_ collection must never be touched.
	if err := os.MkdirAll(filepath.Join(f.store.BaseDir(), "deadbeef"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := category(t, f.analyze(t), CatOrphanCollections)
	if c.ItemCount != 0 {
		t.Errorf("orphan collections = %d, want 0 — a bare directory is not a chromem collection", c.ItemCount)
	}
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
	if got := c.Items[0].Key; got == f.store.BaseDir() {
		t.Fatalf("the ACTIVE namespace %q was listed as stale", got)
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
