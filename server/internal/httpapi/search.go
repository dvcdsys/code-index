package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// ---------------------------------------------------------------------------
// vectorStoreResult / dedupe — used by SemanticSearch fan-out (server.go).
// ---------------------------------------------------------------------------

// vectorStoreResult wraps a vectorstore.SearchResult so fan-out can dedupe by
// (file_path, start_line, end_line) across multiple language-scoped queries.
type vectorStoreResult struct {
	r vectorstore.SearchResult
}

func wrapResults(rs []vectorstore.SearchResult) []vectorStoreResult {
	out := make([]vectorStoreResult, len(rs))
	for i := range rs {
		out[i] = vectorStoreResult{r: rs[i]}
	}
	return out
}

// dedupByLocation keeps the highest-scoring result per (file_path, start, end).
// Preserves the relative order of the first-seen instances.
func dedupByLocation(rs []vectorStoreResult) []vectorStoreResult {
	type key struct {
		fp    string
		start int
		end   int
	}
	seen := make(map[key]int, len(rs))
	out := rs[:0]
	for _, w := range rs {
		k := key{w.r.FilePath, w.r.StartLine, w.r.EndLine}
		if idx, ok := seen[k]; ok {
			if w.r.Score > out[idx].r.Score {
				out[idx] = w
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, w)
	}
	return out
}

// ---------------------------------------------------------------------------
// Wire-format types kept as test fixtures.
//
// Server.* methods in server.go emit the openapi.* equivalents; these inline
// types mirror the same JSON tags so existing *_test.go can still
// `json.Unmarshal` into them. Do not add new fields here — extend the
// OpenAPI spec instead.
// ---------------------------------------------------------------------------

type symbolSearchRequest struct {
	Query string   `json:"query"`
	Kinds []string `json:"kinds"`
	Limit int      `json:"limit"`
}

type symbolResultItem struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	Language   string  `json:"language"`
	Signature  *string `json:"signature,omitempty"`
	ParentName *string `json:"parent_name,omitempty"`
}

type symbolSearchResponse struct {
	Results []symbolResultItem `json:"results"`
	Total   int                `json:"total"`
}

type fileSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type fileResultItem struct {
	FilePath string  `json:"file_path"`
	Language *string `json:"language"`
}

type fileSearchResponse struct {
	Results []fileResultItem `json:"results"`
	Total   int              `json:"total"`
}

type definitionRequest struct {
	Symbol   string `json:"symbol"`
	Kind     string `json:"kind"`
	FilePath string `json:"file_path"`
	Limit    int    `json:"limit"`
}

type definitionItem struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	Language   string  `json:"language"`
	Signature  *string `json:"signature,omitempty"`
	ParentName *string `json:"parent_name,omitempty"`
}

type definitionResponse struct {
	Results []definitionItem `json:"results"`
	Total   int              `json:"total"`
}

type referenceRequest struct {
	Symbol   string `json:"symbol"`
	Limit    int    `json:"limit"`
	FilePath string `json:"file_path"`
}

type referenceItem struct {
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Content    string `json:"content"`
	ChunkType  string `json:"chunk_type"`
	SymbolName string `json:"symbol_name"`
	Language   string `json:"language"`
}

type referenceResponse struct {
	Results []referenceItem `json:"results"`
	Total   int             `json:"total"`
}

type dirEntry struct {
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

type symbolEntry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	FilePath string `json:"file_path"`
	Language string `json:"language"`
}

type projectSummaryResponse struct {
	HostPath       string        `json:"host_path"`
	Status         string        `json:"status"`
	Languages      []string      `json:"languages"`
	TotalFiles     int           `json:"total_files"`
	TotalChunks    int           `json:"total_chunks"`
	TotalSymbols   int           `json:"total_symbols"`
	TopDirectories []dirEntry    `json:"top_directories"`
	RecentSymbols  []symbolEntry `json:"recent_symbols"`
}

// Suppress "unused" warnings — the request shapes are populated by
// json.Unmarshal in tests, which static analysis cannot see.
var (
	_ = symbolSearchRequest{}
	_ = fileSearchRequest{}
	_ = definitionRequest{}
	_ = referenceRequest{}
)

// ---------------------------------------------------------------------------
// resolveProjectFromHash looks up the project by URL path hash.
// Returns the project or writes a 404 and returns nil. Shared by Server
// methods (server.go) — kept here because the chi.URLParam dependency
// belongs with the search/path-handling code.
// ---------------------------------------------------------------------------

func resolveProjectFromHash(w http.ResponseWriter, r *http.Request, d Deps) *projects.Project {
	pathHash := chi.URLParam(r, "path")
	p, err := projects.GetByHash(r.Context(), d.DB, pathHash)
	if err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return nil
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	return p
}

// ---------------------------------------------------------------------------
// Path helpers — mirror Python Path(fp).parts logic. Used by
// Server.GetProjectSummary to bucket file paths into top-level directories.
// ---------------------------------------------------------------------------

func splitPath(fp string) []string {
	var parts []string
	for {
		dir, base := filepath.Split(fp)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		if dir == "" || dir == fp {
			if dir != "" && dir != "/" {
				parts = append([]string{dir}, parts...)
			}
			break
		}
		fp = filepath.Clean(dir)
	}
	return parts
}

func joinPath(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result = filepath.Join(result, p)
	}
	return result
}

// Ensure symbolindex and sql are referenced (compile-time guard against
// accidental import removal during refactors).
var (
	_ = (*sql.DB)(nil)
	_ = symbolindex.Symbol{}
)

// ---------------------------------------------------------------------------
// Internal types used by the semantic-search fan-out / merge / group-by-file
// pipeline. The public wire format lives in openapi.SemanticSearchResponse;
// these types describe the intermediate state inside Server.SemanticSearch.
// ---------------------------------------------------------------------------

// searchResultItem is the per-chunk match used INTERNALLY during retrieval.
// It is not exposed in the JSON response — the wire format groups matches
// by file (see fileGroupResult). The merge step (mergeOverlappingHits)
// works on this struct, then groupByFile lifts the survivors into
// file-grouped results.
type searchResultItem struct {
	FilePath   string      `json:"-"`
	StartLine  int         `json:"start_line"`
	EndLine    int         `json:"end_line"`
	Content    string      `json:"content"`
	Score      float32     `json:"score"`
	ChunkType  string      `json:"chunk_type"`
	SymbolName string      `json:"symbol_name,omitempty"`
	Language   string      `json:"-"`
	NestedHits []nestedHit `json:"nested_hits,omitempty"`
}

// nestedHit records a chunk that was merged INTO another result by
// mergeOverlappingHits (e.g. an H2 section absorbed into its containing H1).
type nestedHit struct {
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	SymbolName string  `json:"symbol_name,omitempty"`
	ChunkType  string  `json:"chunk_type"`
	Score      float32 `json:"score"`
}

// fileMatch is one search hit inside a file group.
type fileMatch struct {
	StartLine  int         `json:"start_line"`
	EndLine    int         `json:"end_line"`
	Content    string      `json:"content"`
	Score      float32     `json:"score"`
	ChunkType  string      `json:"chunk_type"`
	SymbolName string      `json:"symbol_name,omitempty"`
	NestedHits []nestedHit `json:"nested_hits,omitempty"`
}

// fileGroupResult is the top-level unit of search output: one file with
// every match inside it that passed min_score.
type fileGroupResult struct {
	FilePath  string      `json:"file_path"`
	Language  string      `json:"language,omitempty"`
	BestScore float32     `json:"best_score"`
	Matches   []fileMatch `json:"matches"`
}

// searchResponse is the JSON-unmarshal target used by tests. Server.SemanticSearch
// emits openapi.SemanticSearchResponse, which is byte-identical on the wire.
type searchResponse struct {
	Results     []fileGroupResult `json:"results"`
	Total       int               `json:"total"`
	QueryTimeMS float64           `json:"query_time_ms"`
}

// ---------------------------------------------------------------------------
// Constants + helpers shared with server.go (Server.SemanticSearch).
// ---------------------------------------------------------------------------

// maxFanoutSearch is the language-count threshold above which we drop
// per-language pre-filter and fall back to a single over-fetched query
// with post-filter.
const maxFanoutSearch = 4

// maxFactorSearch caps the windowed retrieval expansion. With limit=10
// and factor=16 we top out at 160 raw results.
const maxFactorSearch = 16

// groupByFile lifts merged per-chunk results into per-file groups, sorted by
// best score descending (with a stable tie-break on file_path).
func groupByFile(items []searchResultItem) []fileGroupResult {
	if len(items) == 0 {
		return nil
	}
	indexByPath := map[string]int{}
	var groups []fileGroupResult
	for _, it := range items {
		idx, ok := indexByPath[it.FilePath]
		if !ok {
			groups = append(groups, fileGroupResult{
				FilePath:  it.FilePath,
				Language:  it.Language,
				BestScore: it.Score,
			})
			idx = len(groups) - 1
			indexByPath[it.FilePath] = idx
		}
		g := &groups[idx]
		if it.Score > g.BestScore {
			g.BestScore = it.Score
		}
		g.Matches = append(g.Matches, fileMatch{
			StartLine:  it.StartLine,
			EndLine:    it.EndLine,
			Content:    it.Content,
			Score:      it.Score,
			ChunkType:  it.ChunkType,
			SymbolName: it.SymbolName,
			NestedHits: it.NestedHits,
		})
	}
	for i := range groups {
		ms := groups[i].Matches
		sort.SliceStable(ms, func(a, b int) bool {
			return ms[a].StartLine < ms[b].StartLine
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].BestScore != groups[j].BestScore {
			return groups[i].BestScore > groups[j].BestScore
		}
		return groups[i].FilePath < groups[j].FilePath
	})
	return groups
}

// fetchVectorResults performs the per-language fan-out vector-store query
// at the given limit and returns deduped, score-sorted results.
//
// The fan-out strategy: 0 languages → single query; 1 language → single
// query with where-filter; 2..maxFanout → N queries with per-language
// where-filter, deduped and re-sorted by score; >maxFanout → single
// oversized query, post-filter handled by caller (filterToSearchItems with
// applyPostLangFilter=true).
func fetchVectorResults(
	ctx context.Context,
	store *vectorstore.Store,
	projectPath string,
	qEmb []float32,
	n int,
	languages []string,
) ([]vectorStoreResult, error) {
	switch {
	case len(languages) == 0:
		r1, err := store.Search(ctx, projectPath, qEmb, n, nil)
		if err != nil {
			return nil, err
		}
		return wrapResults(r1), nil
	case len(languages) == 1:
		r1, err := store.Search(ctx, projectPath, qEmb, n,
			map[string]string{"language": languages[0]})
		if err != nil {
			return nil, err
		}
		return wrapResults(r1), nil
	case len(languages) <= maxFanoutSearch:
		var combined []vectorStoreResult
		for _, lang := range languages {
			rPart, err := store.Search(ctx, projectPath, qEmb, n,
				map[string]string{"language": lang})
			if err != nil {
				return nil, err
			}
			combined = append(combined, wrapResults(rPart)...)
		}
		combined = dedupByLocation(combined)
		sort.SliceStable(combined, func(i, j int) bool {
			return combined[i].r.Score > combined[j].r.Score
		})
		return combined, nil
	default:
		rAll, err := store.Search(ctx, projectPath, qEmb, n*len(languages), nil)
		if err != nil {
			return nil, err
		}
		return wrapResults(rAll), nil
	}
}

// filterToSearchItems applies min-score, language post-filter, path
// whitelist (paths), and path blacklist (excludes). It does NOT truncate —
// the merge step needs the full filtered set to identify all overlaps
// before deciding which to drop.
func filterToSearchItems(
	wrapped []vectorStoreResult,
	minScore float32,
	paths []string,
	excludes []string,
	langSet map[string]struct{},
	applyPostLangFilter bool,
) []searchResultItem {
	filtered := make([]searchResultItem, 0, len(wrapped))
	for _, w := range wrapped {
		res := w.r
		if res.Score < minScore {
			continue
		}
		if applyPostLangFilter {
			if _, ok := langSet[res.Language]; !ok {
				continue
			}
		}
		if len(paths) > 0 {
			matched := false
			for _, pfx := range paths {
				if strings.HasPrefix(res.FilePath, pfx) || strings.Contains(res.FilePath, pfx) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(excludes) > 0 {
			excluded := false
			for _, pfx := range excludes {
				if strings.HasPrefix(res.FilePath, pfx) || strings.Contains(res.FilePath, pfx) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}
		filtered = append(filtered, searchResultItem{
			FilePath:   res.FilePath,
			StartLine:  res.StartLine,
			EndLine:    res.EndLine,
			Content:    res.Content,
			Score:      res.Score,
			ChunkType:  res.ChunkType,
			SymbolName: res.SymbolName,
			Language:   res.Language,
		})
	}
	return filtered
}
