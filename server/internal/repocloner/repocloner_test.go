package repocloner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeBareUpstream builds a bare git repo on disk that can be used as a
// `file://` remote for tests. Returns the upstream dir + a tiny helper
// that lets the test add commits to it without needing to run any
// shell. Single branch (the one passed via `branch`).
//
// The path ends in `.git` because repocloner.normaliseURL appends that
// suffix for GitHub URLs that don't already have it; the test URL we
// pass in (`file://<dir>`) goes through the same normalisation, so
// matching the on-disk path saves us from special-casing the test.
func makeBareUpstream(t *testing.T, branch string) (string, *commitWriter) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "upstream.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir upstream: %v", err)
	}
	upstream, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatalf("PlainInit upstream: %v", err)
	}
	// PlainInit creates a bare repo with HEAD pointing at refs/heads/master.
	// Override so the first push lands on the branch the test expects.
	if err := upstream.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD,
		plumbing.NewBranchReferenceName(branch),
	)); err != nil {
		t.Fatalf("set HEAD symref: %v", err)
	}
	return dir, &commitWriter{upstream: dir, branch: branch}
}

// commitWriter builds commits inside a worktree clone of the bare
// upstream and pushes them back. Tests use it to script "what did the
// remote look like at each indexed_sha point" without shelling out.
type commitWriter struct {
	upstream string
	branch   string
	// worktree is the local checkout we keep mutating. Lazily created.
	worktree string
}

func (w *commitWriter) ensureWorktree(t *testing.T) {
	t.Helper()
	if w.worktree != "" {
		return
	}
	w.worktree = t.TempDir()
	_, err := git.PlainClone(w.worktree, false, &git.CloneOptions{
		URL: "file://" + w.upstream,
	})
	// Empty upstream → CloneOptions can't resolve HEAD. Init the worktree
	// in place and wire a remote manually so the first push creates the
	// branch on the upstream.
	if err != nil {
		repo, ierr := git.PlainInit(w.worktree, false)
		if ierr != nil {
			t.Fatalf("init worktree: %v", ierr)
		}
		if _, rerr := repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{"file://" + w.upstream},
		}); rerr != nil {
			t.Fatalf("create remote: %v", rerr)
		}
		// Move HEAD to the test's branch so the first commit lands on it.
		if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
			plumbing.HEAD,
			plumbing.NewBranchReferenceName(w.branch),
		)); err != nil {
			t.Fatalf("set HEAD on worktree: %v", err)
		}
	}
}

// CommitFiles writes `files` (path → content) into the worktree,
// commits everything in one go, pushes, and returns the commit SHA.
// Deleting a file is expressed by content == "" — the helper unlinks
// it (mirroring how a real edit removes a file).
func (w *commitWriter) CommitFiles(t *testing.T, message string, files map[string]string) string {
	t.Helper()
	w.ensureWorktree(t)
	repo, err := git.PlainOpen(w.worktree)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for path, content := range files {
		full := filepath.Join(w.worktree, path)
		if content == "" {
			// Delete intent — unlink the file if it exists, then `git rm`
			// via wt.Remove so the index reflects it.
			_ = os.Remove(full)
			if _, err := wt.Remove(path); err != nil {
				// Already absent (path was never present) is fine.
				continue
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatalf("add %s: %v", path, err)
		}
	}
	sha, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + w.branch + ":refs/heads/" + w.branch),
		},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	return sha.String()
}

// Tag creates a lightweight tag at the current worktree HEAD and pushes it.
func (w *commitWriter) Tag(t *testing.T, name string) {
	t.Helper()
	w.ensureWorktree(t)
	repo, err := git.PlainOpen(w.worktree)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if _, err := repo.CreateTag(name, head.Hash(), nil); err != nil {
		t.Fatalf("create tag %s: %v", name, err)
	}
	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/tags/" + name + ":refs/tags/" + name),
		},
	}); err != nil {
		t.Fatalf("push tag %s: %v", name, err)
	}
}

// legacyClone reproduces what pre-NoTags server versions wrote to disk:
// go-git's clone default was Tags:AllTags, so a shallow clone carried a full
// snapshot per tag.
func legacyClone(t *testing.T, upstream, dir, branch string) {
	t.Helper()
	_, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:           "file://" + upstream,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.AllTags,
	})
	if err != nil {
		t.Fatalf("legacy clone: %v", err)
	}
}

// legacyFetchReset reproduces the old update path: fetch(Depth:1)+hard reset
// without NoTags, persisting one more snapshot pack per call.
func legacyFetchReset(t *testing.T, dir, branch string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	err = repo.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec("+refs/heads/" + branch + ":refs/remotes/origin/" + branch)},
		Depth:    1,
		Force:    true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		t.Fatalf("legacy fetch: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		t.Fatalf("legacy resolve remote ref: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("legacy worktree: %v", err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		t.Fatalf("legacy reset: %v", err)
	}
}

func tagRefCount(t *testing.T, dir string) int {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	refs, err := repo.References()
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	defer refs.Close()
	n := 0
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), tagRefPrefix) {
			n++
		}
		return nil
	})
	return n
}

// initialCloneFor runs a full CloneOrFetch (first-time clone path) so
// subsequent calls go through the reuse/fetch branch.
func initialCloneFor(t *testing.T, upstream, localDir, branch string) Result {
	t.Helper()
	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL: "file://" + upstream,
		Branch:    branch,
		LocalDir:  localDir,
	})
	if err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	return res
}

// sortedCopy is a deterministic-order copy for set comparisons.
func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

func TestCloneOrFetch_FirstClone_ChangesNil(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	w.CommitFiles(t, "init", map[string]string{
		"a.go": "package a\n",
	})

	local := filepath.Join(t.TempDir(), "clone")
	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: "",
	})
	if err != nil {
		t.Fatalf("CloneOrFetch: %v", err)
	}
	if res.HeadSHA == "" {
		t.Error("HeadSHA empty after first clone")
	}
	if res.Changes != nil {
		t.Errorf("Changes = %+v, want nil on first clone", res.Changes)
	}
	if res.NoChanges {
		t.Error("NoChanges true on first clone, want false")
	}
}

func TestCloneOrFetch_NoChanges_WhenHeadUnmoved(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	headSHA := w.CommitFiles(t, "init", map[string]string{
		"a.go": "package a\n",
	})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: headSHA,
	})
	if err != nil {
		t.Fatalf("CloneOrFetch (no-op): %v", err)
	}
	if !res.NoChanges {
		t.Errorf("NoChanges = false, want true when remote HEAD == local HEAD")
	}
	if res.HeadSHA != headSHA {
		t.Errorf("HeadSHA = %s, want %s (unchanged)", res.HeadSHA, headSHA)
	}
}

func TestCloneOrFetch_DiffSet_ModifyAddDelete(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	firstSHA := w.CommitFiles(t, "v1", map[string]string{
		"keep.go":    "package keep\n",
		"modify.go":  "package modify\n// v1\n",
		"delete.go":  "package delete\n",
		"subdir/old": "data\n",
	})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	// Now make a second commit upstream: modify one, add one, delete one.
	w.CommitFiles(t, "v2", map[string]string{
		"modify.go":  "package modify\n// v2\n",
		"new.go":     "package newfile\n",
		"delete.go":  "", // empty content → delete
		"subdir/old": "", // delete in subdir to cover non-root paths
	})

	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: firstSHA,
	})
	if err != nil {
		t.Fatalf("CloneOrFetch (v2): %v", err)
	}
	if res.NoChanges {
		t.Fatal("NoChanges = true, want false after upstream commit")
	}
	if res.Changes == nil {
		t.Fatal("Changes == nil, want populated ChangeSet")
	}
	got := struct {
		Modified, Added, Deleted []string
	}{
		sortedCopy(res.Changes.Modified),
		sortedCopy(res.Changes.Added),
		sortedCopy(res.Changes.Deleted),
	}
	if want := []string{"modify.go"}; !equalSlices(got.Modified, want) {
		t.Errorf("Modified = %v, want %v", got.Modified, want)
	}
	if want := []string{"new.go"}; !equalSlices(got.Added, want) {
		t.Errorf("Added = %v, want %v", got.Added, want)
	}
	if want := []string{"delete.go", "subdir/old"}; !equalSlices(got.Deleted, want) {
		t.Errorf("Deleted = %v, want %v", got.Deleted, want)
	}
}

func TestCloneOrFetch_PrevSHA_Unreachable_ReturnsNilChangeSet(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	w.CommitFiles(t, "init", map[string]string{
		"a.go": "package a\n",
	})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	w.CommitFiles(t, "v2", map[string]string{
		"b.go": "package b\n",
	})

	// Pass a SHA that was never fetched into local objects. Production
	// equivalent: operator ran `git gc` inside the clone dir, or a
	// force-push orphaned the indexed commit. The cloner must not blow
	// up — return Changes=nil so the caller falls back to full reindex.
	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: "0000000000000000000000000000000000000001",
	})
	if err != nil {
		t.Fatalf("CloneOrFetch (unreachable prev): %v", err)
	}
	if res.Changes != nil {
		t.Errorf("Changes = %+v, want nil on unreachable PrevIndexedSHA", res.Changes)
	}
	if res.NoChanges {
		t.Error("NoChanges = true after upstream advanced")
	}
}

func TestCloneOrFetch_EmptyPrevSHA_ReturnsNilChangeSet(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	w.CommitFiles(t, "init", map[string]string{
		"a.go": "package a\n",
	})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	w.CommitFiles(t, "v2", map[string]string{
		"b.go": "package b\n",
	})

	// Empty PrevIndexedSHA → we have nothing to diff against. Caller
	// should land in the full-reindex branch. Confirmed by Changes=nil.
	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: "",
	})
	if err != nil {
		t.Fatalf("CloneOrFetch (no prev): %v", err)
	}
	if res.Changes != nil {
		t.Errorf("Changes = %+v, want nil with empty PrevIndexedSHA", res.Changes)
	}
}

func TestCloneOrFetch_HalfWrittenClone_SelfHeals(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	headSHA := w.CommitFiles(t, "init", map[string]string{
		"a.go": "package a\n",
	})

	// Simulate what a SIGKILL mid-PlainClone leaves behind: .git exists
	// (so needsClone says "reuse") but holds no usable repository state.
	local := filepath.Join(t.TempDir(), "clone")
	if err := os.MkdirAll(filepath.Join(local, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir fake .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write fake HEAD: %v", err)
	}

	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL: "file://" + upstream,
		Branch:    "main",
		LocalDir:  local,
	})
	if err != nil {
		t.Fatalf("CloneOrFetch on half-written clone: %v (want self-heal, got permanent failure)", err)
	}
	if res.HeadSHA != headSHA {
		t.Errorf("HeadSHA = %s, want %s", res.HeadSHA, headSHA)
	}
	if res.RecloneReason == "" {
		t.Error("RecloneReason empty, want the local-state failure that forced the re-clone")
	}
}

func TestCloneOrFetch_RemoteURLChanged_Reclones(t *testing.T) {
	upstreamA, wa := makeBareUpstream(t, "main")
	wa.CommitFiles(t, "init A", map[string]string{"a.go": "package a\n"})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstreamA, local, "main")

	upstreamB, wb := makeBareUpstream(t, "main")
	headB := wb.CommitFiles(t, "init B", map[string]string{"b.go": "package b\n"})

	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL: "file://" + upstreamB,
		Branch:    "main",
		LocalDir:  local,
	})
	if err != nil {
		t.Fatalf("CloneOrFetch with changed URL: %v (want re-clone, got error)", err)
	}
	if res.HeadSHA != headB {
		t.Errorf("HeadSHA = %s, want %s (upstream B)", res.HeadSHA, headB)
	}
	if res.RecloneReason == "" {
		t.Error("RecloneReason empty, want the remote-mismatch reason")
	}
}

func TestCloneOrFetch_FreshClone_HasNoTags(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	w.CommitFiles(t, "v1", map[string]string{"a.go": "package a\n"})
	w.Tag(t, "v1.0.0")
	w.CommitFiles(t, "v2", map[string]string{"a.go": "package a // v2\n"})
	w.Tag(t, "v2.0.0")

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	if n := tagRefCount(t, local); n != 0 {
		t.Errorf("fresh clone carries %d tag refs, want 0 (Tags:NoTags)", n)
	}
}

// TestCloneOrFetch_UpgradeCompactsLegacyCheckout is the no-explicit-migration
// upgrade path: a checkout produced by a PRE-NoTags server (AllTags clone,
// accumulated fetch packs) must be cleaned by the FIRST CloneOrFetch the
// upgraded server runs on it — tags dropped, packs collapsed to one, disk
// reclaimed — while the incremental diff for that same update still computes.
func TestCloneOrFetch_UpgradeCompactsLegacyCheckout(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	w.CommitFiles(t, "v1", map[string]string{
		"a.go":       "package a\n",
		"keep.go":    "package keep\n",
		"assets/big": strings.Repeat("payload ", 4096),
	})
	w.Tag(t, "r1")
	w.CommitFiles(t, "v2", map[string]string{"a.go": "package a // v2\n"})
	w.Tag(t, "r2")

	// What the OLD server left on disk: AllTags shallow clone plus two
	// fetch+reset cycles, each of which persisted another snapshot pack.
	local := filepath.Join(t.TempDir(), "clone")
	legacyClone(t, upstream, local, "main")
	w.CommitFiles(t, "v3", map[string]string{"b.go": "package b\n"})
	legacyFetchReset(t, local, "main")
	indexedSHA := w.CommitFiles(t, "v4", map[string]string{"c.go": "package c\n"})
	legacyFetchReset(t, local, "main")

	if n := tagRefCount(t, local); n != 2 {
		t.Fatalf("legacy checkout has %d tag refs, want 2 — test setup broken", n)
	}
	if n := packfileCount(local); n < 3 {
		t.Fatalf("legacy checkout has %d packs, want >=3 — test setup broken", n)
	}
	objectsBefore := objectsDirSize(local)

	// Server upgrades. The next upstream push triggers an ordinary update —
	// and that first update must clean the store.
	newSHA := w.CommitFiles(t, "v5", map[string]string{"c.go": "package c // v5\n"})
	res, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: indexedSHA,
	})
	if err != nil {
		t.Fatalf("first post-upgrade CloneOrFetch: %v", err)
	}
	if res.HeadSHA != newSHA {
		t.Errorf("HeadSHA = %s, want %s", res.HeadSHA, newSHA)
	}
	if res.RecloneReason != "" {
		t.Errorf("RecloneReason = %q — the upgrade path must compact in place, not re-clone", res.RecloneReason)
	}
	if res.Compaction == nil {
		t.Fatal("Compaction stats nil — legacy checkout was not compacted on first update")
	}
	if res.Compaction.TagRefsDropped != 2 {
		t.Errorf("TagRefsDropped = %d, want 2", res.Compaction.TagRefsDropped)
	}
	if n := tagRefCount(t, local); n != 0 {
		t.Errorf("%d tag refs survive the upgrade compaction, want 0", n)
	}
	if n := packfileCount(local); n != 1 {
		t.Errorf("packfileCount = %d after compaction, want 1", n)
	}
	if after := objectsDirSize(local); after >= objectsBefore {
		t.Errorf("objects dir did not shrink: %d -> %d bytes", objectsBefore, after)
	}
	// The very update that compacted must still deliver the incremental
	// change set (v4 -> v5, computed before the reset).
	if res.Changes == nil {
		t.Fatal("Changes nil across the compacting update, want incremental diff")
	}
	if got := sortedCopy(res.Changes.Modified); !equalSlices(got, []string{"c.go"}) {
		t.Errorf("Modified = %v, want [c.go]", got)
	}

	// The protected diff base must survive compaction: pretend the index
	// job after the upgrade never completed (indexed_sha still v4), push
	// again, and demand a v4-based diff.
	newestSHA := w.CommitFiles(t, "v6", map[string]string{"a.go": "package a // v6\n"})
	res2, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: indexedSHA,
	})
	if err != nil {
		t.Fatalf("second post-upgrade CloneOrFetch: %v", err)
	}
	if res2.HeadSHA != newestSHA {
		t.Errorf("HeadSHA = %s, want %s", res2.HeadSHA, newestSHA)
	}
	if res2.Changes == nil {
		t.Error("Changes nil — protected indexed_sha did not survive compaction")
	}

	// And a quiet no-op cycle afterwards: nothing left to clean.
	res3, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL:      "file://" + upstream,
		Branch:         "main",
		LocalDir:       local,
		PrevIndexedSHA: newestSHA,
	})
	if err != nil {
		t.Fatalf("no-op CloneOrFetch: %v", err)
	}
	if !res3.NoChanges {
		t.Error("NoChanges = false on an unchanged upstream")
	}
	if res3.Compaction != nil {
		t.Error("Compaction ran on a clean checkout below the pack threshold")
	}
}

func TestCloneOrFetch_PackAccumulationStaysBounded(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	prev := w.CommitFiles(t, "init", map[string]string{"a.go": "package a\n"})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	compactions := 0
	for i := 1; i <= 2*compactPackThreshold; i++ {
		sha := w.CommitFiles(t, fmt.Sprintf("push %d", i), map[string]string{
			"a.go": fmt.Sprintf("package a // rev %d\n", i),
		})
		res, err := CloneOrFetch(context.Background(), CloneOptions{
			GitHubURL:      "file://" + upstream,
			Branch:         "main",
			LocalDir:       local,
			PrevIndexedSHA: prev,
		})
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if res.HeadSHA != sha {
			t.Fatalf("cycle %d: HeadSHA = %s, want %s", i, res.HeadSHA, sha)
		}
		if res.Compaction != nil {
			compactions++
		}
		if n := packfileCount(local); n > compactPackThreshold {
			t.Fatalf("cycle %d: %d packs on disk, bound is %d", i, n, compactPackThreshold)
		}
		prev = sha
	}
	if compactions == 0 {
		t.Errorf("no compaction ran across %d fetch cycles", 2*compactPackThreshold)
	}
}

func TestCloneOrFetch_FetchFailure_KeepsClone(t *testing.T) {
	upstream, w := makeBareUpstream(t, "main")
	headSHA := w.CommitFiles(t, "init", map[string]string{"a.go": "package a\n"})

	local := filepath.Join(t.TempDir(), "clone")
	initialCloneFor(t, upstream, local, "main")

	// Kill the upstream. The URL still matches the checkout's origin, so
	// this is indistinguishable from a network outage — the fetch must
	// fail WITHOUT costing us the healthy local clone.
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatalf("remove upstream: %v", err)
	}

	_, err := CloneOrFetch(context.Background(), CloneOptions{
		GitHubURL: "file://" + upstream,
		Branch:    "main",
		LocalDir:  local,
	})
	if err == nil {
		t.Fatal("CloneOrFetch succeeded against a dead upstream, want error")
	}

	repo, oerr := git.PlainOpen(local)
	if oerr != nil {
		t.Fatalf("local clone destroyed by a fetch failure: %v", oerr)
	}
	head, herr := repo.Head()
	if herr != nil {
		t.Fatalf("local clone HEAD unreadable after fetch failure: %v", herr)
	}
	if head.Hash().String() != headSHA {
		t.Errorf("local HEAD = %s, want untouched %s", head.Hash().String(), headSHA)
	}
}

func TestChangeSet_IsEmpty(t *testing.T) {
	if !(*ChangeSet)(nil).IsEmpty() {
		t.Error("nil ChangeSet should report IsEmpty=true")
	}
	if !(&ChangeSet{}).IsEmpty() {
		t.Error("zero ChangeSet should report IsEmpty=true")
	}
	if (&ChangeSet{Added: []string{"x"}}).IsEmpty() {
		t.Error("ChangeSet with Added entries reported IsEmpty=true")
	}
	if (&ChangeSet{Modified: []string{"x"}}).IsEmpty() {
		t.Error("ChangeSet with Modified entries reported IsEmpty=true")
	}
	if (&ChangeSet{Deleted: []string{"x"}}).IsEmpty() {
		t.Error("ChangeSet with Deleted entries reported IsEmpty=true")
	}
}

func equalSlices(a, b []string) bool {
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
