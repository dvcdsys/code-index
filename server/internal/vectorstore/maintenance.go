package vectorstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
)

// CollectionInfo describes one collection that is resident in the chromem-go
// process image. Everything here is read from memory — chromem eagerly loads
// every document of every collection at Open and never evicts, so counting is
// free and does not touch the disk.
type CollectionInfo struct {
	Name      string // chromem collection name, e.g. "project_<md5hex>"
	Documents int    // resident document count
	Dir       string // on-disk persist directory for this collection
}

// Maintainer is the admin-only maintenance surface over the vector store.
//
// Deliberately NOT part of Interface. Interface is the hot path (upsert /
// search / delete-by-file) consumed by the indexer, repojobs and the search
// handlers, and its doc comment promises it "mirrors *Store exactly". These
// methods address collections by their raw chromem name rather than by project
// path — meaningless to every Interface consumer, and folding them in would
// force any future fake to stub four extra methods it never calls. Handlers
// type-assert instead; in production the wired value is a *Holder, which
// implements this, so the assertion always succeeds.
type Maintainer interface {
	// ListCollections returns every resident collection. Cheap: no I/O.
	ListCollections() []CollectionInfo
	// DeleteCollectionByName drops a collection by its raw chromem name,
	// removing both the in-memory documents and the on-disk directory.
	DeleteCollectionByName(name string) error
	// CollectionDir is the on-disk directory backing a project's collection.
	CollectionDir(projectPath string) string
	// BaseDir is the persist directory the store was opened at.
	BaseDir() string
}

var (
	_ Maintainer = (*Store)(nil)
	_ Maintainer = (*Holder)(nil)
)

// collectionDirName reproduces chromem-go's hash2hex: the first 4 bytes of the
// SHA-256 of the collection name, hex-encoded — 8 characters.
//
// Pinned to github.com/philippgille/chromem-go v0.7.0, persistence.go:22. The
// function is unexported there and Collection.persistDirectory is unexported
// too, so a local copy is the only way to resolve a collection's directory.
// TestCollectionDirName_MatchesChromemLayout guards the pin: it writes through
// a real Store and stats the path this function returns, so a chromem upgrade
// that changes the layout fails the build rather than silently reporting
// missing directories (which is exactly the bug this replaces — the previous
// code joined the logical collection name onto the namespace dir, a path that
// never exists, so every project's vector-store size came back empty).
func collectionDirName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:4])
}

// ListCollections implements Maintainer. Results are sorted by name so callers
// (and their tests) see a stable order.
func (s *Store) ListCollections() []CollectionInfo {
	cols := s.db.ListCollections()
	out := make([]CollectionInfo, 0, len(cols))
	for name, col := range cols {
		out = append(out, CollectionInfo{
			Name:      name,
			Documents: col.Count(),
			Dir:       filepath.Join(s.baseDir, collectionDirName(name)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeleteCollectionByName implements Maintainer.
//
// Caveat worth knowing at every call site: chromem's DeleteCollection is a
// silent no-op returning nil for a name it does not hold in memory. It can
// therefore never remove a directory that failed to load at startup — those
// have to be removed with os.RemoveAll by the caller.
func (s *Store) DeleteCollectionByName(name string) error {
	if err := s.db.DeleteCollection(name); err != nil {
		return fmt.Errorf("vectorstore delete collection %q: %w", name, err)
	}
	return nil
}

// CollectionDir implements Maintainer.
func (s *Store) CollectionDir(projectPath string) string {
	return filepath.Join(s.baseDir, collectionDirName(collectionName(projectPath)))
}

// BaseDir implements Maintainer.
func (s *Store) BaseDir() string { return s.baseDir }

// ListCollections proxies to the active store; nil store yields nil.
func (h *Holder) ListCollections() []CollectionInfo {
	s := h.current()
	if s == nil {
		return nil
	}
	return s.ListCollections()
}

// DeleteCollectionByName proxies to the active store.
func (h *Holder) DeleteCollectionByName(name string) error {
	s := h.current()
	if s == nil {
		return errNotInitialised
	}
	return s.DeleteCollectionByName(name)
}

// CollectionDir proxies to the active store; nil store yields "".
func (h *Holder) CollectionDir(projectPath string) string {
	s := h.current()
	if s == nil {
		return ""
	}
	return s.CollectionDir(projectPath)
}

// BaseDir proxies to the active store; nil store yields "".
func (h *Holder) BaseDir() string {
	s := h.current()
	if s == nil {
		return ""
	}
	return s.BaseDir()
}
