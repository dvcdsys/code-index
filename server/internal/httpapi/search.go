package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
)

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
	MinScore  float32  `json:"min_score"`
}

type searchResultItem struct {
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Content    string  `json:"content"`
	Score      float32 `json:"score"`
	ChunkType  string  `json:"chunk_type"`
	SymbolName string  `json:"symbol_name"`
	Language   string  `json:"language"`
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
		if body.MinScore == 0 {
			body.MinScore = 0.1
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

		// Where filter — matches Python: single language → equality; multi → $or.
		var where map[string]string
		if len(body.Languages) == 1 {
			where = map[string]string{"language": body.Languages[0]}
		}
		// Note: chromem-go accepts a single where map; OR across languages is
		// handled via post-filter below when Languages > 1.

		results, err := d.VectorStore.Search(r.Context(), p.HostPath, qEmb, body.Limit*2, where)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		langSet := map[string]struct{}{}
		for _, l := range body.Languages {
			langSet[l] = struct{}{}
		}

		filtered := make([]searchResultItem, 0, len(results))
		for _, r := range results {
			if r.Score < body.MinScore {
				continue
			}
			if len(body.Languages) > 1 {
				if _, ok := langSet[r.Language]; !ok {
					continue
				}
			}
			if len(body.Paths) > 0 {
				matched := false
				for _, pfx := range body.Paths {
					if strings.HasPrefix(r.FilePath, pfx) || strings.Contains(r.FilePath, pfx) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			filtered = append(filtered, searchResultItem{
				FilePath:   r.FilePath,
				StartLine:  r.StartLine,
				EndLine:    r.EndLine,
				Content:    r.Content,
				Score:      r.Score,
				ChunkType:  r.ChunkType,
				SymbolName: r.SymbolName,
				Language:   r.Language,
			})
			if len(filtered) >= body.Limit {
				break
			}
		}

		elapsedMS := float64(time.Since(start).Microseconds()) / 1000.0
		elapsedMS = float64(int(elapsedMS*10+0.5)) / 10

		writeJSON(w, http.StatusOK, searchResponse{
			Results:     filtered,
			Total:       len(filtered),
			QueryTimeMS: elapsedMS,
		})
	}
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
