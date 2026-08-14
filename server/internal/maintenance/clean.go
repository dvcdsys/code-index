package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Outcome records one item that was not deleted, and why.
type Outcome struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// CleanCategoryResult is the per-category outcome.
type CleanCategoryResult struct {
	ID             CategoryID `json:"id"`
	DeletedCount   int        `json:"deleted_count"`
	SkippedCount   int        `json:"skipped_count"`
	FailedCount    int        `json:"failed_count"`
	ReclaimedBytes int64      `json:"reclaimed_bytes"`
	Skipped        []Outcome  `json:"skipped,omitempty"`
	Failures       []Outcome  `json:"failures,omitempty"`
}

// CleanResult is what the admin gets back.
type CleanResult struct {
	StartedAt      time.Time             `json:"started_at"`
	FinishedAt     time.Time             `json:"finished_at"`
	DurationMS     int64                 `json:"duration_ms"`
	ReclaimedBytes int64                 `json:"reclaimed_bytes"`
	Categories     []CleanCategoryResult `json:"categories"`
}

// Clean deletes the selected categories from a previously returned analysis.
//
// The analysis id is not the safety mechanism — every item is re-checked
// against live state immediately before it is removed. The realistic hazard is
// mundane: an admin deletes a project, runs Analyze, then re-adds the same
// project and indexes it before pressing the confirm button. Without the
// re-check, Clean would delete the freshly built index because the analysis
// still lists that collection as an orphan.
//
// Partial failure is normal and is reported, not raised: one unreadable
// directory must not stop the other forty from being reclaimed.
func (s *Service) Clean(ctx context.Context, analysisID string, selected []CategoryID) (*CleanResult, error) {
	// Validate the selection BEFORE consuming the analysis, so a typo in the
	// category list does not cost the admin their scan.
	if len(selected) == 0 {
		return nil, ErrNoCategories
	}
	want := map[CategoryID]bool{}
	for _, id := range selected {
		if !validCategory(id) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCategory, id)
		}
		want[id] = true
	}

	// take() consumes the analysis atomically: an id is good for exactly one
	// clean. Whatever happens below, the cached picture is wrong afterwards.
	a, ok := s.cache.take(analysisID)
	if !ok {
		return nil, ErrAnalysisExpired
	}

	st, err := s.readScanState(ctx)
	if err != nil {
		return nil, err
	}

	byID := map[CategoryID]Category{}
	for _, c := range a.Categories {
		byID[c.ID] = c
	}

	started := s.now()
	out := &CleanResult{StartedAt: started}

	// AllCategories order matters: orphan_collections runs first. Abandoned
	// namespaces are removed with a raw os.RemoveAll, and doing that before the
	// store has dropped the collection would pull the database out from under
	// an open handle.
	for _, id := range AllCategories {
		if !want[id] {
			continue
		}
		cat, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %q not present in this analysis", ErrUnknownCategory, id)
		}
		res := s.cleanCategory(ctx, cat, st)
		out.Categories = append(out.Categories, res)
		out.ReclaimedBytes += res.ReclaimedBytes
	}

	// No forced GC here any more. Dropping a collection used to free a large
	// slice of the Go heap — chromem held every document resident — and the
	// clean ended with runtime.GC() + debug.FreeOSMemory() so the reported
	// heap would actually move. The SQLite store holds nothing per collection,
	// so a stop-the-world pass would buy exactly nothing.

	out.FinishedAt = s.now()
	out.DurationMS = out.FinishedAt.Sub(started).Milliseconds()
	return out, nil
}

func (s *Service) cleanCategory(ctx context.Context, cat Category, st *scanState) CleanCategoryResult {
	res := CleanCategoryResult{ID: cat.ID}
	skip := func(key, reason string) {
		res.SkippedCount++
		res.Skipped = append(res.Skipped, Outcome{Key: key, Reason: reason})
	}
	fail := func(key, reason string) {
		res.FailedCount++
		res.Failures = append(res.Failures, Outcome{Key: key, Reason: reason})
	}
	deleted := func(it Item) {
		res.DeletedCount++
		res.ReclaimedBytes += it.SizeBytes
	}

	switch cat.ID {
	case CatOrphanCollections:
		// Re-assert the category-wide guard: a job that started after the
		// analysis could be writing to any collection, and we cannot tell
		// which one.
		if st.activeIndexJobs > 0 {
			for _, it := range cat.all {
				skip(it.Key, "an index or clone job is running")
			}
			return res
		}
		vs := s.d.VectorStore
		if vs == nil {
			for _, it := range cat.all {
				fail(it.Key, "vector store unavailable")
			}
			return res
		}
		for _, it := range cat.all {
			if hostPath, live := st.liveCollections[it.collection]; live {
				skip(it.Key, fmt.Sprintf("project %s was re-created", hostPath))
				continue
			}
			if err := vs.DeleteCollectionByName(it.collection); err != nil {
				fail(it.Key, err.Error())
				continue
			}
			deleted(it)
		}

	case CatOrphanRepos:
		root := s.reposRoot()
		for _, it := range cat.all {
			if root == "" || !isAncestor(root, it.path) {
				fail(it.Key, "path is outside the clone directory")
				continue
			}
			if pathHashRE.MatchString(it.Key) {
				if _, live := st.liveHashes[it.Key]; live {
					skip(it.Key, "a project now uses this checkout")
					continue
				}
			}
			if err := os.RemoveAll(it.path); err != nil {
				fail(it.Key, err.Error())
				continue
			}
			deleted(it)
		}

	case CatStaleNamespaces:
		active := s.activeNamespaceDirs()
		if len(active) == 0 {
			for _, it := range cat.all {
				skip(it.Key, "active embedding namespace is unknown")
			}
			return res
		}
		// Re-derive the containers rather than trusting the analysis: this is
		// an os.RemoveAll, and the only thing standing between it and an
		// arbitrary path is the check that the target lives inside one of the
		// two vector-store trees.
		var bases []string
		for _, b := range s.namespaceBases() {
			bases = append(bases, b.root)
		}
		for _, it := range cat.all {
			p := filepath.Clean(it.path)
			inside := false
			for _, base := range bases {
				if isAncestor(base, p) {
					inside = true
					break
				}
			}
			if !inside {
				fail(it.Key, "path is outside the vector store directory")
				continue
			}
			if isActiveNamespace(p, active) {
				skip(it.Key, "this namespace is now active")
				continue
			}
			if err := os.RemoveAll(p); err != nil {
				fail(it.Key, err.Error())
				continue
			}
			deleted(it)
		}

	case CatLegacyChromem:
		// Re-validate from scratch, exactly as the orphan categories do. The
		// scenario this is here for: the admin analysed, then switched the
		// embedding model, and the store reopened on this same namespace with
		// a fresh import in flight — half the collections are back to
		// unmigrated and the gob files are load-bearing again. Note this check
		// is what stands in for an active-job guard (see scanLegacyChromem):
		// index jobs cannot make legacy data matter, an in-flight import can,
		// and this asks about the import directly.
		dir, _, ok, _ := s.legacyChromemState()
		root := ""
		if s.d.Cfg != nil {
			root = filepath.Clean(s.d.Cfg.ChromaPersistDir)
		}
		for _, it := range cat.all {
			p := filepath.Clean(it.path)
			// This is an os.RemoveAll of a directory holding gigabytes; the
			// containment check is the only thing between it and an arbitrary
			// path, so it is re-derived from config rather than trusted from
			// the analysis.
			if root == "" || !isAncestor(root, p) {
				fail(it.Key, "path is outside the legacy chroma directory")
				continue
			}
			if !ok || p != dir {
				skip(it.Key, "this legacy store is no longer provably imported into the vector database")
				continue
			}
			if _, err := os.Stat(p); os.IsNotExist(err) {
				skip(it.Key, "already gone")
				continue
			}
			if err := os.RemoveAll(p); err != nil {
				fail(it.Key, err.Error())
				continue
			}
			deleted(it)
		}

	case CatStaleJobs:
		if s.d.Jobs == nil || s.d.DB == nil {
			for _, it := range cat.all {
				fail(it.Key, "job queue unavailable")
			}
			return res
		}
		// Recompute the cutoff instead of deleting the ids seen at analysis
		// time: rows written since then are not garbage, and an id list would
		// have to be carried through the client.
		cutoff := s.now().Add(-staleJobRetention)
		n, err := s.d.Jobs.DeleteFinishedBefore(ctx, cutoff)
		if err != nil {
			fail("jobs", err.Error())
			return res
		}
		if n > 0 {
			res.DeletedCount = int(n)
			for _, it := range cat.all {
				res.ReclaimedBytes += it.SizeBytes
			}
		}

	case CatUnusedModels:
		cacheDir := s.activeGGUFCacheDir()
		activeDir := ""
		if repo := s.activeModelRepo(); repo != "" {
			activeDir = strings.ReplaceAll(repo, "/", "__")
		}
		if cacheDir == "" || activeDir == "" {
			for _, it := range cat.all {
				skip(it.Key, "active embedding model is unknown")
			}
			return res
		}
		cutoff := s.now().Add(-ggufSettleWindow)
		for _, it := range cat.all {
			p := filepath.Clean(it.path)
			if !isAncestor(filepath.Clean(cacheDir), p) {
				fail(it.Key, "path is outside the model cache")
				continue
			}
			if isAncestor(filepath.Join(filepath.Clean(cacheDir), activeDir), p) {
				skip(it.Key, "this model is now active")
				continue
			}
			info, err := os.Stat(p)
			if err != nil {
				skip(it.Key, "file is already gone")
				continue
			}
			if info.ModTime().After(cutoff) {
				skip(it.Key, "file changed recently — a download may be in progress")
				continue
			}
			if _, err := os.Stat(p + ".partial"); err == nil {
				skip(it.Key, "a download is in progress")
				continue
			}
			if err := os.Remove(p); err != nil {
				fail(it.Key, err.Error())
				continue
			}
			deleted(it)
		}
	}
	return res
}
