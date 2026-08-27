package httpapi

import (
	"github.com/dvcdsys/code-index/server/internal/searchstats"
)

// recordSearch notes one completed search against one project.
//
// Only SUCCESSFUL searches are recorded, and every call site sits after the
// response has been decided. A request that was refused for access, rejected
// for a malformed body, or failed to embed did not search the project, and
// counting it would make "how much is this project searched" a measure of
// client bugs.
//
// A search that legitimately returned nothing IS recorded, with no files. That
// is the point of separating the query count from the file counts: a project
// with many queries and few file hits is one whose index is not answering, and
// folding empty results away would hide exactly that.
//
// files is DEDUPLICATED here, so a file counts once per search however many
// chunks of it matched. This is what keeps the numbers legible next to each
// other: hits for any one file can never exceed the project's query count, so
// a row reads as "this file came back in 42 of the project's 128 searches".
// Counting each matching chunk instead would let a file's hits exceed the
// number of searches, and the column would stop having an interpretation.
// Semantic search already groups by file, so there it is a no-op; the symbol,
// definition and reference searches return one row per match and genuinely
// repeat paths.
func (s *Server) recordSearch(projectPath, kind string, files []string) {
	if projectPath == "" {
		return
	}
	// One atomic load. Recorder's methods are nil-safe, so a search that races
	// an administrator switching the feature off is a no-op rather than a
	// branch here and a panic there.
	s.Deps.SearchStats.Recorder().Record(projectPath, kind, dedupePaths(files))
}

// linearDedupeMax is the length below which deduplication scans instead of
// hashing.
//
// A map is the reflex here and it is the wrong one at this size. Result sets are
// bounded by the caller's limit — ten by default, capped well under a hundred —
// and building a map for twenty strings allocates about 1.5 kB that is discarded
// microseconds later, on the critical path of every search. Measured on the
// file-search handler, dropping the map for the common case removed roughly 2 kB
// and 8 allocations per request. Above the threshold the quadratic scan starts to
// matter and the map earns its keep.
const linearDedupeMax = 32

// dedupePaths keeps the first occurrence of each non-empty path.
//
// Order is preserved. The recorder counts each path once either way, but a
// stable order keeps the tests readable and the top-files list deterministic
// when hit counts tie.
//
// The input is NOT modified: every caller builds its slice immediately before
// calling, so writing in place would work today and would be a trap for the next
// one to add a call site.
func dedupePaths(files []string) []string {
	if len(files) == 0 {
		return files
	}
	if len(files) == 1 {
		// Not just `return files`: a lone empty path would survive a shortcut
		// that skips the loop, and the recorder would count a file that is not
		// one. The single-element case is the only one where the fast path and
		// the loop could disagree, so it is the only one worth spelling out.
		if files[0] == "" {
			return nil
		}
		return files
	}
	out := make([]string, 0, len(files))
	if len(files) <= linearDedupeMax {
		for _, f := range files {
			if f == "" {
				continue
			}
			dup := false
			for _, seen := range out {
				if seen == f {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, f)
			}
		}
		return out
	}
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// Kind constants re-exported so handler call sites read as one thing.
const (
	searchKindSemantic    = searchstats.KindSemantic
	searchKindSymbols     = searchstats.KindSymbols
	searchKindDefinitions = searchstats.KindDefinitions
	searchKindReferences  = searchstats.KindReferences
	searchKindFiles       = searchstats.KindFiles
	searchKindWorkspace   = searchstats.KindWorkspace
)

// resultFilePaths lifts the file path out of each item in a result slice.
//
// Generic because the five per-project search endpoints return five unrelated
// generated types that happen to share a FilePath field, and Go has no way to
// say that. The alternative is the same four-line loop written five times.
func resultFilePaths[T any](items []T, path func(T) string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, path(it))
	}
	return out
}

// recordWorkspaceSearch attributes one workspace fan-out to every project it
// touched.
//
// Two different populations are involved and conflating them would misreport
// both:
//
//   - The QUERY is recorded against every project in `searched` — every
//     project whose vector collection was scanned and whose BM25 partition was
//     queried. That work was paid for whether or not the project made the
//     answer, and "how much is this project searched" has to include it, or a
//     repo that carries a busy workspace's fan-out looks idle.
//   - FILE hits come only from `surviving`, the projects that cleared the
//     relevance threshold. A project that was scanned and returned nothing
//     records a query with no files, which is precisely the signal that its
//     index has stopped answering.
//
// `surviving` and not the display panel, deliberately. The panel is truncated
// to the caller's top_projects parameter, and letting a request parameter
// decide what gets counted would make the stored numbers a measurement of how
// the caller configured their request. The same distinction is already
// load-bearing for projects_returned in WorkspaceSearch, where the comments
// record that it has been broken once and re-broken during a refactor.
func (s *Server) recordWorkspaceSearch(searched []string, surviving []projectHits) {
	if len(searched) == 0 || s.Deps.SearchStats.Recorder() == nil {
		return
	}
	byProject := make(map[string][]string, len(surviving))
	for _, ph := range surviving {
		paths := make([]string, 0, len(ph.FusedChunks))
		for _, c := range ph.FusedChunks {
			paths = append(paths, c.FilePath)
		}
		byProject[ph.ProjectPath] = paths
	}
	for _, projectPath := range searched {
		s.recordSearch(projectPath, searchKindWorkspace, byProject[projectPath])
	}
}
