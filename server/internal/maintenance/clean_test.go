package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

func TestClean_RemovesOrphanCollectionFromMemoryAndDisk(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/live/project")
	f.index(t, "/dead/project")
	f.addProject(t, "/live/project")
	deadDir := f.store.CollectionDir("/dead/project")

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
	if _, err := os.Stat(deadDir); !os.IsNotExist(err) {
		t.Errorf("collection directory %s survived the clean (err=%v)", deadDir, err)
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
