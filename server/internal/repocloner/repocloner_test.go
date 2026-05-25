package repocloner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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
