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
// it drops refs/tags/*, walks the objects reachable from the remaining refs
// (honouring .git/shallow graft points exactly like git does) plus any
// explicitly protected commits, encodes that set into one new pack, deletes
// the old packs and loose objects, and rewrites .git/shallow to the entries
// that still exist. `git fsck --strict` is clean afterwards and the worktree
// is untouched — validated byte-for-byte against full-history canonical
// clones on 45 real checkouts (spring-boot, grafana, …) by the PoC on branch
// poc/gc-compaction (server/cmd/gc-poc).
//
// Cost model, measured on those 45 checkouts: time is linear —
// ~0.2–1.4 ms CPU per reachable object plus ~0.2 s per emitted GB (zlib);
// memory is linear in the SNAPSHOT size (not the store size) at roughly 3×
// the uncompressed content, because go-git's packfile encoder materialises
// object data. A typical 60 MB checkout compacts in single-digit seconds
// within a few hundred MB of transient heap; compactMu keeps concurrent
// clone jobs from stacking those peaks.
//
// The delta window is 0 on purpose: after tags are dropped the reachable set
// is essentially a single snapshot, and the PoC measured window=10 at −17%
// pack size for 2.9× the CPU.

import (
	"fmt"
	"os"
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
// before the next CloneOrFetch rewrites the store. Each fetch adds one
// snapshot-sized pack, so the steady-state disk overhead between compactions
// is bounded by (threshold-1) worktree-sized packs, and the compaction cost
// is amortised over that many pushes.
const compactPackThreshold = 4

// compactMu serialises compactions across concurrent clone jobs. The
// transient heap of one compaction is ~3× the repo's uncompressed snapshot;
// letting several worker goroutines pay that simultaneously is how an 8 GB
// host gets OOM-killed.
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

// needsCompaction reports whether the checkout's object store warrants a
// rewrite: enough accumulated fetch packs, or tag refs left behind by
// pre-NoTags server versions (their snapshots dominate the store, and with
// Tags:NoTags on every fetch they will not come back). The tag check makes
// the first post-upgrade update of every existing checkout clean it — there
// is deliberately no separate migration.
func needsCompaction(dir string) bool {
	if packfileCount(dir) >= compactPackThreshold {
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
	defer refs.Close()
	found := false
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), tagRefPrefix) {
			found = true
			return storer.ErrStop
		}
		return nil
	})
	return found
}

// compactCheckout rewrites dir's object store down to the objects reachable
// from its non-tag references plus the protected commits. protect carries
// commits no ref points at that must survive — in practice
// git_repos.indexed_sha, the base of the next incremental tree-diff; entries
// that are zero or absent from the store are skipped.
//
// Failure modes are safe by construction: the new pack is durable before any
// old pack is deleted, so a crash mid-compaction leaves extra packs for the
// next run, never a store missing objects. The caller handles a returned
// error by discarding the checkout and re-cloning.
func compactCheckout(dir string, protect ...plumbing.Hash) (CompactStats, error) {
	compactMu.Lock()
	defer compactMu.Unlock()

	started := time.Now()
	st := CompactStats{ObjectsBefore: objectsDirSize(dir)}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		return st, fmt.Errorf("open: %w", err)
	}

	// 1. Drop tag refs. cix serves exactly one branch; every tag is a whole
	//    retained snapshot the server never reads.
	refs, err := repo.References()
	if err != nil {
		return st, err
	}
	var tagRefs []plumbing.ReferenceName
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), tagRefPrefix) {
			tagRefs = append(tagRefs, ref.Name())
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	for _, name := range tagRefs {
		if err := repo.Storer.RemoveReference(name); err != nil {
			return st, fmt.Errorf("remove tag ref %s: %w", name, err)
		}
		st.TagRefsDropped++
	}

	// 2. Reachability walk from the remaining refs + protected commits,
	//    with git's shallow semantics.
	shallowList, err := repo.Storer.Shallow()
	if err != nil {
		return st, fmt.Errorf("read shallow: %w", err)
	}
	shallowSet := make(map[plumbing.Hash]struct{}, len(shallowList))
	for _, h := range shallowList {
		shallowSet[h] = struct{}{}
	}
	w := &objectWalker{storer: repo.Storer, shallow: shallowSet, seen: map[plumbing.Hash]struct{}{}}
	refs, err = repo.References()
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
		if _, gerr := object.GetObject(repo.Storer, h); gerr != nil {
			// Not in the store (already gc'd away, or a bogus SHA) —
			// nothing to protect.
			continue
		}
		if err := w.walk(h); err != nil {
			return st, fmt.Errorf("walk protected %s: %w", h, err)
		}
	}
	st.Reachable = len(w.seen)
	objs := make([]plumbing.Hash, 0, len(w.seen))
	for h := range w.seen {
		objs = append(objs, h)
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

	// 4. Only now that the new pack is durable: delete the old ones.
	for _, h := range oldPacks {
		if h == newPack {
			continue
		}
		if err := pos.DeleteOldObjectPackAndIndex(h, time.Time{}); err != nil {
			return st, fmt.Errorf("delete pack %s: %w", h, err)
		}
		st.PacksDeleted++
	}

	// 5. Loose objects: everything reachable is in the new pack, so every
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

	// 6. .git/shallow gains one graft entry per fetch; entries whose commit
	//    was just dropped would make real git tooling error out ("did not
	//    find object for shallow …"), so keep only entries still present.
	kept := shallowList[:0]
	for _, h := range shallowList {
		if _, reachable := w.seen[h]; reachable {
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

// objectWalker collects the reachable object set. It is go-git's own
// objectWalker (repository.go uses it for RepackObjects) with three
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
type objectWalker struct {
	storer  storage.Storer
	shallow map[plumbing.Hash]struct{}
	seen    map[plumbing.Hash]struct{}
}

func (w *objectWalker) walk(hash plumbing.Hash) error {
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
			break
		}
		for _, p := range obj.ParentHashes {
			if _, ok := w.seen[p]; ok {
				continue
			}
			// A parent this shallow store never fetched: boundary, not error.
			if _, gerr := object.GetObject(w.storer, p); gerr == plumbing.ErrObjectNotFound {
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
		// Leaf.
	case *object.Tag:
		return w.walk(obj.Target)
	default:
		return fmt.Errorf("unknown object type %T at %s", obj, hash)
	}
	return nil
}

// objectsDirSize sums .git/objects — a few packs plus loose files, so the
// walk is cheap. Best effort; 0 on error.
func objectsDirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(filepath.Join(dir, ".git", "objects"), func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
