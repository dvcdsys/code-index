// Command gc-poc is a throwaway proof-of-concept harness for the git storage
// bloat problem (see loadtests/GIT_STORAGE_CONTEXT.md in the main worktree).
//
// It answers, empirically, four questions:
//
//  1. GROWTH: does the production fetch(Depth:1)+reset loop really accumulate
//     one packfile per fetch with no upper bound? (baseline measurement)
//  2. STOCK: does go-git's own Repository.RepackObjects work on a SHALLOW
//     checkout? (hypothesis: no — its object walker follows commit parents,
//     which a shallow clone does not have on disk)
//  3. COMPACT: does a shallow-tolerant reachability compaction — walk live
//     refs, stop at the shallow boundary, encode exactly the reachable set
//     into one new pack, delete the old packs, prune loose strays, rewrite
//     .git/shallow — reclaim the space while keeping the repo fully usable?
//  4. AFTER: after compaction, do subsequent fetch+reset cycles still work,
//     and is the incremental tree-diff (indexed_sha → new HEAD) still
//     computable? (the property a delete-and-reclone strategy destroys)
//
// The upstream side is a REAL git repository driven through the git binary,
// which also gives us `git fsck` as an independent correctness oracle.
// The client side uses go-git v5 with the exact clone/fetch/reset options
// production uses (repocloner.CloneOrFetch).
//
// NOT production code. No tests, panics on failure, prints a report to stdout.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"golang.org/x/sys/unix"
)

const branch = "main"

func main() {
	repoFlag := flag.String("repo", "", "compact a COPY of this existing checkout under instrumentation, skip the synthetic scenarios")
	flag.Parse()
	if *repoFlag != "" {
		runRepoMode(*repoFlag)
		return
	}

	root, err := os.MkdirTemp("", "gc-poc-*")
	must(err)
	fmt.Printf("workdir: %s\n\n", root)

	upstream := filepath.Join(root, "upstream")
	seedUpstream(upstream)

	// ---------------------------------------------------------------- 1. GROWTH
	fmt.Println("== 1. GROWTH: prod-style fetch(Depth:1)+reset loop, 30 pushes ==")
	clone := filepath.Join(root, "clone")
	cloneShallow(upstream, clone)
	report(clone, "after initial shallow clone")

	for i := 1; i <= 30; i++ {
		pushUpstream(upstream, i)
		fetchAndReset(clone, upstream)
		if i%10 == 0 || i == 1 {
			report(clone, fmt.Sprintf("after push #%d", i))
		}
	}
	// Real pushes often carry several commits. A Depth:1 fetch takes only the
	// tip, so the tip's parent is genuinely absent from the object store —
	// the condition that should kill go-git's own parent-following walker.
	fmt.Println("-- gap: 3 pushes of 3 commits each; fetch sees only each tip --")
	for i := 31; i <= 33; i++ {
		pushUpstreamMulti(upstream, i*10, 3)
		fetchAndReset(clone, upstream)
	}
	report(clone, "after 3 multi-commit pushes")

	growthObjects := dirSize(filepath.Join(clone, ".git", "objects"))
	growthWorktree := dirSize(clone) - dirSize(filepath.Join(clone, ".git"))

	// ---------------------------------------------------------------- 2. STOCK
	fmt.Println("\n== 2. STOCK go-git RepackObjects on the shallow checkout ==")
	stockCopy := filepath.Join(root, "clone-stock")
	copyTree(clone, stockCopy)
	repo, err := git.PlainOpen(stockCopy)
	must(err)
	if rerr := repo.RepackObjects(&git.RepackConfig{}); rerr != nil {
		fmt.Printf("RepackObjects FAILED as hypothesised: %v\n", rerr)
	} else {
		fmt.Println("RepackObjects unexpectedly SUCCEEDED — re-measuring:")
		report(stockCopy, "after stock RepackObjects")
	}

	// ---------------------------------------------------------------- 3. COMPACT
	fmt.Println("\n== 3. COMPACT: shallow-tolerant reachability compaction ==")
	preFiles := worktreeManifest(clone)
	preHead := gitOut(clone, "rev-parse", "HEAD")

	start := time.Now()
	stats, err := compact(clone)
	must(err)
	elapsed := time.Since(start)
	fmt.Printf("compacted in %v: %d reachable objects → 1 pack; %d old packs deleted, %d loose pruned, shallow %d→%d entries\n",
		elapsed.Round(time.Millisecond), stats.reachable, stats.packsDeleted, stats.loosePruned, stats.shallowBefore, stats.shallowAfter)
	report(clone, "after compaction")
	fmt.Printf("objects dir: %s → %s (worktree payload %s)\n",
		human(growthObjects), human(dirSize(filepath.Join(clone, ".git", "objects"))), human(growthWorktree))

	// Oracles.
	fmt.Println("\n-- correctness oracles --")
	fsck := gitRun(clone, "fsck", "--strict", "--no-dangling")
	fmt.Printf("git fsck --strict: %s\n", okOr(fsck, "CLEAN"))
	status := gitOut(clone, "status", "--porcelain")
	fmt.Printf("git status --porcelain: %s\n", okOr(emptyErr(status), "worktree untouched"))
	if h := gitOut(clone, "rev-parse", "HEAD"); h != preHead {
		fail("HEAD moved: %s → %s", preHead, h)
	}
	fmt.Println("HEAD unchanged: OK")
	postFiles := worktreeManifest(clone)
	if preFiles != postFiles {
		fail("worktree manifest changed across compaction")
	}
	fmt.Println("worktree manifest (path+sha256 of every file): IDENTICAL")
	verifyEveryBlobReadable(clone)

	// Idempotency.
	if _, err := compact(clone); err != nil {
		fail("second compaction failed: %v", err)
	}
	fmt.Println("second compaction (idempotency): OK")

	// ---------------------------------------------------------------- 4. AFTER
	fmt.Println("\n== 4. AFTER: prod order fetch → compact(protect=indexed_sha) → diff ==")
	indexedSHA := gitOut(clone, "rev-parse", "HEAD") // what indexed_sha would be
	pushUpstream(upstream, 1001)
	fetchAndReset(clone, upstream)
	newHead := gitOut(clone, "rev-parse", "HEAD")
	if newHead == indexedSHA {
		fail("fetch after compaction brought no new commit")
	}
	// Compaction runs BETWEEN the fetch and the index job, so the not-yet-
	// reindexed base commit survives only because it is protected.
	if _, err := compact(clone, plumbing.NewHash(indexedSHA)); err != nil {
		fail("compact with protected indexed_sha: %v", err)
	}
	cs, err := changeSet(clone, indexedSHA, newHead)
	if err != nil {
		fmt.Printf("tree-diff %s..%s FAILED: %v (a re-clone strategy always fails here)\n", short(indexedSHA), short(newHead), err)
	} else {
		fmt.Printf("tree-diff %s..%s after an intervening compaction: %d modified, %d added, %d deleted — INCREMENTAL INDEXING PRESERVED\n",
			short(indexedSHA), short(newHead), len(cs.mod), len(cs.add), len(cs.del))
	}

	// Steady state: compact after every fetch for 20 more pushes.
	fmt.Println("\n-- steady state: 20 more pushes, compact(protect=prev head) after every fetch --")
	var maxPacks, maxSize int64
	var totalCompact time.Duration
	for i := 2; i <= 21; i++ {
		prev := gitOut(clone, "rev-parse", "HEAD")
		pushUpstream(upstream, 1000+i)
		fetchAndReset(clone, upstream)
		t0 := time.Now()
		_, err := compact(clone, plumbing.NewHash(prev))
		must(err)
		totalCompact += time.Since(t0)
		if n := int64(packCount(clone)); n > maxPacks {
			maxPacks = n
		}
		if s := dirSize(filepath.Join(clone, ".git", "objects")); s > maxSize {
			maxSize = s
		}
	}
	report(clone, "after 20 compact-every-fetch cycles")
	fmt.Printf("steady state: max packs ever = %d, max objects size ever = %s, avg compaction = %v\n",
		maxPacks, human(maxSize), (totalCompact / 20).Round(time.Millisecond))
	fsck = gitRun(clone, "fsck", "--strict", "--no-dangling")
	fmt.Printf("final git fsck --strict: %s\n", okOr(fsck, "CLEAN"))

	// ---------------------------------------------------------------- 5. FORCE-PUSH
	fmt.Println("\n== 5. FORCE-PUSH: upstream history rewrite, then fetch + compact ==")
	gitMust(upstream, "reset", "--hard", "HEAD~2")
	pushUpstream(upstream, 3000)
	fetchAndReset(clone, upstream)
	if got, want := gitOut(clone, "rev-parse", "HEAD"), gitOut(upstream, "rev-parse", "HEAD"); got != want {
		fail("HEAD after force-push fetch = %s, upstream = %s", got, want)
	}
	_, err = compact(clone)
	must(err)
	report(clone, "after force-push fetch + compaction")
	fsck = gitRun(clone, "fsck", "--strict", "--no-dangling")
	fmt.Printf("git fsck --strict: %s\n", okOr(fsck, "CLEAN"))
	verifyEveryBlobReadable(clone)

	// ---------------------------------------------------------------- 6. CANONICAL
	fmt.Println("\n== 6. CANONICAL: compacted checkout vs a full-history git clone ==")
	compareAgainstCanonical(root, upstream, clone, "small")

	// ---------------------------------------------------------------- 7. RESOURCES
	fmt.Println("\n== 7. RESOURCES: large repo (binary blobs), instrumented compaction ==")
	bigUp := filepath.Join(root, "big-upstream")
	seedBigUpstream(bigUp)
	bigClone := filepath.Join(root, "big-clone")
	cloneShallow(bigUp, bigClone)
	report(bigClone, "after initial shallow clone")
	for i := 1; i <= 12; i++ {
		pushBigUpstream(bigUp, i)
		fetchAndReset(bigClone, bigUp)
	}
	report(bigClone, "after 12 pushes")

	objectsBefore := dirSize(filepath.Join(bigClone, ".git", "objects"))
	preManifest := worktreeManifest(bigClone)
	var bigStats compactStats
	res, err := measured(func() error {
		var cerr error
		bigStats, cerr = compact(bigClone)
		return cerr
	})
	must(err)
	objectsAfter := dirSize(filepath.Join(bigClone, ".git", "objects"))
	report(bigClone, "after compaction")
	fmt.Printf("\n-- resource usage of compact() on the large repo --\n")
	fmt.Printf("objects on disk : before=%s  PEAK during=%s (new pack coexists with old)  after=%s\n",
		human(objectsBefore), human(bigStats.packDirPeak), human(objectsAfter))
	fmt.Printf("wall time       : %v\n", res.wall.Round(time.Millisecond))
	fmt.Printf("cpu time        : user=%v sys=%v (%.0f%% of one core)\n",
		res.cpuUser.Round(time.Millisecond), res.cpuSys.Round(time.Millisecond),
		100*float64(res.cpuUser+res.cpuSys)/float64(res.wall))
	fmt.Printf("go heap         : baseline=%s  peak during=%s  (algorithm footprint ≈ %s)\n",
		human(int64(res.heapBefore)), human(int64(res.heapPeak)), human(int64(res.heapPeak-res.heapBefore)))
	fmt.Printf("process max RSS : %s (Δ vs before the call: %s)\n", human(res.maxRSSAfter), human(res.maxRSSAfter-res.maxRSSBefore))
	fmt.Printf("reachable objects packed: %d; packs deleted: %d\n\n", bigStats.reachable, bigStats.packsDeleted)

	if worktreeManifest(bigClone) != preManifest {
		fail("large repo worktree changed across compaction")
	}
	fmt.Println("worktree manifest identical across compaction: OK")
	fsck = gitRun(bigClone, "fsck", "--strict", "--no-dangling")
	fmt.Printf("git fsck --strict: %s\n", okOr(fsck, "CLEAN"))
	compareAgainstCanonical(root, bigUp, bigClone, "big")

	fmt.Println("\nall checks passed")
}

// ---------------------------------------------------------------------------
// The compaction algorithm under test. This is the candidate for
// production — everything else in this file is harness.
// ---------------------------------------------------------------------------

type compactStats struct {
	reachable     int
	packsDeleted  int
	loosePruned   int
	shallowBefore int
	shallowAfter  int
	// packDirPeak is .git/objects size at the worst moment: the new pack is
	// fully written while every old pack still exists. This is the extra disk
	// headroom the algorithm transiently needs.
	packDirPeak int64
	tagsDropped int
}

// compact rewrites the checkout's object store down to exactly the objects
// reachable from its current references, honouring the shallow boundary the
// way git itself does: a commit listed in .git/shallow is a graft point and
// its parents are NOT walked, even when they happen to be in the store.
// (Every Depth:1-fetched tip is such a graft point, so without this rule the
// store retains every previously fetched snapshot forever — reachable to a
// naive walker, invisible to git rev-list, pure dead weight.)
//
// protect lists commits that must survive even though no ref points at them —
// in production that is git_repos.indexed_sha, the base of the next
// incremental tree-diff. A protected commit that is not in the store is
// silently skipped (nothing to protect).
func compact(dir string, protect ...plumbing.Hash) (compactStats, error) {
	var st compactStats
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return st, fmt.Errorf("open: %w", err)
	}

	// Optional: drop tag refs before computing reachability. cix indexes one
	// branch and never serves tags, yet a default go-git clone brings a full
	// shallow SNAPSHOT PER TAG (spring-boot: 391 tags -> 345k objects for a
	// 39MB worktree). Dropping the refs lets the walk collapse to the branch
	// snapshot; paired with Tags:NoTags on clone/fetch they never return.
	if os.Getenv("GCPOC_DROP_TAGS") == "1" {
		refs, err := repo.References()
		if err != nil {
			return st, err
		}
		var tagRefs []plumbing.ReferenceName
		_ = refs.ForEach(func(ref *plumbing.Reference) error {
			if strings.HasPrefix(ref.Name().String(), "refs/tags/") {
				tagRefs = append(tagRefs, ref.Name())
			}
			return nil
		})
		for _, name := range tagRefs {
			if err := repo.Storer.RemoveReference(name); err != nil {
				return st, fmt.Errorf("remove tag ref %s: %w", name, err)
			}
			st.tagsDropped++
		}
	}

	shallowList, err := repo.Storer.Shallow()
	if err != nil {
		return st, fmt.Errorf("read shallow: %w", err)
	}
	shallowSet := make(map[plumbing.Hash]struct{}, len(shallowList))
	for _, h := range shallowList {
		shallowSet[h] = struct{}{}
	}

	// 1. Reachable set. Like go-git's objectWalker, but with git's shallow
	//    semantics for commit parents (see above); a parent missing from the
	//    store is likewise a boundary, not an error. Missing trees/blobs stay
	//    fatal — those would mean real corruption.
	w := &shallowWalker{storer: repo.Storer, shallow: shallowSet, seen: map[plumbing.Hash]struct{}{}}
	refs, err := repo.References()
	if err != nil {
		return st, err
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		return w.walk(ref.Hash())
	})
	if err != nil {
		return st, fmt.Errorf("reachability walk: %w", err)
	}
	for _, h := range protect {
		if h.IsZero() {
			continue
		}
		if _, err := object.GetObject(repo.Storer, h); err == plumbing.ErrObjectNotFound {
			continue
		}
		if err := w.walk(h); err != nil {
			return st, fmt.Errorf("walk protected %s: %w", h, err)
		}
	}
	st.reachable = len(w.seen)
	objs := make([]plumbing.Hash, 0, len(w.seen))
	for h := range w.seen {
		objs = append(objs, h)
	}

	// 2. Remember the packs that exist BEFORE writing the new one.
	pos, ok := repo.Storer.(storer.PackedObjectStorer)
	if !ok {
		return st, fmt.Errorf("storer does not support packed objects")
	}
	oldPacks, err := pos.ObjectPacks()
	if err != nil {
		return st, err
	}

	// 3. Write the new pack. PackfileWriter lands it in objects/pack with a
	//    proper idx; the encoder returns the pack hash.
	pfw, ok := repo.Storer.(storer.PackfileWriter)
	if !ok {
		return st, fmt.Errorf("storer does not support packfile writing")
	}
	wc, err := pfw.PackfileWriter()
	if err != nil {
		return st, err
	}
	enc := packfile.NewEncoder(wc, repo.Storer, false)
	newPack, err := enc.Encode(objs, packWindow())
	if cerr := wc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return st, fmt.Errorf("encode pack: %w", err)
	}
	st.packDirPeak = dirSize(filepath.Join(dir, ".git", "objects"))

	// 4. Only now that the new pack is durable: delete every old pack.
	for _, h := range oldPacks {
		if h == newPack {
			continue
		}
		if err := pos.DeleteOldObjectPackAndIndex(h, time.Time{}); err != nil {
			return st, fmt.Errorf("delete pack %s: %w", h, err)
		}
		st.packsDeleted++
	}

	// 5. Loose objects: everything reachable is now packed, so every loose
	//    object — reachable or not — is redundant. (go-git's own Prune can't
	//    be used here: its walker dies on the shallow boundary.)
	if los, ok := repo.Storer.(storer.LooseObjectStorer); ok {
		err = los.ForEachObjectHash(func(h plumbing.Hash) error {
			if derr := los.DeleteLooseObject(h); derr != nil {
				return derr
			}
			st.loosePruned++
			return nil
		})
		if err != nil {
			return st, fmt.Errorf("prune loose: %w", err)
		}
	}

	// 6. .git/shallow accumulates one boundary entry per fetch; entries whose
	//    commit we just dropped would make real git error out ("did not find
	//    object for shallow ..."). Keep only entries still in the store.
	shallow, err := repo.Storer.Shallow()
	if err != nil {
		return st, err
	}
	st.shallowBefore = len(shallow)
	kept := shallow[:0]
	for _, h := range shallow {
		if _, reachable := w.seen[h]; reachable {
			kept = append(kept, h)
		}
	}
	st.shallowAfter = len(kept)
	if st.shallowAfter != st.shallowBefore {
		if err := repo.Storer.SetShallow(kept); err != nil {
			return st, fmt.Errorf("rewrite shallow: %w", err)
		}
	}
	return st, nil
}

// packWindow is the delta-compression window for the new pack. 10 is git's
// default; GCPOC_WINDOW=0 disables delta search entirely, which matters for
// CPU: with shallow-correct reachability the packed set is mostly a single
// snapshot with few cross-object deltas to find anyway.
func packWindow() uint {
	if v := os.Getenv("GCPOC_WINDOW"); v != "" {
		var n uint
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 10
}

// shallowWalker is objectWalker with two behavioural changes: commits listed
// in .git/shallow are graft points whose parents are never walked (git's own
// semantics), and a commit parent missing from the object store terminates
// that branch of the walk instead of failing it.
type shallowWalker struct {
	storer  storage.Storer
	shallow map[plumbing.Hash]struct{}
	seen    map[plumbing.Hash]struct{}
}

func (w *shallowWalker) walk(hash plumbing.Hash) error {
	if _, ok := w.seen[hash]; ok {
		return nil
	}
	obj, err := object.GetObject(w.storer, hash)
	if err != nil {
		return fmt.Errorf("get object %s: %w", hash, err)
	}
	w.seen[hash] = struct{}{}
	switch obj := obj.(type) {
	case *object.Commit:
		if err := w.walk(obj.TreeHash); err != nil {
			return err
		}
		if _, grafted := w.shallow[obj.Hash]; grafted {
			// Shallow graft point: git treats this commit as parentless.
			break
		}
		for _, p := range obj.ParentHashes {
			if _, ok := w.seen[p]; ok {
				continue
			}
			// The commit names a parent we never fetched.
			if _, err := object.GetObject(w.storer, p); err == plumbing.ErrObjectNotFound {
				continue
			}
			if err := w.walk(p); err != nil {
				return err
			}
		}
	case *object.Tree:
		for i := range obj.Entries {
			e := obj.Entries[i]
			if e.Mode == filemode.Submodule {
				// Gitlink: the hash is a commit in ANOTHER repository and is
				// never present in this object store. (Stock go-git's
				// objectWalker trips over these too.)
				continue
			}
			if e.Mode|0o755 == filemode.Executable { // plain blob, any file mode
				w.seen[e.Hash] = struct{}{}
				continue
			}
			if err := w.walk(e.Hash); err != nil {
				return err
			}
		}
	case *object.Blob:
		// Leaf (reached via a symlink or other non-regular tree entry —
		// stock go-git's walker errors "unknown object" here).
	case *object.Tag:
		return w.walk(obj.Target)
	default:
		return fmt.Errorf("unknown object type %T at %s", obj, hash)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Client side: the exact clone/fetch/reset production performs.
// ---------------------------------------------------------------------------

func cloneShallow(upstream, dir string) {
	_, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:           "file://" + upstream,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	})
	must(err)
}

func fetchAndReset(dir, upstream string) {
	repo, err := git.PlainOpen(dir)
	must(err)
	err = repo.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))},
		Depth:    1,
		Force:    true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		fail("fetch: %v", err)
	}
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	must(err)
	wt, err := repo.Worktree()
	must(err)
	must(wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}))
	// Keep refs/heads in step the way a fresh clone would have it.
	must(repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), ref.Hash())))
}

// changeSet mirrors repocloner.computeChangeSet: tree-diff between two SHAs.
type cset struct{ mod, add, del []string }

func changeSet(dir, oldSHA, newSHA string) (*cset, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	oldC, err := repo.CommitObject(plumbing.NewHash(oldSHA))
	if err != nil {
		return nil, fmt.Errorf("old commit: %w", err)
	}
	newC, err := repo.CommitObject(plumbing.NewHash(newSHA))
	if err != nil {
		return nil, fmt.Errorf("new commit: %w", err)
	}
	oldT, err := oldC.Tree()
	if err != nil {
		return nil, err
	}
	newT, err := newC.Tree()
	if err != nil {
		return nil, err
	}
	diff, err := oldT.Diff(newT)
	if err != nil {
		return nil, err
	}
	out := &cset{}
	for _, ch := range diff {
		switch {
		case ch.From.Name == "":
			out.add = append(out.add, ch.To.Name)
		case ch.To.Name == "":
			out.del = append(out.del, ch.From.Name)
		default:
			out.mod = append(out.mod, ch.To.Name)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Canonical comparison: the compacted shallow checkout must be
// indistinguishable — file bytes AND git object identity — from a fresh
// FULL-history clone of the same upstream at the same commit.
// ---------------------------------------------------------------------------

func compareAgainstCanonical(root, upstream, clone, label string) {
	canonical := filepath.Join(root, "canonical-"+label)
	gitMust(filepath.Dir(canonical), "clone", "--quiet", "--branch", branch, "file://"+upstream, canonical)

	// Same commit on both sides (the loop always fetched to upstream HEAD).
	cHead := gitOut(clone, "rev-parse", "HEAD")
	kHead := gitOut(canonical, "rev-parse", "HEAD")
	if cHead != kHead {
		fail("HEAD mismatch: compacted=%s canonical=%s", cHead, kHead)
	}

	// 1. File bytes: every path, sha256, both directions.
	if worktreeManifest(clone) != worktreeManifest(canonical) {
		fail("worktree bytes differ from canonical full clone")
	}
	fmt.Println("file content vs canonical full clone (path+sha256, both directions): IDENTICAL")

	// 2. Git object identity: ls-tree -r prints mode, type, blob hash and path
	//    for every entry — equal output means the compacted store serves
	//    byte-identical git objects, not just byte-identical files.
	if gitOut(clone, "ls-tree", "-r", "HEAD") != gitOut(canonical, "ls-tree", "-r", "HEAD") {
		fail("ls-tree -r HEAD differs from canonical")
	}
	fmt.Println("git ls-tree -r HEAD vs canonical: IDENTICAL")

	// 3. History mode: every commit that SURVIVES in the shallow store
	//    (rev-list stops at the shallow boundary) must exist in canonical
	//    history with the same tree hash.
	surviving := strings.Fields(gitOut(clone, "rev-list", "HEAD"))
	for _, sha := range surviving {
		if gitOut(clone, "rev-parse", sha+"^{tree}") != gitOut(canonical, "rev-parse", sha+"^{tree}") {
			fail("commit %s: tree hash differs from canonical history", sha)
		}
	}
	fmt.Printf("surviving history (%d commits up to the shallow boundary): every commit present in canonical history with an identical tree hash\n", len(surviving))
}

// ---------------------------------------------------------------------------
// Resource instrumentation.
// ---------------------------------------------------------------------------

type resUsage struct {
	wall             time.Duration
	cpuUser, cpuSys  time.Duration
	heapBefore       uint64
	heapPeak         uint64
	maxRSSBefore     int64
	maxRSSAfter      int64
}

// measured runs fn while sampling the Go heap every 2ms and diffing
// getrusage around the call. maxRSS is a process-lifetime high-water mark, so
// the "before" value is the baseline the call may or may not have raised.
func measured(fn func() error) (resUsage, error) {
	var r resUsage
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.heapBefore = ms.HeapAlloc
	var ruBefore, ruAfter syscall.Rusage
	must(syscall.Getrusage(syscall.RUSAGE_SELF, &ruBefore))
	r.maxRSSBefore = rssBytes(ruBefore.Maxrss)

	var peak atomic.Uint64
	peak.Store(ms.HeapAlloc)
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()

	start := time.Now()
	err := fn()
	r.wall = time.Since(start)
	close(stop)
	<-sampled

	must(syscall.Getrusage(syscall.RUSAGE_SELF, &ruAfter))
	r.maxRSSAfter = rssBytes(ruAfter.Maxrss)
	r.cpuUser = timevalDuration(ruAfter.Utime) - timevalDuration(ruBefore.Utime)
	r.cpuSys = timevalDuration(ruAfter.Stime) - timevalDuration(ruBefore.Stime)
	r.heapPeak = peak.Load()
	return r, err
}

func timevalDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// rssBytes normalises getrusage maxrss: bytes on darwin, kilobytes on linux.
func rssBytes(maxrss int64) int64 {
	if runtime.GOOS == "linux" {
		return maxrss * 1024
	}
	return maxrss
}

// runRepoMode compacts a COPY of an arbitrary real checkout (e.g. one scp'd
// from production) and prints the same instrumentation. Read-only towards the
// original. GCPOC_REPEAT=N re-runs the compaction N times in this one process
// — the algorithm is idempotent in content but does its full work every time,
// so a heap/RSS/fd count that climbs across iterations is a leak.
func runRepoMode(orig string) {
	if _, err := os.Stat(filepath.Join(orig, ".git")); err != nil {
		fail("%s does not look like a checkout: %v", orig, err)
	}
	repeat := envInt("GCPOC_REPEAT", 1)
	tmp, err := os.MkdirTemp("", "gc-poc-repo-*")
	must(err)
	work := filepath.Join(tmp, "copy")
	copyTree(orig, work)
	fmt.Printf("compacting a copy of %s in %s (window=%d, repeat=%d)\n\n", orig, work, packWindow(), repeat)
	report(work, "before")
	objectsBefore := dirSize(filepath.Join(work, ".git", "objects"))
	pre := worktreeManifest(work)
	haveGit := false
	lsBefore := ""
	if _, err := exec.LookPath("git"); err == nil {
		haveGit = true
		lsBefore = gitOut(work, "ls-tree", "-r", "HEAD")
	}

	var st compactStats
	var res resUsage
	for i := 1; i <= repeat; i++ {
		res, err = measured(func() error {
			var cerr error
			st, cerr = compact(work)
			return cerr
		})
		must(err)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Printf("iter %2d: wall=%-8v cpu=%-8v heapPeak=%-9s heapAfterGC=%-9s rss=%-9s fds=%d\n",
			i, res.wall.Round(time.Millisecond), (res.cpuUser + res.cpuSys).Round(time.Millisecond),
			human(int64(res.heapPeak)), human(int64(ms.HeapAlloc)), human(res.maxRSSAfter), countFDs())
	}

	objectsAfter := dirSize(filepath.Join(work, ".git", "objects"))
	report(work, "after")
	fmt.Printf("\nobjects on disk : before=%s  PEAK during=%s  after=%s\n",
		human(objectsBefore), human(st.packDirPeak), human(objectsAfter))
	fmt.Printf("last iteration  : wall=%v cpu user=%v sys=%v heap peak=%s ΔmaxRSS=%s\n",
		res.wall.Round(time.Millisecond), res.cpuUser.Round(time.Millisecond), res.cpuSys.Round(time.Millisecond),
		human(int64(res.heapPeak)), human(res.maxRSSAfter-res.maxRSSBefore))
	fmt.Printf("objects packed  : %d; packs deleted: %d; loose pruned: %d; shallow %d→%d; tag refs dropped: %d\n",
		st.reachable, st.packsDeleted, st.loosePruned, st.shallowBefore, st.shallowAfter, st.tagsDropped)

	manifestOK := worktreeManifest(work) == pre
	if !manifestOK {
		fail("worktree changed across compaction")
	}
	fmt.Println("worktree manifest identical: OK")
	lsOK, fsckMsg := true, "skipped (no git)"
	if haveGit {
		lsOK = gitOut(work, "ls-tree", "-r", "HEAD") == lsBefore
		if !lsOK {
			fail("ls-tree -r HEAD changed across compaction")
		}
		fmt.Println("git ls-tree -r HEAD identical: OK")
		ferr := gitRun(work, "fsck", "--strict", "--no-dangling")
		fsckMsg = okOr(ferr, "CLEAN")
		fmt.Printf("git fsck --strict: %s\n", fsckMsg)
	}

	// Machine-readable line for batch scaling analysis.
	worktreeBytes := dirSize(work) - dirSize(filepath.Join(work, ".git"))
	fmt.Printf("SUMMARY repo=%s worktree=%d objects_before=%d objects_after=%d reachable=%d wall_ms=%d cpu_ms=%d heap_peak=%d rss_delta=%d packs_deleted=%d manifest=%v lstree=%v fsck=%q\n",
		filepath.Base(strings.TrimRight(orig, "/")), worktreeBytes, objectsBefore, objectsAfter, st.reachable,
		res.wall.Milliseconds(), (res.cpuUser + res.cpuSys).Milliseconds(),
		res.heapPeak, res.maxRSSAfter-res.maxRSSBefore, st.packsDeleted, manifestOK, lsOK, fsckMsg)

	must(os.RemoveAll(tmp))
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// countFDs counts this process's open file descriptors (leak detector).
// Probing fcntl works on both darwin and linux; /dev/fd is unreadable from a
// plain ReadDir on macOS devfs.
func countFDs() int {
	n := 0
	for fd := 0; fd < 4096; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Upstream side: a real git repo, driven through the git binary.
// ---------------------------------------------------------------------------

// seedBigUpstream builds a repo big enough for resource numbers to mean
// something: ~3000 source files plus five 2MB incompressible blobs
// (~18MB worktree). Pushes rewrite one blob + 100 sources, so every fetch
// pack carries megabytes.
func seedBigUpstream(dir string) {
	must(os.MkdirAll(dir, 0o755))
	gitMust(dir, "init", "-b", branch)
	gitMust(dir, "config", "user.email", "poc@example.com")
	gitMust(dir, "config", "user.name", "poc")
	for i := range 3000 {
		writeFile(dir, fmt.Sprintf("src/pkg%02d/file%04d.go", i%40, i), fileBody(i, 0))
	}
	for i := range 5 {
		writeRandomBlob(dir, i, 0)
	}
	gitMust(dir, "add", "-A")
	gitMust(dir, "commit", "-q", "-m", "seed")
}

func pushBigUpstream(dir string, n int) {
	for i := range 100 {
		f := (n*31 + i*17) % 3000
		writeFile(dir, fmt.Sprintf("src/pkg%02d/file%04d.go", f%40, f), fileBody(f, n))
	}
	writeRandomBlob(dir, n%5, n)
	gitMust(dir, "add", "-A")
	gitMust(dir, "commit", "-q", "-m", fmt.Sprintf("big push %d", n))
}

// writeRandomBlob writes a deterministic 2MB pseudo-random (incompressible)
// blob — the realistic worst case for pack size.
func writeRandomBlob(dir string, id, rev int) {
	rnd := rand.New(rand.NewSource(int64(id)*1_000_003 + int64(rev)))
	b := make([]byte, 2<<20)
	rnd.Read(b)
	full := filepath.Join(dir, fmt.Sprintf("assets/blob%d.bin", id))
	must(os.MkdirAll(filepath.Dir(full), 0o755))
	must(os.WriteFile(full, b, 0o644))
}

func seedUpstream(dir string) {
	must(os.MkdirAll(dir, 0o755))
	gitMust(dir, "init", "-b", branch)
	gitMust(dir, "config", "user.email", "poc@example.com")
	gitMust(dir, "config", "user.name", "poc")
	// Realistic-ish payload: 200 source files a few KB each + 3 bigger blobs.
	for i := range 200 {
		writeFile(dir, fmt.Sprintf("src/pkg%02d/file%03d.go", i%20, i), fileBody(i, 0))
	}
	for i := range 3 {
		writeFile(dir, fmt.Sprintf("assets/big%d.bin", i), strings.Repeat(fmt.Sprintf("payload-%d-", i), 20000)) // ~200KB
	}
	gitMust(dir, "add", "-A")
	gitMust(dir, "commit", "-q", "-m", "seed")
}

// pushUpstreamMulti simulates a push of `count` commits landing at once.
func pushUpstreamMulti(dir string, n, count int) {
	for c := range count {
		pushUpstream(dir, n+c)
	}
}

// pushUpstream simulates one developer push: touch 5 files, add 1.
func pushUpstream(dir string, n int) {
	for i := range 5 {
		f := (n*7 + i*13) % 200
		writeFile(dir, fmt.Sprintf("src/pkg%02d/file%03d.go", f%20, f), fileBody(f, n))
	}
	writeFile(dir, fmt.Sprintf("src/new/added%04d.go", n), fileBody(1000+n, n))
	gitMust(dir, "add", "-A")
	gitMust(dir, "commit", "-q", "-m", fmt.Sprintf("push %d", n))
}

func fileBody(id, rev int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package p%02d\n\n// file %d revision %d\n", id%20, id, rev)
	for l := range 80 {
		fmt.Fprintf(&b, "func fn_%d_%d_%d() int { return %d }\n", id, rev, l, l*id+rev)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Measurement + oracle helpers.
// ---------------------------------------------------------------------------

func report(dir, label string) {
	objects := filepath.Join(dir, ".git", "objects")
	fmt.Printf("%-42s packs=%-3d objects=%-9s shallow-entries=%d\n",
		label+":", packCount(dir), human(dirSize(objects)), shallowCount(dir))
}

func packCount(dir string) int {
	m, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	return len(m)
}

func shallowCount(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "shallow"))
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(b)))
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// worktreeManifest is path→sha256 over every non-.git file, one string.
func worktreeManifest(dir string) string {
	var lines []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		f.Close()
		rel, _ := filepath.Rel(dir, path)
		lines = append(lines, rel+" "+hex.EncodeToString(h.Sum(nil)))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// verifyEveryBlobReadable re-reads every blob of HEAD's tree through go-git
// and compares against the worktree file bytes.
func verifyEveryBlobReadable(dir string) {
	repo, err := git.PlainOpen(dir)
	must(err)
	head, err := repo.Head()
	must(err)
	commit, err := repo.CommitObject(head.Hash())
	must(err)
	tree, err := commit.Tree()
	must(err)
	n := 0
	must(tree.Files().ForEach(func(f *object.File) error {
		blob, err := f.Contents()
		if err != nil {
			return fmt.Errorf("read blob %s: %w", f.Name, err)
		}
		disk, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil {
			return err
		}
		if blob != string(disk) {
			return fmt.Errorf("blob/worktree mismatch at %s", f.Name)
		}
		n++
		return nil
	}))
	fmt.Printf("every HEAD blob readable via go-git and byte-identical to worktree (%d files): OK\n", n)
}

func copyTree(src, dst string) {
	// -c asks APFS for a copy-on-write clone (instant, no extra blocks until
	// the compaction rewrites packs); fall back to a plain copy elsewhere.
	if err := exec.Command("cp", "-Rc", src, dst).Run(); err == nil {
		return
	}
	_ = os.RemoveAll(dst)
	must(exec.Command("cp", "-R", src, dst).Run())
}

func gitMust(dir string, args ...string) {
	if err := gitRun(dir, args...); err != nil {
		fail("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	must(err)
	return strings.TrimSpace(string(out))
}

func writeFile(dir, rel, content string) {
	full := filepath.Join(dir, rel)
	must(os.MkdirAll(filepath.Dir(full), 0o755))
	must(os.WriteFile(full, []byte(content), 0o644))
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func short(sha string) string { return sha[:8] }

func okOr(err error, okMsg string) string {
	if err == nil {
		return okMsg
	}
	return "FAILED: " + err.Error()
}

func emptyErr(s string) error {
	if s == "" {
		return nil
	}
	return fmt.Errorf("non-empty: %q", s)
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
