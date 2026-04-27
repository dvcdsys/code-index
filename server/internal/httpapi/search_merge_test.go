package httpapi

import (
	"testing"
)

func mkItem(file string, start, end int, score float32, symbol, kind string) searchResultItem {
	return searchResultItem{
		FilePath:   file,
		StartLine:  start,
		EndLine:    end,
		Score:      score,
		SymbolName: symbol,
		ChunkType:  kind,
		Language:   "go",
	}
}

// TestMerge_NestedSections: H1 (1-200) wraps H2 (27-80), which wraps H3 (29-50).
// All three matched the query — output should be the H1 chunk with
// 2 nested hits, score = max of all three.
func TestMerge_NestedSections(t *testing.T) {
	items := []searchResultItem{
		mkItem("README.md", 1, 200, 0.30, "", "section"),
		mkItem("README.md", 27, 80, 0.45, "", "section"),
		mkItem("README.md", 29, 50, 0.50, "", "section"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 1 {
		t.Fatalf("want 1 merged result, got %d", len(out))
	}
	got := out[0]
	if got.StartLine != 1 || got.EndLine != 200 {
		t.Errorf("expected outer range 1-200, got %d-%d", got.StartLine, got.EndLine)
	}
	if got.Score != 0.50 {
		t.Errorf("merged score = %v, want max=0.50", got.Score)
	}
	if len(got.NestedHits) != 2 {
		t.Fatalf("want 2 nested hits, got %d", len(got.NestedHits))
	}
}

// TestMerge_SameSymbolAdjacent: splitChunk emitted run() as
//   - lines 61-195, function:run
//   - lines 196-198, block, no symbol
// Merge the second into the first.
func TestMerge_SameSymbolAdjacent(t *testing.T) {
	items := []searchResultItem{
		mkItem("main.go", 61, 195, 0.40, "run", "function"),
		mkItem("main.go", 196, 198, 0.30, "", "block"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 1 {
		t.Fatalf("want 1 merged result, got %d", len(out))
	}
	if out[0].StartLine != 61 || out[0].EndLine != 195 {
		// Parent absorbs child; parent's range stays as-is.
		t.Errorf("merged range = %d-%d, want 61-195", out[0].StartLine, out[0].EndLine)
	}
	if out[0].SymbolName != "run" {
		t.Errorf("symbol lost: got %q, want run", out[0].SymbolName)
	}
}

// TestMerge_SiblingsNotMerged: two H2 sections in same file at separate
// non-overlapping ranges → keep both.
func TestMerge_SiblingsNotMerged(t *testing.T) {
	items := []searchResultItem{
		mkItem("doc.md", 10, 30, 0.40, "", "section"),
		mkItem("doc.md", 50, 90, 0.45, "", "section"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 2 {
		t.Fatalf("siblings should stay separate, got %d results", len(out))
	}
}

// TestMerge_DifferentFiles: same line range, different files → not merged.
func TestMerge_DifferentFiles(t *testing.T) {
	items := []searchResultItem{
		mkItem("a.go", 10, 30, 0.40, "fn", "function"),
		mkItem("b.go", 10, 30, 0.45, "fn", "function"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 2 {
		t.Fatalf("cross-file dupes shouldn't merge, got %d", len(out))
	}
}

// TestMerge_ExactDuplicateNotAbsorbed: same range twice (e.g. fan-out
// hiccup) — these should be deduped upstream (dedupByLocation), not by
// merge. We treat them as siblings here.
func TestMerge_ExactDuplicateNotAbsorbed(t *testing.T) {
	items := []searchResultItem{
		mkItem("x.go", 1, 100, 0.30, "Foo", "class"),
		mkItem("x.go", 1, 100, 0.40, "Foo", "class"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 2 {
		t.Errorf("exact duplicates should NOT be merged here (dedup is a separate step), got %d", len(out))
	}
}

// TestMerge_RescoreUsesMax: parent had lower score than child → merged
// result inherits child's higher score.
func TestMerge_RescoreUsesMax(t *testing.T) {
	items := []searchResultItem{
		mkItem("a.go", 1, 100, 0.20, "Outer", "class"),
		mkItem("a.go", 30, 50, 0.80, "inner", "method"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].Score != 0.80 {
		t.Errorf("merged score = %v, want 0.80", out[0].Score)
	}
	if len(out[0].NestedHits) != 1 || out[0].NestedHits[0].SymbolName != "inner" {
		t.Errorf("nested hit missing or wrong: %+v", out[0].NestedHits)
	}
}

// TestMerge_ResortByMergedScore: merged item's max score should bring it
// to the top of the result list.
func TestMerge_ResortByMergedScore(t *testing.T) {
	items := []searchResultItem{
		mkItem("a.go", 1, 100, 0.20, "Outer", "class"), // will absorb 0.80
		mkItem("a.go", 30, 50, 0.80, "inner", "method"),
		mkItem("b.go", 1, 50, 0.50, "other", "function"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 2 {
		t.Fatalf("want 2 results after merge, got %d", len(out))
	}
	if out[0].FilePath != "a.go" {
		t.Errorf("merged a.go (score 0.80) should be first, got %s (score %v)", out[0].FilePath, out[0].Score)
	}
}

// TestMerge_NoOverlapNoMerge: completely disjoint hits stay as-is and
// keep their original score order.
func TestMerge_NoOverlapNoMerge(t *testing.T) {
	items := []searchResultItem{
		mkItem("a.go", 1, 5, 0.50, "fnA", "function"),
		mkItem("b.go", 10, 15, 0.40, "fnB", "function"),
		mkItem("c.go", 20, 25, 0.30, "fnC", "function"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 3 {
		t.Fatalf("disjoint items should stay separate, got %d", len(out))
	}
	if out[0].Score != 0.50 || out[1].Score != 0.40 || out[2].Score != 0.30 {
		t.Errorf("score order broken: %v / %v / %v", out[0].Score, out[1].Score, out[2].Score)
	}
}

// TestMerge_TripleNesting: H1 -> H2 -> H3, all match. After merge, ONE
// result with 2 nested hits (H2 and H3 inside H1).
func TestMerge_TripleNesting(t *testing.T) {
	items := []searchResultItem{
		mkItem("d.md", 1, 100, 0.30, "", "section"),
		mkItem("d.md", 10, 50, 0.40, "", "section"),
		mkItem("d.md", 20, 30, 0.55, "", "section"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 1 {
		t.Fatalf("triple nesting → 1 result, got %d", len(out))
	}
	if len(out[0].NestedHits) != 2 {
		t.Errorf("want 2 nested hits, got %d", len(out[0].NestedHits))
	}
}

// TestMerge_AdjacentNoSymbolNotMerged: two anonymous chunks adjacent in
// the same file are NOT merged — we only merge adjacent chunks if at
// least one carries a symbol (otherwise we have no signal that they're
// related).
func TestMerge_AdjacentNoSymbolNotMerged(t *testing.T) {
	items := []searchResultItem{
		mkItem("x.go", 1, 50, 0.40, "", "module"),
		mkItem("x.go", 51, 100, 0.45, "", "module"),
	}
	out := mergeOverlappingHits(items)
	if len(out) != 2 {
		t.Errorf("anonymous adjacent chunks shouldn't merge, got %d", len(out))
	}
}
