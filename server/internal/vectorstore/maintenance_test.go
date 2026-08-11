package vectorstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCollectionDirName_MatchesChromemLayout is the pin on our local copy of
// chromem-go's hash2hex. It writes a real document through a real Store, then
// stats the directory CollectionDir claims. If a chromem upgrade changes the
// on-disk naming, this fails loudly instead of every size silently reading 0.
func TestCollectionDirName_MatchesChromemLayout(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const project = "/some/project"
	if err := s.UpsertChunks(context.Background(), project,
		[]Chunk{{Content: "x", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}},
	); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got := s.CollectionDir(project)
	info, err := os.Stat(got)
	if err != nil {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("CollectionDir(%q) = %q, but it does not exist; base dir holds %v", project, got, names)
	}
	if !info.IsDir() {
		t.Fatalf("CollectionDir returned %q which is not a directory", got)
	}
	if filepath.Dir(got) != s.BaseDir() {
		t.Errorf("CollectionDir parent = %q, want BaseDir %q", filepath.Dir(got), s.BaseDir())
	}
}

func TestListCollections_ReportsNameAndCount(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	chunk := func(p string) ([]Chunk, [][]float32) {
		return []Chunk{{Content: "c", FilePath: p, StartLine: 1, EndLine: 2}}, [][]float32{{1, 0}}
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
		if info.Dir != s.CollectionDir(p) {
			t.Errorf("%q dir = %q, want %q", p, info.Dir, s.CollectionDir(p))
		}
	}
}

func TestDeleteCollectionByName_RemovesDirAndDocuments(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := s.UpsertChunks(ctx, "/gone",
		[]Chunk{{Content: "c", FilePath: "a.go", StartLine: 1, EndLine: 2}},
		[][]float32{{1, 0}},
	); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	dir := s.CollectionDir("/gone")

	if err := s.DeleteCollectionByName(CollectionName("/gone")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("collection dir %q still exists after delete (err=%v)", dir, err)
	}
	if got := s.ListCollections(); len(got) != 0 {
		t.Errorf("ListCollections after delete = %+v, want empty", got)
	}
}

// Deleting a name chromem never loaded is a no-op, not an error. Callers that
// need an unloaded directory gone must remove it themselves.
func TestDeleteCollectionByName_UnknownIsNoOp(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.DeleteCollectionByName("project_deadbeef"); err != nil {
		t.Errorf("delete of unknown collection returned %v, want nil", err)
	}
}

func TestHolder_MaintainerNilSafe(t *testing.T) {
	h := NewHolder(nil)
	if got := h.ListCollections(); got != nil {
		t.Errorf("ListCollections on empty holder = %+v, want nil", got)
	}
	if got := h.CollectionDir("/p"); got != "" {
		t.Errorf("CollectionDir on empty holder = %q, want empty", got)
	}
	if got := h.BaseDir(); got != "" {
		t.Errorf("BaseDir on empty holder = %q, want empty", got)
	}
	if err := h.DeleteCollectionByName("project_x"); err == nil {
		t.Error("DeleteCollectionByName on empty holder returned nil, want an error")
	}

	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h.Swap(s)
	if h.BaseDir() != s.BaseDir() {
		t.Errorf("after Swap, holder BaseDir = %q, want %q", h.BaseDir(), s.BaseDir())
	}
}
