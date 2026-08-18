// Package repocloner is the git boundary for server-side GitHub repo
// management. It wraps go-git so the rest of the codebase doesn't need
// to know about plumbing objects, references, or storage layers.
//
// This package is independent of the workspaces organisational layer —
// callers (currently just repojobs.handleClone) use it to keep a single
// repo's local clone in sync with its remote, regardless of whether
// that repo participates in any workspace.
//
// Why go-git (not `git` shell-out): the production CUDA image runs on
// distroless/cc-debian13 which has no shell and no git binary. Pulling
// go-git into the binary keeps the runtime image untouched.
//
// What this package does:
//   - Clone a branch (public OR PAT-authenticated), shallow and tag-free
//   - Fetch + reset to remote HEAD on subsequent runs
//   - Compact the object store in place when fetch packs pile up or legacy
//     tag snapshots are present (see compact.go) — go-git has no gc of its
//     own and the distroless runtime has no git binary
//   - Discard and re-clone a checkout whose local state is unusable
//     (half-written clone, changed remote URL, failed compaction)
//   - Report the current HEAD SHA (for last_sha bookkeeping)
//   - Resolve a "github.com/owner/repo" + branch to a deterministic local
//     directory under DataDir/repos/{path_hash}/
//
// Errors are deliberately coarse — the worker pool surfaces them in the
// job row and the dashboard renders them verbatim. There's no point
// distinguishing "wrong PAT" from "branch missing" deep in the call chain.
package repocloner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// ErrAlreadyUpToDate signals a fetch found no new commits. Callers can
// short-circuit reindex on this.
var ErrAlreadyUpToDate = errors.New("repo already up to date")

// errLocalState marks a reuse-path failure caused by the checkout on disk
// itself (half-written clone, missing refs, mismatched remote) rather than by
// the network or the remote. CloneOrFetch recovers from these by discarding
// the directory and cloning fresh; anything NOT wrapped with this sentinel
// (fetch/transport failures) is returned as-is so a network blip never costs
// an otherwise healthy clone.
var errLocalState = errors.New("local clone state unusable")

// CloneOptions parameterises a clone or fetch.
type CloneOptions struct {
	// GitHubURL is the canonical HTTPS URL — "https://github.com/owner/repo"
	// (with or without ".git" suffix; both work).
	GitHubURL string
	Branch    string
	// PAT, when non-empty, is sent as HTTP BasicAuth with username
	// "x-access-token" — works for fine-grained tokens, classic PATs, and
	// GitHub App installation tokens alike.
	PAT string
	// LocalDir is the absolute destination. Created if missing; reused
	// (fetch+reset) if it already contains a git repository for the same
	// remote URL.
	LocalDir string
	// PrevIndexedSHA, when non-empty AND reachable from .git/objects on
	// the existing clone, drives the incremental change-set computation:
	// CloneOrFetch returns ChangeSet=diff(tree(PrevIndexedSHA), tree(newHEAD))
	// instead of nil. Empty (or unreachable) → ChangeSet nil and the
	// caller does a full reindex. Pass git_repos.indexed_sha, not last_sha:
	// the diff has to start from what's actually in the index, otherwise
	// changes that happened between the last successful index and the
	// previous (perhaps failed) clone job would be missed.
	PrevIndexedSHA string
}

// ChangeSet is the per-path delta computed via go-git tree.Diff between
// the previously-indexed commit and the post-fetch HEAD. Paths are
// repo-relative POSIX-style (slash-separated). Modified and Added are
// the union of what the incremental indexer must re-process; Deleted
// drives FinishIndexing's per-file cleanup of vectorstore + symbols +
// refs + chunks_fts + file_hashes.
type ChangeSet struct {
	Modified []string
	Added    []string
	Deleted  []string
}

// IsEmpty reports whether the change set contains no work for the
// incremental indexer. Empty is legal (e.g. a fetch that brought new
// commits but only touched files we filter out at the indexer level)
// but the caller can short-circuit on it to avoid an empty
// Begin→Finish round trip.
func (c *ChangeSet) IsEmpty() bool {
	if c == nil {
		return true
	}
	return len(c.Modified) == 0 && len(c.Added) == 0 && len(c.Deleted) == 0
}

// Result is what handlers care about post-clone.
type Result struct {
	// HeadSHA is the commit SHA on disk after the operation completes
	// (post-clone or post-reset-to-origin/<branch>).
	HeadSHA string
	// Changes carries the path-level delta produced by tree.Diff
	// between PrevIndexedSHA and HeadSHA. nil signals "caller should
	// run a full reindex" — first clone, no PrevIndexedSHA supplied,
	// old commit not in .git/objects anymore, or tree comparison
	// outright failed. Non-nil (even if empty after IsEmpty()) means
	// "we know the exact change set, run incremental".
	Changes *ChangeSet
	// NoChanges is true when the fetch revealed that the remote HEAD
	// matches the local HEAD before the fetch (i.e. nothing new). The
	// caller can skip enqueueing an index_repo job entirely.
	NoChanges bool
	// RecloneReason is non-empty when an existing checkout was discarded
	// and cloned fresh (unusable local state or a changed remote URL).
	// Purely informational — callers log it so the operator can see why a
	// fetch turned into a full clone.
	RecloneReason string
	// PrevHeadSHA is the commit that was on disk BEFORE this update moved
	// the checkout (empty on a fresh clone). Callers pass it to
	// MaybeCompact's protect list: it is the target of a possibly
	// still-queued index job, and nothing else keeps it alive once the
	// branch ref has moved on.
	PrevHeadSHA string
}

// CloneOrFetch clones the repo when LocalDir is empty, otherwise fetches
// + resets the local checkout to origin/{branch}. Returns the HEAD SHA
// after the operation completes.
//
// An existing checkout is discarded and cloned fresh (Result.RecloneReason
// says why) in two situations: its .git state is unusable — the half-clone
// a SIGKILL mid-clone leaves behind used to fail every retry forever — or
// its origin URL no longer matches the requested one (github_url changed).
// The local-state check is retried once first: transient filesystem pressure
// (EMFILE under concurrent jobs, a momentary EACCES) must not cost a
// multi-GB checkout, while a genuinely broken one fails the retry the same
// way. Fetch/transport failures are never grounds for a re-clone — a network
// blip must not cost a healthy clone (and force the full reindex that
// follows one).
//
// Compaction of the object store is NOT part of this call: callers run
// MaybeCompact afterwards, outside their per-repo write lock — see that
// function for why the locking is layered that way.
//
// The caller is responsible for choosing a LocalDir that won't collide
// across repos — typically `<DataDir>/repos/<path_hash>/` keyed by
// projects.path_hash (NOT the github URL, which can change with
// rename + redirect).
func CloneOrFetch(ctx context.Context, opts CloneOptions) (Result, error) {
	if strings.TrimSpace(opts.GitHubURL) == "" {
		return Result{}, fmt.Errorf("GitHubURL required")
	}
	if strings.TrimSpace(opts.Branch) == "" {
		return Result{}, fmt.Errorf("Branch required")
	}
	if strings.TrimSpace(opts.LocalDir) == "" {
		return Result{}, fmt.Errorf("LocalDir required")
	}
	url := normaliseURL(opts.GitHubURL)
	auth := authFor(opts.PAT)

	// First-time clone path: LocalDir is missing or empty.
	if needsClone(opts.LocalDir) {
		return cloneFresh(ctx, opts, url, auth)
	}

	res, err := updateExisting(ctx, opts, url, auth)
	if err != nil && errors.Is(err, errLocalState) && ctx.Err() == nil {
		// One retry before concluding the state is structural: EMFILE/EIO
		// class failures heal, a half-written clone fails identically.
		res, err = updateExisting(ctx, opts, url, auth)
	}
	if err == nil {
		return res, nil
	}
	// Only local-state failures are recoverable by re-cloning, and never on
	// a dead context — a cancelled shutdown fetch is not evidence the
	// checkout is bad.
	if !errors.Is(err, errLocalState) || ctx.Err() != nil {
		return Result{}, err
	}
	return reclone(ctx, opts, url, auth, err.Error())
}

// cloneFresh is the first-time clone into an empty or missing LocalDir.
func cloneFresh(ctx context.Context, opts CloneOptions, url string, auth *http.BasicAuth) (Result, error) {
	if err := os.MkdirAll(opts.LocalDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("mkdir clone target: %w", err)
	}
	repo, err := git.PlainCloneContext(ctx, opts.LocalDir, false, &git.CloneOptions{
		URL:           url,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(opts.Branch),
		SingleBranch:  true,
		Depth:         1, // shallow — minimises bandwidth + disk
		// go-git's clone default is AllTags, and on a shallow clone every
		// tag arrives as a FULL tree snapshot (spring-boot's 391 tags cost
		// a 102 MB store for a 39 MB worktree). cix indexes one branch and
		// never reads tags.
		Tags: git.NoTags,
	})
	if err != nil {
		// Cleanup so the next retry isn't stuck with a half-clone.
		_ = os.RemoveAll(opts.LocalDir)
		return Result{}, fmt.Errorf("clone: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	return Result{HeadSHA: head.Hash().String()}, nil
}

// reclone discards the existing checkout and clones fresh. The re-clone loses
// the old object store, so PrevIndexedSHA becomes unreachable and Changes
// stays nil — the caller lands in its reconcile path, which is the correct
// (and hash-gated, so cheap) recovery.
func reclone(ctx context.Context, opts CloneOptions, url string, auth *http.BasicAuth, reason string) (Result, error) {
	if err := os.RemoveAll(opts.LocalDir); err != nil {
		return Result{}, fmt.Errorf("remove stale clone at %s (%s): %w", opts.LocalDir, reason, err)
	}
	res, err := cloneFresh(ctx, opts, url, auth)
	if err != nil {
		return Result{}, fmt.Errorf("reclone (%s): %w", reason, err)
	}
	res.RecloneReason = reason
	return res, nil
}

// updateExisting is the reuse path: open the existing repo, ensure the remote
// matches, fetch, (optionally compute change set,) reset to origin/{branch}.
// Failures rooted in the on-disk state are wrapped with errLocalState so the
// caller can recover by re-cloning; fetch failures are returned bare.
func updateExisting(ctx context.Context, opts CloneOptions, url string, auth *http.BasicAuth) (Result, error) {
	repo, err := git.PlainOpen(opts.LocalDir)
	if err != nil {
		return Result{}, fmt.Errorf("%w: open existing repo at %s: %w", errLocalState, opts.LocalDir, err)
	}
	if err := ensureRemote(repo, url); err != nil {
		return Result{}, fmt.Errorf("%w: %w", errLocalState, err)
	}

	// Snapshot the pre-fetch HEAD so we can short-circuit on NoChanges
	// when the fetch reveals no new commits. This is the commit currently
	// on disk; it may or may not match opts.PrevIndexedSHA (mismatch
	// means a previous index job failed mid-way — the caller decides
	// how to recover). A failure here is the signature of a half-written
	// clone (SIGKILL mid-PlainClone leaves .git without refs).
	prevHead, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("%w: resolve pre-fetch HEAD: %w", errLocalState, err)
	}

	err = repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     auth,
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", opts.Branch, opts.Branch))},
		Depth:    1,
		Force:    true,
		// Default is TagFollowing; without NoTags every fetch can drag in
		// new tag snapshots. See the matching option in cloneFresh.
		Tags: git.NoTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Result{}, fmt.Errorf("fetch: %w", err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", opts.Branch), true)
	if err != nil {
		return Result{}, fmt.Errorf("%w: resolve remote ref: %w", errLocalState, err)
	}

	newSHA := remoteRef.Hash()
	// No-op fetch: remote HEAD already matches what's on disk. Tell the
	// caller there is nothing to reindex (NoChanges supersedes Changes —
	// no index job should be enqueued) but still run the hard reset below:
	// a crash mid-reset on a previous run leaves HEAD already pointing at
	// newSHA over half-rewritten files, and this path is the only chance
	// to repair that — go-git writes HEAD before it touches the worktree,
	// so the torn state looks exactly like a completed update. On a clean
	// worktree the reset writes nothing.
	noChanges := prevHead.Hash() == newSHA

	// Best-effort change-set computation. Runs BEFORE the reset so
	// tree.Diff still sees both commits via their stored tree objects.
	// Failures here are non-fatal — Changes stays nil and the caller
	// falls back to a full reindex.
	var changes *ChangeSet
	diffBase := strings.TrimSpace(opts.PrevIndexedSHA)
	if diffBase != "" && !noChanges {
		cs, derr := computeChangeSet(repo, diffBase, newSHA.String())
		if derr == nil {
			changes = cs
		}
		// derr is intentionally swallowed: the caller routes to full
		// reindex on nil Changes, which is the safe fallback when the
		// old commit isn't in objects (ran git gc, corrupted clone,
		// force-push that orphaned the indexed tree, etc.).
	}

	wt, err := repo.Worktree()
	if err != nil {
		return Result{}, fmt.Errorf("%w: worktree: %w", errLocalState, err)
	}
	// Hard reset — discards any local mutation that crept in. Worker-managed
	// checkouts have no human edits we'd want to preserve. The commit was
	// just fetched, so a failure here means the local store is broken.
	if err := wt.Reset(&git.ResetOptions{
		Commit: newSHA,
		Mode:   git.HardReset,
	}); err != nil {
		return Result{}, fmt.Errorf("%w: reset: %w", errLocalState, err)
	}

	head, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("%w: resolve HEAD post-reset: %w", errLocalState, err)
	}
	return Result{
		HeadSHA:     head.Hash().String(),
		Changes:     changes,
		NoChanges:   noChanges,
		PrevHeadSHA: prevHead.Hash().String(),
	}, nil
}

// computeChangeSet diffs the tree of oldSHA against the tree of newSHA
// via go-git's tree.Diff. Returns nil on any inability to fetch either
// commit/tree from .git/objects — the caller treats that as "fall back
// to full reindex". Rename detection is intentionally OFF: a rename
// shows up as one Added + one Deleted, both get re-embedded under the
// new path (path is part of the embedding preamble), and the obsolete
// chunks at the old path are cleaned up via the Deleted list. Trying
// to be clever about renames would save only the embedding cost of
// the new-name copy.
func computeChangeSet(repo *git.Repository, oldSHA, newSHA string) (*ChangeSet, error) {
	oldHash := plumbing.NewHash(oldSHA)
	newHash := plumbing.NewHash(newSHA)
	if oldHash.IsZero() || newHash.IsZero() {
		return nil, fmt.Errorf("invalid sha (oldSHA=%q newSHA=%q)", oldSHA, newSHA)
	}
	oldCommit, err := repo.CommitObject(oldHash)
	if err != nil {
		return nil, fmt.Errorf("lookup old commit %s: %w", oldSHA, err)
	}
	newCommit, err := repo.CommitObject(newHash)
	if err != nil {
		return nil, fmt.Errorf("lookup new commit %s: %w", newSHA, err)
	}
	oldTree, err := oldCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("old tree: %w", err)
	}
	newTree, err := newCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("new tree: %w", err)
	}
	changes, err := oldTree.Diff(newTree)
	if err != nil {
		return nil, fmt.Errorf("tree diff: %w", err)
	}
	cs := &ChangeSet{}
	for _, c := range changes {
		action, aerr := c.Action()
		if aerr != nil {
			// Indeterminate change (corrupt entry?). Treat as a worst-case
			// "this file was touched, reindex it" — list both sides so
			// the new content gets indexed and any stale old-path data
			// gets cleaned.
			if c.From.Name != "" {
				cs.Deleted = append(cs.Deleted, c.From.Name)
			}
			if c.To.Name != "" {
				cs.Modified = append(cs.Modified, c.To.Name)
			}
			continue
		}
		switch action {
		case merkletrie.Insert:
			cs.Added = append(cs.Added, c.To.Name)
		case merkletrie.Delete:
			cs.Deleted = append(cs.Deleted, c.From.Name)
		case merkletrie.Modify:
			// Treat tree-entry modifications uniformly even when the
			// path renamed (rename detection is off, so this case is
			// just "same path, different blob").
			cs.Modified = append(cs.Modified, c.To.Name)
		}
	}
	return cs, nil
}

// LocalDirFor returns the canonical path for a workspace_repo's checkout
// under dataDir. Centralised so the worker pool and the cleanup path agree.
// The id segment is treated as opaque (UUID/ULID), no validation here.
func LocalDirFor(dataDir, id string) string {
	return filepath.Join(dataDir, "repos", id)
}

// --- helpers ---

func authFor(pat string) *http.BasicAuth {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return nil
	}
	return &http.BasicAuth{
		// x-access-token is the username GitHub accepts for App / fine-grained
		// token auth. Classic PATs also accept it.
		Username: "x-access-token",
		Password: pat,
	}
}

func normaliseURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, "/")
	if !strings.HasSuffix(u, ".git") {
		u += ".git"
	}
	return u
}

// packfileCount counts the .pack files in the checkout's object store. A
// fresh shallow clone has exactly one; each subsequent fetch adds one more.
// Best effort — 0 on any error keeps the caller on the ordinary reuse path.
func packfileCount(dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	if err != nil {
		return 0
	}
	return len(matches)
}

func needsClone(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		// Either dir doesn't exist or has no .git/ — fresh clone path.
		return true
	}
	return false
}

func ensureRemote(repo *git.Repository, wantURL string) error {
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("no origin remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 || urls[0] != wantURL {
		// Repo on disk points at a different URL — likely the workspace
		// admin changed the github_url. The old checkout is dead weight;
		// the errLocalState wrap this gets in updateExisting is what turns
		// it into a nuke + re-clone.
		return fmt.Errorf("local repo remote %v does not match expected %s", urls, wantURL)
	}
	return nil
}
