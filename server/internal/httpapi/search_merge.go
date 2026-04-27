package httpapi

import "sort"

// mergeOverlappingHits collapses search results that come from the same file
// when one's line range fully contains another's, or when same-symbol pieces
// of a split chunk happen to be adjacent. The "outer" hit survives, picks up
// the best score across the merged set, and records inner hits as
// NestedHits so the renderer can show them as breadcrumbs.
//
// Why this matters
//
// Tree-sitter emits nested chunks by design: a class chunk wraps its method
// chunks; a markdown H1 section wraps its H2 sub-sections; a Python class
// wraps inner functions. Without merging, a vector-search query that hits
// strongly inside one of those nested chunks tends to also hit (slightly
// less strongly) the parent chunk that textually contains the same lines,
// and the user's --limit budget gets eaten by N copies of essentially the
// same code region.
//
// Adjacency rule (the splitChunk leftover): when a function is too long for
// a single chunk, splitChunk emits piece 1 with the symbol metadata and
// pieces 2..N as anonymous `block`s. If a query happens to hit BOTH the
// named first piece AND the anonymous tail, those two ranges are exactly
// adjacent (piece1.EndLine + 1 == piece2.StartLine). We merge those too —
// the anonymous tail "belongs" to the named symbol on the same file.
//
// Cross-file results are NEVER merged: two functions with the same name in
// two different files are legitimately separate hits.
//
// The function does not truncate to any limit — that's the caller's job
// after this returns. Output is sorted by descending merged score.
func mergeOverlappingHits(items []searchResultItem) []searchResultItem {
	if len(items) <= 1 {
		return items
	}

	// Group indices by file path. Keeping indices (not copies) so we can
	// edit items[parentIdx] in-place to grow its NestedHits.
	byFile := map[string][]int{}
	for i := range items {
		byFile[items[i].FilePath] = append(byFile[items[i].FilePath], i)
	}

	consumed := make([]bool, len(items))

	for _, idxs := range byFile {
		if len(idxs) <= 1 {
			continue
		}

		// Sort by range size descending (largest first → potential parent),
		// tiebreak by start line ascending so the iteration order is stable
		// and biggest-encloses-everything-inside-it semantics fall out
		// naturally.
		sort.Slice(idxs, func(a, b int) bool {
			ia, ib := items[idxs[a]], items[idxs[b]]
			sa := ia.EndLine - ia.StartLine
			sb := ib.EndLine - ib.StartLine
			if sa != sb {
				return sa > sb
			}
			return ia.StartLine < ib.StartLine
		})

		for ai := 0; ai < len(idxs); ai++ {
			parentIdx := idxs[ai]
			if consumed[parentIdx] {
				continue
			}
			parent := items[parentIdx]

			for _, childIdx := range idxs[ai+1:] {
				if consumed[childIdx] {
					continue
				}
				child := items[childIdx]
				if !shouldMerge(parent, child) {
					continue
				}
				consumed[childIdx] = true
				if child.Score > parent.Score {
					parent.Score = child.Score
				}
				parent.NestedHits = append(parent.NestedHits, nestedHit{
					StartLine:  child.StartLine,
					EndLine:    child.EndLine,
					SymbolName: child.SymbolName,
					ChunkType:  child.ChunkType,
					Score:      child.Score,
				})
			}
			items[parentIdx] = parent
		}
	}

	out := make([]searchResultItem, 0, len(items))
	for i := range items {
		if !consumed[i] {
			out = append(out, items[i])
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// shouldMerge returns true when child should be absorbed into parent.
// Two cases trigger a merge — see mergeOverlappingHits doc-comment for
// the rationale.
func shouldMerge(parent, child searchResultItem) bool {
	if parent.FilePath != child.FilePath {
		return false
	}
	// Case 1: parent's range strictly contains child's range. We require a
	// strict containment (i.e. it's NOT the same range) to avoid merging
	// duplicates from per-language fan-out — those should be deduped at the
	// vector-store layer, not here.
	if parent.StartLine <= child.StartLine && parent.EndLine >= child.EndLine {
		if parent.StartLine != child.StartLine || parent.EndLine != child.EndLine {
			return true
		}
	}
	// Case 2: same-symbol adjacent ranges. After splitChunk, only the
	// first piece keeps SymbolName, so the typical pattern is
	// {symbol=run, lines 61..195} + {symbol="" tail block, lines 196..198}.
	// Adjacency by itself isn't enough — we need at least one to carry the
	// symbol so we know they're related; otherwise we'd merge unrelated
	// neighbouring chunks.
	if parent.SymbolName != "" || child.SymbolName != "" {
		if parent.EndLine+1 == child.StartLine || child.EndLine+1 == parent.StartLine {
			return true
		}
	}
	return false
}
