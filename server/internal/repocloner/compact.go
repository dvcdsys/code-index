package repocloner

// In-process object-store compaction for shallow checkouts.
//
// Why it exists: the update path is fetch(Depth:1) + hard reset. go-git
// persists ONE NEW PACKFILE per fetch — and each of those packs is a
// near-full snapshot of the tree, not a delta — while the reset makes the
// previously fetched snapshot unreachable. go-git has no gc and the
// distroless runtime has no git binary, so without intervention the object
// store grows with every upstream push, forever (this is what took a ~4.5 GB
// production fleet of checkouts to 76 GB). On top of that, go-git's clone
// default is Tags:AllTags, so a day-zero clone of a tag-rich repo carries a
// full shallow snapshot PER TAG (spring-boot: 391 tags → a 102 MB store for
// a 39 MB worktree) that cix, which indexes exactly one branch, never reads.
//
// Compaction rewrites the store down to what the server actually uses:
// it walks the objects reachable from the non-tag refs (honouring
// .git/shallow graft points exactly like git does) plus any explicitly
// protected commits, encodes that set into one new pack, drops refs/tags/*,
// deletes the old packs and loose objects, and rewrites .git/shallow to the
// entries that still exist. `git fsck --strict` is clean afterwards and the
// worktree is untouched — validated byte-for-byte against full-history
// canonical clones on 45 real checkouts (spring-boot, grafana, …) by the PoC
// on branch poc/gc-compaction (server/cmd/gc-poc).
//
// Cost model, measured on those 45 checkouts: time is linear —
// ~0.2–1.4 ms CPU per reachable object plus ~0.2 s per emitted GB (zlib);
// memory is linear in the SNAPSHOT size (not the store size) at roughly 3×
// the uncompressed content, because go-git's packfile encoder materialises
// object data. A typical 60 MB checkout compacts in single-digit seconds
// within a few hundred MB of transient heap; MaybeCompact's global gate
// keeps concurrent clone jobs from stacking those peaks.
//
// Crash-safety ordering inside compactCheckout: the new pack is durable
// before anything is deleted, and the tag refs are removed before the old
// packs go away (so a ref never dangles over a missing object). A crash at
// any point leaves either extra packs or dropped-tags-with-bloat — both
// states re-trigger needsCompaction (pack count and store/worktree ratio
// respectively) and heal on the next update.
//
// The delta window is 0 on purpose: after tags are dropped the reachable set
// is essentially a single snapshot, and the PoC measured window=10 at −17%
// pack size for 2.9× the CPU.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
)

// compactPackThreshold is how many packfiles a checkout may accumulate
// before the next update compacts the store. Each fetch adds one
// snapshot-sized pack, so the steady-state disk overhead between compactions
// is bounded by (threshold-1) worktree-sized packs, and the compaction cost
// is amortised over that many pushes.
const compactPackThreshold = 4

// Ratio backstop: a store this many times larger than the worktree it
// serves (and above the floor) is carrying dead weight regardless of pack
// count. This is what re-arms cleanup for a checkout whose tag refs were
// dropped by a compaction that crashed before deleting the old packs — pack
// count alone would never fire again on a quiet repo. A healthy compacted
// store is zlib-compressed and smaller than its worktree, so legitimate
// checkouts sit far below 2×.
const (
	compactRatioTrigger = 2
	compactRatioFloor   = 1 << 20 // ignore ratio noise on tiny stores
)

// compactMu serialises compactions across concurrent clone jobs. The
// transient heap of one compaction is ~3× the repo's uncompressed snapshot;
// letting several worker goroutines pay that simultaneously is how an 8 GB
// host gets OOM-killed. MaybeCompact acquires it BEFORE the caller-supplied
// per-repo write lock, so a job queued on this mutex never stalls another
// repo's readers.
var compactMu sync.Mutex

const tagRefPrefix = "refs/tags/"

// CompactStats reports what one compaction did. Purely informational —
// callers log it.
type CompactStats struct {
	ObjectsBefore  int64 // .git/objects bytes before
	ObjectsAfter   int64 // .git/objects bytes after
	Reachable      int   // objects written to the new pack
	PacksDeleted   int
	LoosePruned    int
	TagRefsDropped int
	Duration       time.Duration
}

// MaybeCompact compacts dir's object store when it needs it (see
// needsCompaction) and reports what it did; (nil, nil) means "nothing to
// do". withWrite, when non-nil, must serialise the on-disk mutation against
// concurrent readers of this checkout (repojobs passes RepoLocks.WithWrite);
// it is acquired AFTER the global compaction gate, so waiting for another
// repo's compaction never happens while holding this repo's lock.
//
// A compaction error leaves the checkout exactly as the preceding update
// left it — valid — so callers must NOT treat it as reason to discard the
// checkout: log it and move on; the trigger re-fires on the next update.
//
// protect lists commit SHAs that must survive even though no ref points at
// them: git_repos.indexed_sha (the base of the next incremental tree-diff)
// and the pre-fetch HEAD (the target of a possibly still-queued index job).
// Empty strings and SHAs absent from the store are skipped.
func MaybeCompact(ctx context.Context, dir string, withWrite func(func() error) error, protect ...string) (*CompactStats, error) {
	if ctx.Err() != nil || !needsCompaction(dir) {
		return nil, nil
	}
	var hashes []plumbing.Hash
	for _, s := range protect {
		if s = strings.TrimSpace(s); s != "" {
			hashes = append(hashes, plumbing.NewHash(s))
		}
	}

	compactMu.Lock()
	defer compactMu.Unlock()
	// The queue on compactMu can be long on upgrade day; don't start work
	// for a request that is already gone.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var st CompactStats
	run := func() error {
		var err error
		st, err = compactCheckout(ctx, dir, hashes...)
		return err
	}
	var err error
	if withWrite != nil {
		err = withWrite(run)
	} else {
		err = run()
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// needsCompaction reports whether the checkout's object store warrants a
// rewrite. Three triggers:
//
//   - packfileCount ≥ compactPackThreshold: accumulated fetch packs.
//   - tag refs present: snapshots left behind by pre-NoTags server versions.
//     Their objects dominate the store, and with Tags:NoTags on every fetch
//     they will not come back — this makes the first post-upgrade update of
//     every existing checkout clean it, with no separate migration.
//   - store ≥ compactRatioTrigger × worktree (packs ≥ 2 only): the backstop
//     that re-arms cleanup after a crash mid-compaction dropped the tag refs
//     without reclaiming their objects. Gated on pack count so the worktree
//     walk is not paid on the common single-pack steady state.
func needsCompaction(dir string) bool {
	packs := packfileCount(dir)
	if packs >= compactPackThreshold {
		return true
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false
	}
	refs, err := repo.References()
	if err != nil {
		return false
	}
	found := false
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), tagRefPrefix) {
			found = true
			return storer.ErrStop
		}
		return nil
	})
	refs.Close()
	if found {
		return true
	}
	if packs >= 2 {
		if objects := objectsDirSize(dir); objects > compactRatioFloor {
			return objects >= compactRatioTrigger*worktreeSize(dir)
		}
	}
	return false
}

// compactCheckout rewrites dir's object store down to the objects reachable
// from its non-tag references plus the protected commits, then drops the tag
// refs. See the package comment for the crash-safety ordering. The context
// is honoured between phases and inside the walk; cancellation before the
// deletion phase leaves the store untouched (bar an extra pack).
func compactCheckout(ctx context.Context, dir string, protect ...plumbing.Hash) (CompactStats, error) {
	started := time.Now()
	st := CompactStats{ObjectsBefore: objectsDirSize(dir)}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		return st, fmt.Errorf("open: %w", err)
	}

	// 1. One pass over the refs: non-tag hash refs seed the walk, tag refs
	//    are remembered for deletion later. cix serves exactly one branch;
	//    every tag is a whole retained snapshot the server never reads.
	var roots []plumbing.Hash
	var tagRefs []plumbing.ReferenceName
	refs, err := repo.References()
	if err != nil {
		return st, err
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), tagRefPrefix) {
			tagRefs = append(tagRefs, ref.Name())
			return nil
		}
		if ref.Type() == plumbing.HashReference {
			roots = append(roots, ref.Hash())
		}
		return nil
	})
	refs.Close()
	if err != nil {
		return st, err
	}
	for _, h := range protect {
		if h.IsZero() {
			continue
		}
		// Absent is fine (already gc'd away, or a bogus SHA — nothing to
		// protect); any OTHER failure is a store problem the caller must
		// hear about, not a silent loss of the diff base.
		if _, gerr := object.GetObject(repo.Storer, h); gerr != nil {
			if errors.Is(gerr, plumbing.ErrObjectNotFound) {
				continue
			}
			return st, fmt.Errorf("probe protected %s: %w", h, gerr)
		}
		roots = append(roots, h)
	}

	// 2. Reachability walk with git's shallow semantics.
	shallowList, err := repo.Storer.Shallow()
	if err != nil {
		return st, fmt.Errorf("read shallow: %w", err)
	}
	shallowSet := make(map[plumbing.Hash]struct{}, len(shallowList))
	for _, h := range shallowList {
		shallowSet[h] = struct{}{}
	}
	seen, err := walkReachable(ctx, repo.Storer, roots, shallowSet)
	if err != nil {
		return st, fmt.Errorf("reachability walk: %w", err)
	}
	st.Reachable = len(seen)
	objs := make([]plumbing.Hash, 0, len(seen))
	for h := range seen {
		objs = append(objs, h)
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}

	// 3. Write the reachable set as one new pack. PackfileWriter lands it in
	//    objects/pack with a proper idx before we touch anything old.
	pos, ok := repo.Storer.(storer.PackedObjectStorer)
	if !ok {
		return st, fmt.Errorf("storage does not support packed objects")
	}
	oldPacks, err := pos.ObjectPacks()
	if err != nil {
		return st, err
	}
	pfw, ok := repo.Storer.(storer.PackfileWriter)
	if !ok {
		return st, fmt.Errorf("storage does not support packfile writing")
	}
	wc, err := pfw.PackfileWriter()
	if err != nil {
		return st, err
	}
	enc := packfile.NewEncoder(wc, repo.Storer, false)
	// Window 0: no delta search — see the package comment.
	newPack, err := enc.Encode(objs, 0)
	if cerr := wc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return st, fmt.Errorf("encode pack: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}

	// 4. The new pack is durable — now drop the tag refs, BEFORE the old
	//    packs: a tag ref must never outlive its objects (fetch negotiation
	//    advertises refs as haves), and the reverse crash window — tags
	//    gone, bloat still on disk — is re-armed by the ratio trigger.
	for _, name := range tagRefs {
		if err := repo.Storer.RemoveReference(name); err != nil {
			return st, fmt.Errorf("remove tag ref %s: %w", name, err)
		}
		st.TagRefsDropped++
	}

	// 5. Delete the old packs.
	for _, h := range oldPacks {
		if h == newPack {
			continue
		}
		if err := pos.DeleteOldObjectPackAndIndex(h, time.Time{}); err != nil {
			return st, fmt.Errorf("delete pack %s: %w", h, err)
		}
		st.PacksDeleted++
	}

	// 6. Loose objects: everything reachable is in the new pack, so every
	//    loose object is redundant regardless of reachability.
	if los, ok := repo.Storer.(storer.LooseObjectStorer); ok {
		err = los.ForEachObjectHash(func(h plumbing.Hash) error {
			if derr := los.DeleteLooseObject(h); derr != nil {
				return derr
			}
			st.LoosePruned++
			return nil
		})
		if err != nil {
			return st, fmt.Errorf("prune loose: %w", err)
		}
	}

	// 7. .git/shallow gains one graft entry per fetch; entries whose commit
	//    was just dropped would make real git tooling error out ("did not
	//    find object for shallow …"), so keep only entries still present.
	kept := shallowList[:0]
	for _, h := range shallowList {
		if _, reachable := seen[h]; reachable {
			kept = append(kept, h)
		}
	}
	if len(kept) != len(shallowList) {
		if err := repo.Storer.SetShallow(kept); err != nil {
			return st, fmt.Errorf("rewrite shallow: %w", err)
		}
	}

	st.ObjectsAfter = objectsDirSize(dir)
	st.Duration = time.Since(started)
	return st, nil
}

// walkReachable collects every object reachable from roots. It is go-git's
// objectWalker (repository.go uses it for RepackObjects) reworked into an
// iterative worklist — recursion depth would otherwise equal the contiguous
// commit-chain length, and a full-history clone manually seeded into the
// repos dir must not be able to blow the goroutine stack — with three
// behavioural fixes, each of which real checkouts hit immediately:
//
//   - commits listed in .git/shallow are graft points whose parents are
//     never walked — git's own semantics. (Stock go-git follows ParentHashes
//     unconditionally: it crashes on any multi-commit push fetched at
//     Depth:1, and where the chain happens to be complete it retains every
//     previously fetched snapshot forever.)
//   - submodule (gitlink) tree entries are skipped — the hash is a commit in
//     a different repository. (Stock go-git crashes.)
//   - blobs reached as objects (via symlink and other non-regular-file tree
//     entries) are accepted leaves. (Stock go-git errors "unknown object".)
func walkReachable(ctx context.Context, s storage.Storer, roots []plumbing.Hash, shallow map[plumbing.Hash]struct{}) (map[plumbing.Hash]struct{}, error) {
	seen := make(map[plumbing.Hash]struct{})
	stack := make([]plumbing.Hash, len(roots))
	copy(stack, roots)
	visited := 0
	for len(stack) > 0 {
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[hash]; ok {
			continue
		}
		// Keep cancellation prompt without paying ctx.Err() per object.
		if visited++; visited%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		obj, err := object.GetObject(s, hash)
		if err != nil {
			return nil, fmt.Errorf("get object %s: %w", hash, err)
		}
		seen[hash] = struct{}{}
		switch obj := obj.(type) {
		case *object.Commit:
			stack = append(stack, obj.TreeHash)
			if _, grafted := shallow[obj.Hash]; grafted {
				continue
			}
			for _, p := range obj.ParentHashes {
				if _, ok := seen[p]; ok {
					continue
				}
				// A parent this shallow store never fetched: boundary,
				// not error.
				if _, gerr := object.GetObject(s, p); gerr != nil {
					if errors.Is(gerr, plumbing.ErrObjectNotFound) {
						continue
					}
					return nil, fmt.Errorf("probe parent %s: %w", p, gerr)
				}
				stack = append(stack, p)
			}
		case *object.Tree:
			for i := range obj.Entries {
				e := obj.Entries[i]
				if e.Mode == filemode.Submodule {
					continue
				}
				if e.Mode|0o755 == filemode.Executable { // plain blob, any file mode
					seen[e.Hash] = struct{}{}
					continue
				}
				stack = append(stack, e.Hash)
			}
		case *object.Blob:
			// Leaf.
		case *object.Tag:
			stack = append(stack, obj.Target)
		default:
			return nil, fmt.Errorf("unknown object type %T at %s", obj, hash)
		}
	}
	return seen, nil
}

// objectsDirSize sums .git/objects — a few packs plus loose files, so the
// walk is cheap. Best effort; 0 on error.
func objectsDirSize(dir string) int64 {
	return treeSize(filepath.Join(dir, ".git", "objects"), false)
}

// worktreeSize sums the checkout's payload, excluding .git. Only consulted
// by the ratio backstop, which is gated on pack count ≥ 2.
func worktreeSize(dir string) int64 {
	return treeSize(dir, true)
}

func treeSize(dir string, skipDotGit bool) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDotGit && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
