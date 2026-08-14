package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

func TestClean_RemovesOrphanCollection(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.index(t, "/dead/project")
	f.addProject(t, "/live/project")

	a := f.analyze(t)
	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0]; got.DeletedCount != 1 || got.FailedCount != 0 {
		t.Fatalf("clean result = %+v, want 1 deleted / 0 failed", got)
	}
	if res.ReclaimedBytes <= 0 {
		t.Errorf("reclaimed = %d, want > 0", res.ReclaimedBytes)
	}
	if _, ok := f.store.CollectionSizeBytes("/dead/project"); ok {
		t.Error("the orphaned collection is still in the store after the clean")
	}

	// The live project must be untouched and still searchable.
	after := category(t, f.analyze(t), CatOrphanCollections)
	if after.ItemCount != 0 {
		t.Errorf("orphans after clean = %d, want 0", after.ItemCount)
	}
	if n := f.store.Count("/live/project"); n != 1 {
		t.Errorf("live project has %d documents after clean, want 1", n)
	}
}

// The realistic race: the admin deletes a project, analyzes, then re-adds and
// re-indexes it before pressing confirm. The analysis still calls that
// collection an orphan; re-validation is what stops us wiping a fresh index.
func TestClean_SkipsCollectionWhoseProjectCameBack(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")

	a := f.analyze(t)
	if got := category(t, a, CatOrphanCollections).ItemCount; got != 1 {
		t.Fatalf("expected 1 orphan at analysis time, got %d", got)
	}

	// …and now the project exists again.
	f.addProject(t, "/dead/project")

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	got := res.Categories[0]
	if got.DeletedCount != 0 || got.SkippedCount != 1 {
		t.Fatalf("clean result = %+v, want 0 deleted / 1 skipped", got)
	}
	if n := f.store.Count("/dead/project"); n != 1 {
		t.Errorf("the re-created project lost its %d documents — re-validation failed", n)
	}
}

func TestClean_SkipsRepoCheckoutThatCameBack(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.dataDir, "repos")
	hash := projects.HashPath("/back/project")
	if err := os.MkdirAll(filepath.Join(root, hash), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	a := f.analyze(t)
	if got := category(t, a, CatOrphanRepos).ItemCount; got != 1 {
		t.Fatalf("expected 1 orphan checkout, got %d", got)
	}
	f.addProject(t, "/back/project")

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanRepos})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0]; got.SkippedCount != 1 || got.DeletedCount != 0 {
		t.Errorf("clean result = %+v, want the re-created checkout skipped", got)
	}
	if _, err := os.Stat(filepath.Join(root, hash)); err != nil {
		t.Errorf("the re-created checkout was deleted: %v", err)
	}
}

func TestClean_NeverTouchesTheActiveNamespace(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.addProject(t, "/live/project")
	stale := filepath.Join(f.cfg.ChromaPersistDir, "ollama", "old-model", "aabbccdd")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	a := f.analyze(t)
	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatStaleNamespaces}); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if _, err := os.Stat(f.store.BaseDir()); err != nil {
		t.Fatalf("the ACTIVE namespace was deleted: %v", err)
	}
	if n := f.store.Count("/live/project"); n != 1 {
		t.Errorf("live project has %d documents, want 1", n)
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Errorf("the stale namespace survived (err=%v)", err)
	}
}

// The live SQLite database must survive a stale-namespace clean, and so must
// the chromem directory it was imported from — that is the rollback path.
func TestClean_KeepsTheActiveNamespaceInBothTrees(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.addProject(t, "/live/project")

	staleVectors := filepath.Join(f.cfg.VectorsDir, "voyage", "voyage-code-3")
	if err := os.MkdirAll(staleVectors, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleVectors, "vectors.db"), make([]byte, 8), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := f.analyze(t)
	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatStaleNamespaces})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0]; got.DeletedCount != 1 {
		t.Fatalf("clean result = %+v, want the abandoned vectors namespace deleted", got)
	}
	if _, err := os.Stat(staleVectors); !os.IsNotExist(err) {
		t.Errorf("the abandoned vectors namespace survived (err=%v)", err)
	}
	if _, err := os.Stat(f.store.DBPath()); err != nil {
		t.Fatalf("the ACTIVE vector database was deleted: %v", err)
	}
	if _, err := os.Stat(f.store.LegacyChromaDir()); err != nil {
		t.Fatalf("the legacy chromem directory of the ACTIVE namespace was deleted: %v", err)
	}
	if n := f.store.Count("/live/project"); n != 1 {
		t.Errorf("live project has %d documents, want 1", n)
	}
}

func TestClean_PartialFailureIsReportedNotRaised(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics required")
	}
	f := newFixture(t)
	root := filepath.Join(f.dataDir, "repos")
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "aaaaaaaaaaaaaaaa"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bbbbbbbbbbbbbbbb"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	a := f.analyze(t)
	// Make the "locked" directory undeletable by removing write on it.
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanRepos})
	if err != nil {
		t.Fatalf("clean returned an error instead of reporting the failure: %v", err)
	}
	got := res.Categories[0]
	if got.FailedCount != 1 {
		t.Errorf("failed count = %d, want 1 (failures: %+v)", got.FailedCount, got.Failures)
	}
	if got.DeletedCount != 1 {
		t.Errorf("deleted count = %d, want 1 — one bad directory must not stop the others", got.DeletedCount)
	}
}

func TestClean_StaleJobsUsesAFreshCutoff(t *testing.T) {
	f := newFixture(t)
	insertJob(t, f.db, "index_repo", "completed", "old", f.now.Add(-60*24*time.Hour))
	insertJob(t, f.db, "index_repo", "running", "live", f.now.Add(-60*24*time.Hour))

	a := f.analyze(t)
	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatStaleJobs})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0].DeletedCount; got != 1 {
		t.Errorf("deleted jobs = %d, want 1", got)
	}
	var remaining int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&remaining); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if remaining != 1 {
		t.Errorf("%d job rows remain, want 1 — a running job must never be deleted", remaining)
	}
}

func TestClean_RejectsExpiredUnknownAndEmpty(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")
	a := f.analyze(t)

	if _, err := f.svc.Clean(context.Background(), "nope", []CategoryID{CatOrphanCollections}); !errors.Is(err, ErrAnalysisExpired) {
		t.Errorf("unknown id error = %v, want ErrAnalysisExpired", err)
	}
	if _, err := f.svc.Clean(context.Background(), a.ID, nil); !errors.Is(err, ErrNoCategories) {
		t.Errorf("empty categories error = %v, want ErrNoCategories", err)
	}
	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{"made_up"}); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("bad category error = %v, want ErrUnknownCategory", err)
	}

	// Past the TTL the token stops being redeemable.
	f.now = f.now.Add(analysisTTL + time.Minute)
	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections}); !errors.Is(err, ErrAnalysisExpired) {
		t.Errorf("expired id error = %v, want ErrAnalysisExpired", err)
	}
}

// A second clean against the same id must fail: the picture it described no
// longer matches the server.
func TestClean_InvalidatesTheAnalysis(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")
	a := f.analyze(t)

	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections}); err != nil {
		t.Fatalf("first clean: %v", err)
	}
	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections}); !errors.Is(err, ErrAnalysisExpired) {
		t.Errorf("second clean error = %v, want ErrAnalysisExpired", err)
	}
}

// An analysis id buys exactly one clean. Two concurrent requests carrying the
// same id must not both run: the deletions are idempotent so nothing breaks,
// but the second would report bytes the first already reclaimed.
func TestClean_AnalysisIsSingleUseUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/one")
	f.index(t, "/dead/two")
	a := f.analyze(t)

	const callers = 6
	var wg sync.WaitGroup
	results := make([]*CleanResult, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections})
		}()
	}
	wg.Wait()

	won := 0
	for i := range callers {
		switch {
		case errs[i] == nil:
			won++
		case errors.Is(errs[i], ErrAnalysisExpired):
		default:
			t.Errorf("caller %d got unexpected error %v", i, errs[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d callers succeeded, want exactly 1", won)
	}
}

// A selection the server cannot honour must not burn the analysis — the admin
// should be able to correct the request and clean without re-scanning.
func TestClean_BadSelectionLeavesTheAnalysisUsable(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")
	a := f.analyze(t)

	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{"nonsense"}); !errors.Is(err, ErrUnknownCategory) {
		t.Fatalf("bad category error = %v, want ErrUnknownCategory", err)
	}
	if _, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections}); err != nil {
		t.Errorf("the analysis was consumed by a rejected selection: %v", err)
	}
}

// Cleaning must act on every item found, not on the truncated sample the
// browser is sent.
func TestClean_DeletesBeyondTheSerialisedSample(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.dataDir, "repos")
	const n = maxItemsPerCategory + 7
	for i := range n {
		name := string(rune('a'+i/16)) + string(rune('a'+i%16))
		dir := filepath.Join(root, "0000000000000"+name+"0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	a := f.analyze(t)
	c := category(t, a, CatOrphanRepos)
	if c.ItemCount != n {
		t.Fatalf("item count = %d, want %d", c.ItemCount, n)
	}
	if !c.ItemsTruncated || len(c.Items) != maxItemsPerCategory {
		t.Fatalf("expected the sample to be truncated to %d, got %d (truncated=%v)",
			maxItemsPerCategory, len(c.Items), c.ItemsTruncated)
	}

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanRepos})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0].DeletedCount; got != n {
		t.Errorf("deleted = %d, want all %d — Clean must use the full list, not the UI sample", got, n)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d checkout directories remain, want 0", len(entries))
	}
}

func TestClean_UnknownVectorStoreDegradesGracefully(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")
	a := f.analyze(t)
	f.svc.d.VectorStore = nil

	res, err := f.svc.Clean(context.Background(), a.ID, []CategoryID{CatOrphanCollections})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got := res.Categories[0]; got.FailedCount != 1 || got.DeletedCount != 0 {
		t.Errorf("clean result = %+v, want the item reported as failed", got)
	}
	var _ vectorstore.Maintainer = f.store
}
