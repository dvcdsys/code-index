package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

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
		fp     string
		start  int
		end    int
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
// Request / response types (match Python schemas/search.py exactly)
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
	Symbol   string  `json:"symbol"`
	Kind     string  `json:"kind"`
	FilePath string  `json:"file_path"`
	Limit    int     `json:"limit"`
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

// ---------------------------------------------------------------------------
// resolveProjectFromHash looks up the project by URL path hash.
// Returns the project or writes a 404 and returns nil.
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
// POST /api/v1/projects/{path}/search/symbols
// ---------------------------------------------------------------------------

func symbolSearchHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}

		var body symbolSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.Query == "" {
			writeError(w, http.StatusUnprocessableEntity, "query is required")
			return
		}
		if body.Limit <= 0 {
			body.Limit = 20
		}

		symbols, err := symbolindex.SearchByName(r.Context(), d.DB, p.HostPath, body.Query, body.Kinds, body.Limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		results := make([]symbolResultItem, 0, len(symbols))
		for _, s := range symbols {
			results = append(results, symbolResultItem{
				Name:       s.Name,
				Kind:       s.Kind,
				FilePath:   s.FilePath,
				Line:       s.Line,
				EndLine:    s.EndLine,
				Language:   s.Language,
				Signature:  s.Signature,
				ParentName: s.ParentName,
			})
		}
		writeJSON(w, http.StatusOK, symbolSearchResponse{Results: results, Total: len(results)})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/search/definitions
// ---------------------------------------------------------------------------

func definitionSearchHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}

		var body definitionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.Symbol == "" {
			writeError(w, http.StatusUnprocessableEntity, "symbol is required")
			return
		}
		if body.Limit <= 0 {
			body.Limit = 10
		}

		syms, err := symbolindex.SearchDefinitions(r.Context(), d.DB, p.HostPath, body.Symbol, body.Kind, body.FilePath, body.Limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		results := make([]definitionItem, 0, len(syms))
		for _, s := range syms {
			results = append(results, definitionItem{
				Name:       s.Name,
				Kind:       s.Kind,
				FilePath:   s.FilePath,
				Line:       s.Line,
				EndLine:    s.EndLine,
				Language:   s.Language,
				Signature:  s.Signature,
				ParentName: s.ParentName,
			})
		}
		writeJSON(w, http.StatusOK, definitionResponse{Results: results, Total: len(results)})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/search/references
// ---------------------------------------------------------------------------

func referenceSearchHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}

		var body referenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.Symbol == "" {
			writeError(w, http.StatusUnprocessableEntity, "symbol is required")
			return
		}
		if body.Limit <= 0 {
			body.Limit = 50
		}

		refs, err := symbolindex.SearchReferences(r.Context(), d.DB, p.HostPath, body.Symbol, body.FilePath, body.Limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// m3 — the refs table stores only token locations (name, file, line,
		// col) so `Content` is intentionally empty and `EndLine == StartLine`.
		// Matches the Python `ReferenceIndexService` shape. Clients that need
		// source snippets should follow up with a semantic search or a
		// file-read; populating Content here would require a full-file
		// re-read on every request and was deemed too costly.
		results := make([]referenceItem, 0, len(refs))
		for _, ref := range refs {
			results = append(results, referenceItem{
				FilePath:   ref.FilePath,
				StartLine:  ref.Line,
				EndLine:    ref.Line,
				Content:    "",
				ChunkType:  "reference",
				SymbolName: ref.Name,
				Language:   ref.Language,
			})
		}
		writeJSON(w, http.StatusOK, referenceResponse{Results: results, Total: len(results)})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/search/files
// ---------------------------------------------------------------------------

func fileSearchHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}

		var body fileSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.Query == "" {
			writeError(w, http.StatusUnprocessableEntity, "query is required")
			return
		}
		if body.Limit <= 0 {
			body.Limit = 20
		}

		var results []fileResultItem
		{
			rows, err := d.DB.QueryContext(r.Context(),
				`SELECT file_path FROM file_hashes WHERE project_path = ? AND file_path LIKE ? ORDER BY file_path LIMIT ?`,
				p.HostPath, "%"+body.Query+"%", body.Limit,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for rows.Next() {
				var fp string
				if err := rows.Scan(&fp); err != nil {
					rows.Close()
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				lang := langdetect.Detect(fp)
				var langPtr *string
				if lang != "" {
					langPtr = &lang
				}
				results = append(results, fileResultItem{FilePath: fp, Language: langPtr})
			}
			// m1 — a WAL / IO error during iteration would otherwise return a
			// partial list with HTTP 200 and no hint that anything went wrong.
			if err := rows.Err(); err != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			rows.Close()
		}
		if results == nil {
			results = []fileResultItem{}
		}
		writeJSON(w, http.StatusOK, fileSearchResponse{Results: results, Total: len(results)})
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/projects/{path}/summary
// ---------------------------------------------------------------------------

func projectSummaryHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}

		// Top directories — from file_hashes.
		dirCount := map[string]int{}
		{
			rows, err := d.DB.QueryContext(r.Context(),
				`SELECT file_path FROM file_hashes WHERE project_path = ?`, p.HostPath,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for rows.Next() {
				var fp string
				if err := rows.Scan(&fp); err != nil {
					rows.Close()
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				// Mirrors Python path bucketing logic.
				parts := splitPath(fp)
				var key string
				if len(parts) > 3 {
					key = joinPath(parts[:4])
				} else if len(parts) > 1 {
					key = joinPath(parts[:2])
				}
				if key != "" {
					dirCount[key]++
				}
			}
			if err := rows.Err(); err != nil { // m1
				rows.Close()
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			rows.Close()
		}

		topDirs := topN(dirCount, 10)

		// Recent symbols.
		var recentSyms []symbolEntry
		{
			symRows, err := d.DB.QueryContext(r.Context(),
				`SELECT name, kind, file_path, language FROM symbols WHERE project_path = ? LIMIT 20`,
				p.HostPath,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for symRows.Next() {
				var s symbolEntry
				if err := symRows.Scan(&s.Name, &s.Kind, &s.FilePath, &s.Language); err != nil {
					symRows.Close()
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				recentSyms = append(recentSyms, s)
			}
			if err := symRows.Err(); err != nil { // m1
				symRows.Close()
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			symRows.Close()
		}
		if recentSyms == nil {
			recentSyms = []symbolEntry{}
		}

		// Total symbol count.
		var totalSymbols int
		_ = d.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM symbols WHERE project_path = ?`, p.HostPath,
		).Scan(&totalSymbols)

		langs := p.Languages
		if langs == nil {
			langs = []string{}
		}

		writeJSON(w, http.StatusOK, projectSummaryResponse{
			HostPath:       p.HostPath,
			Status:         p.Status,
			Languages:      langs,
			TotalFiles:     p.Stats.TotalFiles,
			TotalChunks:    p.Stats.TotalChunks,
			TotalSymbols:   totalSymbols,
			TopDirectories: topDirs,
			RecentSymbols:  recentSyms,
		})
	}
}

// ---------------------------------------------------------------------------
// Path helpers — mirror Python Path(fp).parts logic
// ---------------------------------------------------------------------------

func splitPath(fp string) []string {
	// filepath.SplitList is for PATH env — use manual split.
	// We want to split by "/" for consistency with Python pathlib.
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

// topN returns the top-n directory entries by count.
func topN(m map[string]int, n int) []dirEntry {
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	// Sort descending.
	for i := 1; i < len(kvs); i++ {
		j := i
		for j > 0 && kvs[j].v > kvs[j-1].v {
			kvs[j], kvs[j-1] = kvs[j-1], kvs[j]
			j--
		}
	}
	if n > len(kvs) {
		n = len(kvs)
	}
	out := make([]dirEntry, n)
	for i := 0; i < n; i++ {
		out[i] = dirEntry{Path: kvs[i].k, FileCount: kvs[i].v}
	}
	return out
}

// Ensure symbolindex and sql are used (avoid import cycle in future if moved).
var _ = (*sql.DB)(nil)
var _ = symbolindex.Symbol{}

// ---------------------------------------------------------------------------
// Semantic search — POST /api/v1/projects/{path}/search
// ---------------------------------------------------------------------------

type searchRequest struct {
	Query     string   `json:"query"`
	Limit     int      `json:"limit"`
	Languages []string `json:"languages"`
	Paths     []string `json:"paths"`
	// MinScore is a pointer so we can distinguish "not provided" from an
	// explicit zero. Python uses a Pydantic default (0.1) which also allows
	// explicit 0 through — mirror that here. m2 fix.
	MinScore *float32 `json:"min_score,omitempty"`
}

type searchResultItem struct {
	FilePath   string      `json:"file_path"`
	StartLine  int         `json:"start_line"`
	EndLine    int         `json:"end_line"`
	Content    string      `json:"content"`
	Score      float32     `json:"score"`
	ChunkType  string      `json:"chunk_type"`
	SymbolName string      `json:"symbol_name"`
	Language   string      `json:"language"`
	// NestedHits records other matches inside this result's line range that
	// were merged into it by mergeOverlappingHits. Populated only when at
	// least one inner hit was absorbed; emitted as `nested_hits` in JSON.
	// The renderer uses these to show breadcrumbs (e.g. "+ 2 more matches:
	// H2 'Foo' line 27, H3 'Bar' line 29") so the user can see WHY this
	// outer chunk ranks well even when the actual signal came from a
	// sub-section.
	NestedHits []nestedHit `json:"nested_hits,omitempty"`
}

// nestedHit is a compact view of a chunk that was merged INTO another
// result. We don't need the full content (the parent's content already
// contains it textually) — just enough metadata to render a breadcrumb
// and let the caller jump to the exact line.
type nestedHit struct {
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	SymbolName string  `json:"symbol_name,omitempty"`
	ChunkType  string  `json:"chunk_type"`
	Score      float32 `json:"score"`
}

type searchResponse struct {
	Results     []searchResultItem `json:"results"`
	Total       int                `json:"total"`
	QueryTimeMS float64            `json:"query_time_ms"`
}

// semanticSearchHandler implements POST /api/v1/projects/{path}/search,
// matching api/app/routers/search.py semantic_search behaviour:
//   - embed query with prefix
//   - query vectorstore with limit*2 and optional where(language)
//   - post-filter by min_score + paths (prefix OR substring)
//   - trim to limit, round query_time_ms to 1 decimal
func semanticSearchHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.VectorStore == nil || d.EmbeddingSvc == nil {
			writeError(w, http.StatusServiceUnavailable, "semantic search not configured")
			return
		}

		var body searchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if strings.TrimSpace(body.Query) == "" {
			writeError(w, http.StatusUnprocessableEntity, "query is required")
			return
		}
		if body.Limit <= 0 {
			body.Limit = 10
		}
		// m2 — only apply default when the caller did not send the field.
		// Explicit 0 means "return everything above the HNSW floor".
		minScore := float32(0.1)
		if body.MinScore != nil {
			minScore = *body.MinScore
		}

		start := time.Now()

		qEmb, err := d.EmbeddingSvc.EmbedQuery(r.Context(), body.Query)
		if err != nil {
			if retry, busy := embeddings.IsBusy(err); busy {
				w.Header().Set("Retry-After", strconvItoa(retry))
				writeError(w, http.StatusServiceUnavailable,
					"GPU is busy processing another embedding request, retry after "+strconvItoa(retry)+"s")
				return
			}
			if errors.Is(err, embeddings.ErrDisabled) {
				writeError(w, http.StatusServiceUnavailable, "embeddings disabled")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Post-filter (path/language) and merge state are computed once outside
		// the window loop — both are cheap and don't depend on factor.
		langSet := map[string]struct{}{}
		for _, l := range body.Languages {
			langSet[l] = struct{}{}
		}
		applyPostLangFilter := len(body.Languages) > maxFanoutSearch

		// Windowed retrieval. Start by asking the vector store for limit×2
		// (the historical default), and if mergeOverlappingHits collapses
		// the result set below the user's --limit budget — typically because
		// of nested markdown sections or class+method overlaps — re-ask for
		// limit×4, then ×8, up to ×maxFactorSearch. Stops early when the
		// store returns fewer rows than requested (HNSW exhausted).
		var merged []searchResultItem
		factor := 2
		for {
			n := body.Limit * factor
			rawWrapped, err := fetchVectorResults(
				r.Context(), d.VectorStore, p.HostPath, qEmb, n, body.Languages,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			filtered := filterToSearchItems(rawWrapped, minScore, body.Paths, langSet, applyPostLangFilter)
			merged = mergeOverlappingHits(filtered)
			if len(merged) >= body.Limit {
				break
			}
			if len(rawWrapped) < n {
				// Vector store returned everything it had — no point asking again.
				break
			}
			if factor >= maxFactorSearch {
				break
			}
			factor *= 2
		}

		if len(merged) > body.Limit {
			merged = merged[:body.Limit]
		}

		elapsedMS := float64(time.Since(start).Microseconds()) / 1000.0
		elapsedMS = float64(int(elapsedMS*10+0.5)) / 10

		writeJSON(w, http.StatusOK, searchResponse{
			Results:     merged,
			Total:       len(merged),
			QueryTimeMS: elapsedMS,
		})
	}
}

// maxFanoutSearch is the language-count threshold above which we drop
// per-language pre-filter and fall back to a single over-fetched query
// with post-filter. Same value as the previous inline `maxFanout`.
const maxFanoutSearch = 4

// maxFactorSearch caps the windowed retrieval expansion. With body.Limit=10
// and factor=16 we top out at 160 raw results — enough to fill the budget
// even on heavily nested markdown without spending all day re-querying.
const maxFactorSearch = 16

// fetchVectorResults performs the per-language fan-out vector-store query
// at the given limit and returns deduped, score-sorted results. Extracted
// from semanticSearchHandler so the windowed retry loop can call it with
// growing `n` values without duplicating the four-case switch.
//
// The fan-out strategy mirrors the original inline logic: 0 languages →
// single query; 1 language → single query with where-filter; 2..maxFanout
// → N queries with per-language where-filter, deduped and re-sorted by
// score; >maxFanout → single oversized query, post-filter handled by
// caller (filterToSearchItems with applyPostLangFilter=true).
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

// filterToSearchItems applies min-score, language post-filter, and path
// prefix/substring matches. It does NOT truncate — the merge step needs
// the full filtered set to identify all overlaps before deciding which to
// drop. Truncation happens after merge in the caller.
func filterToSearchItems(
	wrapped []vectorStoreResult,
	minScore float32,
	paths []string,
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

// strconvItoa avoids pulling strconv just for one call in this file — mirrors
// the pattern used elsewhere in the package.
func strconvItoa(n int) string {
	// strconv is already imported elsewhere in the package? No — keep inline.
	// Use fmt-free int-to-string.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
