package vectorstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStoreLayout_PinsOnDiskShape replaces the old chromem layout pin
// (TestCollectionDirName_MatchesChromemLayout). There are no per-collection
// directories any more: a namespace is one SQLite file inside the namespace
// directory, and the maintenance surface reports exactly that.
func TestStoreLayout_PinsOnDiskShape(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	const project = "/some/project"
	if err := s.UpsertChunks(context.Background(), project,
		[]Chunk{{Content: "x", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}},
	); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if got, want := s.BaseDir(), filepath.Clean(dir); got != want {
		t.Errorf("BaseDir = %q, want %q", got, want)
	}
	if got, want := s.DBPath(), filepath.Join(dir, "vectors.db"); got != want {
		t.Errorf("DBPath = %q, want %q", got, want)
	}
	if info, err := os.Stat(s.DBPath()); err != nil {
		t.Fatalf("stat %q: %v", s.DBPath(), err)
	} else if info.IsDir() {
		t.Fatalf("%q is a directory, want a file", s.DBPath())
	}
	// Nothing else is created inside the namespace directory besides the
	// database and its WAL sidecars.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("unexpected directory %q inside the namespace dir", e.Name())
		}
	}

	size, ok := s.CollectionSizeBytes(project)
	if !ok {
		t.Fatal("CollectionSizeBytes reported no collection for a project that has one")
	}
	if size <= 0 {
		t.Errorf("CollectionSizeBytes = %d, want > 0", size)
	}
	if _, ok := s.CollectionSizeBytes("/never/indexed"); ok {
		t.Error("CollectionSizeBytes reported a collection for an unknown project")
	}
}

// The page size is a measured choice (see sqlite.go), so it is pinned: 16 KiB
// pages push every page-cache buffer past modernc.org/memory's slab limit and
// cost two syscalls each.
func TestFreshDatabaseUsesTunedPageSize(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var got int
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&got); err != nil {
		t.Fatalf("read page_size: %v", err)
	}
	if got != pageSize {
		t.Errorf("page_size = %d, want %d", got, pageSize)
	}
	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
}

func TestListCollections_ReportsNameCountAndSize(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	chunk := func(p string) ([]Chunk, [][]float32) {
		return []Chunk{{Content: "content", FilePath: p, StartLine: 1, EndLine: 2}}, [][]float32{{1, 0}}
	}
	c1, e1 := chunk("a.go")
	if err := s.UpsertChunks(ctx, "/one", c1, e1); err != nil {
		t.Fatalf("upsert one: %v", err)
	}
	c2, e2 := chunk("b.go")
	if err := s.UpsertChunks(ctx, "/two", c2, e2); err != nil {
		t.Fatalf("upsert two: %v", err)
	}

	got := s.ListCollections()
	if len(got) != 2 {
		t.Fatalf("ListCollections returned %d collections, want 2: %+v", len(got), got)
	}
	byName := map[string]CollectionInfo{}
	for _, c := range got {
		byName[c.Name] = c
	}
	for _, p := range []string{"/one", "/two"} {
		info, ok := byName[CollectionName(p)]
		if !ok {
			t.Fatalf("no collection reported for %q (got %+v)", p, got)
		}
		if info.Documents != 1 {
			t.Errorf("%q documents = %d, want 1", p, info.Documents)
		}
		want, _ := s.CollectionSizeBytes(p)
		if info.SizeBytes != want {
			t.Errorf("%q size = %d, want %d (CollectionSizeBytes)", p, info.SizeBytes, want)
		}
	}
}

func TestDeleteCollectionByName_RemovesDocuments(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.UpsertChunks(ctx, "/gone",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}},
	); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteCollectionByName(CollectionName("/gone")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := s.ListCollections(); len(got) != 0 {
		t.Errorf("ListCollections after delete = %+v, want empty", got)
	}
	if got := s.Count("/gone"); got != 0 {
		t.Errorf("Count after delete = %d, want 0", got)
	}
	// The content table must not keep orphan rows behind.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vector_contents`).Scan(&n); err != nil {
		t.Fatalf("count contents: %v", err)
	}
	if n != 0 {
		t.Errorf("vector_contents holds %d rows after collection delete, want 0", n)
	}
}

// Deleting a name the store never held is a no-op, not an error — the previous
// chromem-backed implementation behaved the same way and callers rely on it.
func TestDeleteCollectionByName_UnknownIsNoOp(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.DeleteCollectionByName("project_deadbeef"); err != nil {
		t.Errorf("delete of unknown collection returned %v, want nil", err)
	}
}

func TestHolder_MaintainerNilSafe(t *testing.T) {
	h := NewHolder(nil)
	if got := h.ListCollections(); got != nil {
		t.Errorf("ListCollections on empty holder = %+v, want nil", got)
	}
	if got, ok := h.CollectionSizeBytes("/p"); ok || got != 0 {
		t.Errorf("CollectionSizeBytes on empty holder = (%d,%v), want (0,false)", got, ok)
	}
	if got := h.BaseDir(); got != "" {
		t.Errorf("BaseDir on empty holder = %q, want empty", got)
	}
	if got := h.DBPath(); got != "" {
		t.Errorf("DBPath on empty holder = %q, want empty", got)
	}
	if got := h.LegacyChromaDir(); got != "" {
		t.Errorf("LegacyChromaDir on empty holder = %q, want empty", got)
	}
	if err := h.DeleteCollectionByName("project_x"); err == nil {
		t.Error("DeleteCollectionByName on empty holder returned nil, want an error")
	}

	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	h.Swap(s)
	if h.BaseDir() != s.BaseDir() {
		t.Errorf("after Swap, holder BaseDir = %q, want %q", h.BaseDir(), s.BaseDir())
	}
	if h.DBPath() != s.DBPath() {
		t.Errorf("after Swap, holder DBPath = %q, want %q", h.DBPath(), s.DBPath())
	}
}

// A store that reports a legacy directory keeps it verbatim: maintenance uses
// it to protect the pre-migration gob files from the stale-namespace scan.
func TestLegacyChromaDirIsReported(t *testing.T) {
	legacy := t.TempDir()
	s, err := OpenWith(Options{Dir: t.TempDir(), LegacyChromaDir: legacy})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if got := s.LegacyChromaDir(); got != filepath.Clean(legacy) {
		t.Errorf("LegacyChromaDir = %q, want %q", got, legacy)
	}

	plain, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer plain.Close()
	if got := plain.LegacyChromaDir(); got != "" {
		t.Errorf("LegacyChromaDir without a legacy store = %q, want empty", got)
	}
}
