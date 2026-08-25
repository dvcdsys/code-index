package repoindexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fakeEmbedder is a deterministic stub so IndexDir tests don't need a
// running llama-server. Mirrors indexer/indexer_test.go's helper —
// duplicated here because Go test packages can't share unexported types.
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim && j < len(t); j++ {
			v[j] = float32(t[j]) / 255.0
		}
		out[i] = v
	}
	return out, nil
}

func newIndexerForTest(t *testing.T) (*sql.DB, *indexer.Service) {
	return newIndexerForTestEmb(t, &fakeEmbedder{dim: 8})
}

func newIndexerForTestEmb(t *testing.T, emb indexer.Embedder) (*sql.DB, *indexer.Service) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	vs, err := vectorstore.Open(filepath.Join(t.TempDir(), "chroma"))
	if err != nil {
		t.Fatalf("vectorstore: %v", err)
	}
	return d, indexer.New(d, vs, emb, nil)
}

// countingEmbedder is a fakeEmbedder that records how many EmbedTexts calls
// (≈ files embedded) it served, so reconcile tests can prove unchanged files
// were SKIPPED rather than re-embedded.
type countingEmbedder struct {
	dim   int
	calls int
	texts int
}

func (f *countingEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.texts += len(texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim && j < len(t); j++ {
			v[j] = float32(t[j]) / 255.0
		}
		out[i] = v
	}
	return out, nil
}

// busyThenOKEmbedder returns *embeddings.ErrBusy (the queue-saturated
// backpressure signal) for its first `fails` EmbedTexts calls, then succeeds.
// RetryAfter:0 keeps processBatch's backoff at its 1s floor so the test stays
// fast while still exercising a real wait.
type busyThenOKEmbedder struct {
	dim   int
	fails int
	calls int
}

func (f *busyThenOKEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls <= f.fails {
		return nil, &embeddings.ErrBusy{RetryAfter: 0}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for j := 0; j < f.dim && j < len(t); j++ {
			v[j] = float32(t[j]) / 255.0
		}
		out[i] = v
	}
	return out, nil
}

func seedProjectForTest(t *testing.T, d *sql.DB, hostPath string) {
	t.Helper()
	if _, err := d.Exec(`
		INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'pending', ?, ?, ?)`,
		hostPath, hostPath, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", db.HashHostPath(hostPath),
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

// Tests focus on the parts that don't require the embeddings sidecar:
// file filtering, walk pruning, and binary detection. The full pipeline
// (IndexDir) is exercised by the integration test in httpapi that
// stands up a fake indexer service.

func TestBuildPayloadSkipsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.go")
	// 600 KiB — over the 512 KiB default cap.
	if err := os.WriteFile(bigPath, make([]byte, 600*1024), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, err := buildPayload(bigPath, "big.go", DefaultFilter())
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if ok {
		t.Fatalf("expected oversized file to be skipped")
	}
}

func TestBuildPayloadSkipsBinaries(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "x.go")
	// Embed a NUL near the start — flips the binary heuristic.
	content := append([]byte("package x\n"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(binPath, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, err := buildPayload(binPath, "x.go", DefaultFilter())
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if ok {
		t.Fatalf("expected binary-looking content to be skipped")
	}
}

func TestBuildPayloadAcceptsRegular(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	src := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fp, ok, err := buildPayload(p, "main.go", DefaultFilter())
	if err != nil || !ok {
		t.Fatalf("expected ok payload, got ok=%v err=%v", ok, err)
	}
	if fp.Language != "go" {
		t.Fatalf("language detection wrong: %q", fp.Language)
	}
	if fp.Content != src {
		t.Fatalf("content mismatch")
	}
	if fp.ContentHash == "" {
		t.Fatalf("hash empty")
	}
	if fp.Path != "main.go" {
		t.Fatalf("path mismatch: %q", fp.Path)
	}
}

func TestBuildPayloadSkipsUnknownLanguage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "README.unknown_ext_zz")
	if err := os.WriteFile(p, []byte("plain text"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, _ := buildPayload(p, "README.unknown_ext_zz", DefaultFilter())
	if ok {
		t.Fatalf("expected unknown extension to be skipped")
	}
}

func TestShouldSkipDir(t *testing.T) {
	f := DefaultFilter()
	root := "/tmp/root"
	cases := map[string]bool{
		"node_modules": true,
		".git":         true,
		".venv":        true,
		"vendor":       true,
		"src":          false,
		"pkg":          false,
	}
	for name, want := range cases {
		got := f.shouldSkipDir(filepath.Join(root, name), root, name)
		if got != want {
			t.Errorf("shouldSkipDir(%q) = %v, want %v", name, got, want)
		}
	}
	// Root itself must never be skipped, even if its name matches.
	if f.shouldSkipDir(root, root, ".git") {
		t.Fatal("root directory should never be skipped")
	}
}

// TestIndexDir_Full_WalksAllFiles covers the unchanged (pre-incremental)
// behaviour: changes=nil → BeginIndexing(full=true) → WalkDir → all
// files indexed → file_hashes populated for every recognised file.
func TestIndexDir_Full_WalksAllFiles(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	files := map[string]string{
		"a.go":           "package a\n",
		"sub/b.go":       "package b\n",
		"vendor/skip.go": "package skip\n", // DefaultFilter excludes this
		"README.md":      "# hi\n",
	}
	writeTree(t, root, files)

	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(full): %v", err)
	}

	got := fileHashes(t, d, "proj")
	wantIndexed := map[string]bool{"a.go": true, "sub/b.go": true, "README.md": true}
	for path := range wantIndexed {
		if _, ok := got[path]; !ok {
			t.Errorf("file_hashes missing %q after full IndexDir", path)
		}
	}
	if _, ok := got["vendor/skip.go"]; ok {
		t.Error("file_hashes contains vendor/skip.go but DefaultFilter should have excluded it")
	}
}

// TestIndexDir_RetriesEmbeddingQueueSaturation covers the regression where a
// transient "embedding queue saturated" (ErrBusy) failed the whole run instead
// of being treated as the retryable backpressure it is. The embedder reports
// the queue busy on its first call, then succeeds; IndexDir must transparently
// retry the batch and complete, leaving the file indexed.
func TestIndexDir_RetriesEmbeddingQueueSaturation(t *testing.T) {
	emb := &busyThenOKEmbedder{dim: 8, fails: 1}
	d, idx := newIndexerForTestEmb(t, emb)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})

	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir should ride out transient ErrBusy, got: %v", err)
	}
	if emb.calls < 2 {
		t.Fatalf("embedder calls = %d, want >= 2 (initial busy + retry)", emb.calls)
	}
	if _, ok := fileHashes(t, d, "proj")["a.go"]; !ok {
		t.Error("file_hashes missing a.go — batch was not re-processed after backpressure")
	}
}

// TestIndexDir_Incremental_OnlyTouchesChangedPaths covers the new
// incremental branch end-to-end: BeginIndexing(full=false) keeps the
// existing index intact; ProcessFiles only runs on Modified+Added
// paths; FinishIndexing(deleted) drops file_hashes rows for the deleted
// set. Verifies all three at the file_hashes level — the indexer's
// per-file-replace semantics + FinishIndexing's deletion handling are
// already covered by indexer/indexer_test.go, so we trust those and
// only check the orchestration side.
func TestIndexDir_Incremental_OnlyTouchesChangedPaths(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	// Round 1: full index of three files, capture initial hashes.
	writeTree(t, root, map[string]string{
		"keep.go":   "package keep\n",
		"modify.go": "package m\n// v1\n",
		"delete.go": "package del\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(full) round1: %v", err)
	}
	round1 := fileHashes(t, d, "proj")
	if _, ok := round1["keep.go"]; !ok {
		t.Fatalf("round1: keep.go missing from file_hashes (got %v)", round1)
	}

	// Round 2: on disk, "modify.go" gets new content, "new.go" appears,
	// "delete.go" is gone. Then call IndexDir with the explicit change
	// list — keep.go must NOT be re-processed (its content_hash should
	// remain identical to round1's).
	if err := os.Remove(filepath.Join(root, "delete.go")); err != nil {
		t.Fatalf("remove delete.go: %v", err)
	}
	writeTree(t, root, map[string]string{
		"modify.go": "package m\n// v2 — touched\n",
		"new.go":    "package newpkg\n",
	})
	cs := &repocloner.ChangeSet{
		Modified: []string{"modify.go"},
		Added:    []string{"new.go"},
		Deleted:  []string{"delete.go"},
	}
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, cs, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(incremental): %v", err)
	}

	round2 := fileHashes(t, d, "proj")

	// keep.go: untouched — same content_hash as round1.
	if round2["keep.go"] != round1["keep.go"] {
		t.Errorf("keep.go content_hash changed (incremental should have left it alone): round1=%s round2=%s",
			round1["keep.go"], round2["keep.go"])
	}
	// modify.go: changed — new hash.
	if round2["modify.go"] == round1["modify.go"] {
		t.Errorf("modify.go hash unchanged after content edit: %s", round2["modify.go"])
	}
	// new.go: present.
	if _, ok := round2["new.go"]; !ok {
		t.Errorf("new.go missing from file_hashes after incremental Add")
	}
	// delete.go: cleaned out by FinishIndexing(deletedPaths).
	if _, ok := round2["delete.go"]; ok {
		t.Errorf("delete.go still in file_hashes after incremental Delete")
	}
}

// TestIndexDir_Incremental_EmptyChangeSet covers an empty change set
// (push that touched only filtered-out files, or a manual reindex that
// raced with an upstream that hasn't moved): BeginIndexing(false) +
// FinishIndexing must still run cleanly so the project's status is
// updated. No error, no row mutations.
func TestIndexDir_Incremental_EmptyChangeSet(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round 1 full: %v", err)
	}
	before := fileHashes(t, d, "proj")

	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, &repocloner.ChangeSet{}, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(empty changeset): %v", err)
	}
	after := fileHashes(t, d, "proj")

	if len(after) != len(before) {
		t.Errorf("file_hashes count changed after empty changeset: before=%d after=%d", len(before), len(after))
	}
	for path, hash := range before {
		if after[path] != hash {
			t.Errorf("content_hash for %q drifted after empty changeset: %s → %s", path, hash, after[path])
		}
	}
}

// TestIndexDir_Incremental_FilteredOutPaths covers tree.Diff returning
// paths that DefaultFilter rejects (e.g. files under vendor/, or files
// > 512 KiB). IndexDir must silently skip them — no Process call for
// the filtered paths, no error to the caller.
func TestIndexDir_Incremental_FilteredOutPaths(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "package a\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round1 full: %v", err)
	}

	// Write a file under vendor/ — included in ChangeSet but
	// DefaultFilter excludes the directory, so buildPayload still has
	// to read it (the filter currently runs at the file path, not the
	// directory, when ChangeSet drives the iteration). Verify nothing
	// blows up and file_hashes doesn't gain a vendor entry.
	writeTree(t, root, map[string]string{
		"vendor/dep.go": "package dep\n",
	})
	cs := &repocloner.ChangeSet{Added: []string{"vendor/dep.go"}}
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, cs, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(vendor change): %v", err)
	}

	hashes := fileHashes(t, d, "proj")
	if _, ok := hashes["vendor/dep.go"]; ok {
		t.Error("vendor/dep.go ended up in file_hashes despite DefaultFilter; vendor handling regressed")
	}
}

// TestIndexDir_Reconcile_ResumesAndSkipsUnchanged covers the resume path:
// wipe=false + changes=nil walks the whole tree but must skip files whose
// on-disk hash already matches file_hashes, re-embed changed/new files, and
// delete files gone from disk — all WITHOUT a changeset. The countingEmbedder
// proves the skip is real (unchanged files are not re-embedded), which is the
// whole point: a crashed/aborted index resumes for the cost of the remainder,
// not the whole repo.
func TestIndexDir_Reconcile_ResumesAndSkipsUnchanged(t *testing.T) {
	emb := &countingEmbedder{dim: 8}
	d, idx := newIndexerForTestEmb(t, emb)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n// v1\n",
		"c.go": "package c\n",
	})

	// Round 1: reconcile on a fresh project embeds every file.
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(reconcile) round1: %v", err)
	}
	round1 := fileHashes(t, d, "proj")
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		if _, ok := round1[p]; !ok {
			t.Fatalf("round1: %q missing from file_hashes", p)
		}
	}
	if emb.calls == 0 {
		t.Fatalf("round1 embedded nothing")
	}

	// Mutate on disk: b.go changes, d.go appears, c.go removed.
	if err := os.Remove(filepath.Join(root, "c.go")); err != nil {
		t.Fatalf("rm c.go: %v", err)
	}
	writeTree(t, root, map[string]string{
		"b.go": "package b\n// v2 changed\n",
		"d.go": "package d\n",
	})

	emb.calls = 0 // count only round-2 embeds

	// Round 2: reconcile with NO changeset (nil).
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(reconcile) round2: %v", err)
	}
	round2 := fileHashes(t, d, "proj")

	// a.go: untouched (resume skip) — same hash.
	if round2["a.go"] != round1["a.go"] {
		t.Errorf("a.go hash changed; reconcile should have skipped it")
	}
	// b.go: changed — new hash.
	if round2["b.go"] == round1["b.go"] {
		t.Errorf("b.go hash unchanged after edit; reconcile should have re-embedded it")
	}
	// d.go: new file indexed.
	if _, ok := round2["d.go"]; !ok {
		t.Errorf("d.go missing; reconcile should have indexed the new file")
	}
	// c.go: deleted by reconcile's delete pass — even with a nil changeset.
	if _, ok := round2["c.go"]; ok {
		t.Errorf("c.go still present; reconcile should have deleted the vanished file")
	}
	// The crux: round 2 re-embedded ONLY b.go (changed) + d.go (new) = 2
	// files. a.go was skipped. A full wipe would have re-embedded all 3
	// surviving files — that's the expensive behaviour this fix removes.
	if emb.calls != 2 {
		t.Errorf("round2 embedded %d files; want 2 (b.go changed + d.go new; a.go skipped)", emb.calls)
	}
}

// writeTree writes a path→content map into root, creating parent dirs
// as needed. Existing files are overwritten — convenient for "round 2"
// scenarios where some files change between IndexDir calls.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// fileHashes reads the current state of file_hashes for a project.
// Path-keyed map of content_hash so tests can assert presence/absence
// AND value changes (drift detection between rounds).
func fileHashes(t *testing.T, d *sql.DB, projectPath string) map[string]string {
	t.Helper()
	rows, err := d.Query(`SELECT file_path, content_hash FROM file_hashes WHERE project_path = ?`, projectPath)
	if err != nil {
		t.Fatalf("query file_hashes: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[p] = h
	}
	return out
}

// chunkCount reports how many indexed chunks a project holds for one file.
// file_hashes alone would not prove the prune reached the searchable stores;
// chunks_meta is the row-level shadow FinishIndexing has to clear alongside
// the vectors and symbols.
func chunkCount(t *testing.T, d *sql.DB, projectPath, filePath string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM chunks_meta WHERE project_path = ? AND file_path = ?`,
		projectPath, filePath,
	).Scan(&n); err != nil {
		t.Fatalf("count chunks_meta for %s: %v", filePath, err)
	}
	return n
}

// TestIndexDir_Reconcile_PrunesNewlyIgnored is the acceptance test for
// issue #274: a .cixignore committed to a repo that is ALREADY indexed must
// remove the files it now covers, not merely stop adding new ones.
func TestIndexDir_Reconcile_PrunesNewlyIgnored(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"keep.go":  "package keep\n\nfunc Keep() {}\n",
		"gen/a.go": "package gen\n\nfunc GenA() {}\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round1 reconcile: %v", err)
	}
	round1 := fileHashes(t, d, "proj")
	if _, ok := round1["gen/a.go"]; !ok {
		t.Fatal("round1: gen/a.go should have been indexed before the rule existed")
	}
	if chunkCount(t, d, "proj", "gen/a.go") == 0 {
		t.Fatal("round1: gen/a.go produced no chunks, test cannot prove the prune")
	}

	// The user commits a .cixignore covering gen/ and pushes.
	writeTree(t, root, map[string]string{".cixignore": "gen/\n"})

	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round2 reconcile: %v", err)
	}
	round2 := fileHashes(t, d, "proj")

	if _, ok := round2["gen/a.go"]; ok {
		t.Error("gen/a.go still in file_hashes; the new .cixignore rule was not applied retroactively")
	}
	if n := chunkCount(t, d, "proj", "gen/a.go"); n != 0 {
		t.Errorf("gen/a.go still has %d chunks; FinishIndexing did not prune the searchable rows", n)
	}
	// Everything outside the rule must survive untouched — a prune that
	// re-embeds the rest would be correct but ruinously expensive.
	if round2["keep.go"] != round1["keep.go"] {
		t.Error("keep.go hash changed; the prune should not have disturbed it")
	}
}

func TestIndexDir_Full_SkipsIgnored(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":    "*.gen.go\n",
		".cixignore":    "fixtures/\n",
		"keep.go":       "package keep\n",
		"api.gen.go":    "package api\n",
		"fixtures/f.go": "package fixtures\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(full): %v", err)
	}

	hashes := fileHashes(t, d, "proj")
	if _, ok := hashes["keep.go"]; !ok {
		t.Error("keep.go missing; the ignore rules over-matched")
	}
	for _, p := range []string{"api.gen.go", "fixtures/f.go"} {
		if _, ok := hashes[p]; ok {
			t.Errorf("%s was indexed despite the ignore rules", p)
		}
	}
	// The ignore files themselves have no detectable language, so they never
	// reach the index — no explicit skip needed for them.
	for _, p := range []string{".gitignore", ".cixignore"} {
		if _, ok := hashes[p]; ok {
			t.Errorf("%s should not be indexed", p)
		}
	}
}

// The incremental driver never walks directories, so a path that a new rule
// covers has to be deleted explicitly — otherwise its chunks survive until
// some unrelated reconcile happens to run.
func TestIndexDir_Incremental_DeletesNewlyIgnored(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"keep.go":  "package keep\n\nfunc Keep() {}\n",
		"gen/a.go": "package gen\n\nfunc GenA() {}\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round1 full: %v", err)
	}
	if chunkCount(t, d, "proj", "gen/a.go") == 0 {
		t.Fatal("round1: gen/a.go produced no chunks")
	}

	// A push that edits gen/a.go and adds the rule that now covers it.
	writeTree(t, root, map[string]string{
		".cixignore": "gen/\n",
		"gen/a.go":   "package gen\n\nfunc GenA() { _ = 1 }\n",
	})
	cs := &repocloner.ChangeSet{Modified: []string{"gen/a.go"}, Added: []string{".cixignore"}}
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, cs, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(incremental): %v", err)
	}

	hashes := fileHashes(t, d, "proj")
	if _, ok := hashes["gen/a.go"]; ok {
		t.Error("gen/a.go still in file_hashes; incremental skipped it instead of deleting it")
	}
	if n := chunkCount(t, d, "proj", "gen/a.go"); n != 0 {
		t.Errorf("gen/a.go still has %d chunks after the incremental pass", n)
	}
	if _, ok := hashes["keep.go"]; !ok {
		t.Error("keep.go disappeared; the incremental prune over-reached")
	}
}

// Deleting a path that was never indexed must be a no-op, not an error — the
// common case when a rule is added at the same time as the files it covers.
func TestIndexDir_Incremental_IgnoredNewPathIsHarmless(t *testing.T) {
	d, idx := newIndexerForTest(t)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{"keep.go": "package keep\n"})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, true, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round1 full: %v", err)
	}

	writeTree(t, root, map[string]string{
		".cixignore": "gen/\n",
		"gen/new.go": "package gen\n",
	})
	cs := &repocloner.ChangeSet{Added: []string{".cixignore", "gen/new.go"}}
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, cs, DefaultFilter(), nil); err != nil {
		t.Fatalf("IndexDir(incremental): %v", err)
	}

	hashes := fileHashes(t, d, "proj")
	if _, ok := hashes["gen/new.go"]; ok {
		t.Error("gen/new.go was indexed despite the rule added in the same push")
	}
	if _, ok := hashes["keep.go"]; !ok {
		t.Error("keep.go disappeared")
	}
}

// Ignore rules must not disturb the resume skip: a reconcile after adding a
// rule re-embeds nothing that the rule does not cover.
func TestIndexDir_Reconcile_IgnoreDoesNotForceReembed(t *testing.T) {
	emb := &countingEmbedder{dim: 8}
	d, idx := newIndexerForTestEmb(t, emb)
	seedProjectForTest(t, d, "proj")

	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go":     "package a\n",
		"b.go":     "package b\n",
		"gen/c.go": "package gen\n",
	})
	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round1 reconcile: %v", err)
	}

	writeTree(t, root, map[string]string{".cixignore": "gen/\n"})
	emb.calls = 0

	if _, _, err := IndexDir(context.Background(), idx, "proj", root, false, nil, DefaultFilter(), nil); err != nil {
		t.Fatalf("round2 reconcile: %v", err)
	}
	if emb.calls != 0 {
		t.Errorf("round2 embedded %d files; adding an ignore rule must only delete, never re-embed", emb.calls)
	}
	if _, ok := fileHashes(t, d, "proj")["gen/c.go"]; ok {
		t.Error("gen/c.go survived the reconcile")
	}
}

// shouldSkipDir has to consult the ignore rules too, and it has to ask about
// a DIRECTORY: go-git enforces a "build/" pattern's dirOnly flag only on the
// last path segment, so asking as a file would silently fail to prune.
func TestShouldSkipDir_IgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".cixignore": "fixtures/\nsrc/gen/\n"})
	f := filterFor(t, root)

	cases := map[string]bool{
		"fixtures": true,
		"src":      false,
		"pkg":      false,
	}
	for name, want := range cases {
		if got := f.shouldSkipDir(filepath.Join(root, name), root, name); got != want {
			t.Errorf("shouldSkipDir(%q) = %v, want %v", name, got, want)
		}
	}
	// A nested pattern must prune only at its own depth.
	nested := filepath.Join(root, "src", "gen")
	if !f.shouldSkipDir(nested, root, "gen") {
		t.Error("src/gen should be pruned by the src/gen/ rule")
	}
	if f.shouldSkipDir(filepath.Join(root, "other", "gen"), root, "gen") {
		t.Error("other/gen must not be pruned by an src/-anchored rule")
	}
	// The hardcoded exclude list still applies alongside the rules.
	if !f.shouldSkipDir(filepath.Join(root, "node_modules"), root, "node_modules") {
		t.Error("DefaultFilter exclusions regressed")
	}
}
