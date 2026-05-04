package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// Server is the chi-server implementation generated from doc/openapi.yaml.
// It owns the Deps bundle and translates between the generated types and
// the internal package types (projects.Project, indexer.Progress, etc.).
//
// Wire format guarantee: every JSON shape this struct emits is byte-identical
// to the pre-OpenAPI handler closures it replaced. The migration changes
// internal organisation only — request/response keys, status codes, and
// header behaviour are unchanged.
type Server struct {
	Deps Deps
}

// Compile-time assertion that Server implements the generated interface.
var _ openapi.ServerInterface = (*Server)(nil)

// ---------------------------------------------------------------------------
// Probe endpoints
// ---------------------------------------------------------------------------

// GetHealth — GET /health (public).
func (s *Server) GetHealth(w http.ResponseWriter, r *http.Request) {
	if s.Deps.DB != nil {
		pingCtx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.Deps.DB.PingContext(pingCtx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unhealthy",
				"reason": "db unreachable",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// GetStatus — GET /api/v1/status.
func (s *Server) GetStatus(w http.ResponseWriter, r *http.Request) {
	projectCount := 0
	activeJobs := 0
	if s.Deps.DB != nil {
		_ = s.Deps.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM projects`).Scan(&projectCount)
		_ = s.Deps.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM index_runs WHERE status = 'running'`).Scan(&activeJobs)
	}
	modelLoaded := false
	if s.Deps.EmbeddingSvc != nil {
		readyCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		modelLoaded = s.Deps.EmbeddingSvc.Ready(readyCtx) == nil
		cancel()
	}
	// PR-E — embedding_model must reflect the LIVE config (after any
	// dashboard runtime override + restart), not the boot-time value
	// stamped into Deps. Fall back to Deps when the service is a fake or
	// disabled, so test fixtures still get a stable string.
	model := s.Deps.EmbeddingModel
	if es, ok := s.Deps.EmbeddingSvc.(*embeddings.Service); ok && es != nil {
		if cfg := es.Config(); cfg != nil && cfg.EmbeddingModel != "" {
			model = cfg.EmbeddingModel
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":               "ok",
		"backend":              s.Deps.Backend,
		"server_version":       s.Deps.ServerVersion,
		"api_version":          s.Deps.APIVersion,
		"model_loaded":         modelLoaded,
		"embedding_model":      model,
		"projects":             projectCount,
		"active_indexing_jobs": activeJobs,
	})
}

// ---------------------------------------------------------------------------
// Project CRUD
// ---------------------------------------------------------------------------

// CreateProject — POST /api/v1/projects.
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	var body openapi.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.HostPath == "" {
		writeError(w, http.StatusUnprocessableEntity, "host_path is required")
		return
	}
	p, err := projects.Create(r.Context(), s.Deps.DB, projects.CreateRequest{HostPath: body.HostPath})
	if err != nil {
		if errors.Is(err, projects.ErrConflict) || errors.Is(err, projects.ErrOverlap) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, projectToOpenAPI(p))
}

// ListProjects — GET /api/v1/projects.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	list, err := projects.List(r.Context(), s.Deps.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]openapi.Project, 0, len(list))
	for i := range list {
		out = append(out, projectToOpenAPI(&list[i]))
	}
	writeJSON(w, http.StatusOK, openapi.ProjectListResponse{
		Projects: out,
		Total:    len(out),
	})
}

// GetProject — GET /api/v1/projects/{path}.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	out := projectToOpenAPI(p)
	s.enrichProjectStorage(&out, p)
	writeJSON(w, http.StatusOK, out)
}

// enrichProjectStorage fills the storage-related Project fields. Skipped
// when embeddings are disabled / unavailable — callers see those as nil and
// the dashboard hides the section. Per-call os.Stat is cheap enough for the
// single-project endpoint; we deliberately do NOT enrich the list endpoint
// (would multiply stat calls × N projects on every page load).
func (s *Server) enrichProjectStorage(out *openapi.Project, p *projects.Project) {
	es, ok := s.Deps.EmbeddingSvc.(*embeddings.Service)
	if !ok || es == nil {
		return
	}
	cfg := es.Config()
	if cfg == nil {
		return
	}
	sqlitePath := cfg.DynamicSQLitePath()
	if sqlitePath != "" {
		out.SqlitePath = ptrString(sqlitePath)
		if info, err := os.Stat(sqlitePath); err == nil {
			sz := info.Size()
			out.SqliteSizeBytes = &sz
		}
	}
	if cfg.ChromaPersistDir != "" {
		col := vectorstore.CollectionName(p.HostPath)
		dir := filepath.Join(cfg.DynamicChromaPersistDir(), col)
		out.ChromaPath = ptrString(dir)
		if sz, ok := dirSizeBytes(dir); ok {
			out.ChromaSizeBytes = &sz
		}
	}
}

// dirSizeBytes walks dir and sums regular-file sizes. Returns (0,false) on
// any error (missing dir, permission, etc.) so callers can decide to omit
// the field rather than report a misleading 0.
func dirSizeBytes(dir string) (int64, bool) {
	var total int64
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		return 0, false
	}
	return total, true
}

// UpdateProject — PATCH /api/v1/projects/{path}.
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	var body openapi.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	var settingsPtr *projects.Settings
	if body.Settings != nil {
		s := projects.Settings{
			ExcludePatterns: body.Settings.ExcludePatterns,
			MaxFileSize:     body.Settings.MaxFileSize,
		}
		settingsPtr = &s
	}
	updated, err := projects.Patch(r.Context(), s.Deps.DB, p.HostPath, projects.UpdateRequest{Settings: settingsPtr})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectToOpenAPI(updated))
}

// DeleteProject — DELETE /api/v1/projects/{path}.
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if err := projects.Delete(r.Context(), s.Deps.DB, p.HostPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Symbol / definition / reference / file search
// ---------------------------------------------------------------------------

// SearchSymbols — POST /api/v1/projects/{path}/search/symbols.
func (s *Server) SearchSymbols(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	var body openapi.SymbolSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusUnprocessableEntity, "query is required")
		return
	}
	limit := derefIntOrDefault(body.Limit, 20)
	kinds := derefStringSlice(body.Kinds)

	symbols, err := symbolindex.SearchByName(r.Context(), s.Deps.DB, p.HostPath, body.Query, kinds, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]openapi.SymbolResultItem, 0, len(symbols))
	for _, sym := range symbols {
		results = append(results, openapi.SymbolResultItem{
			Name:       sym.Name,
			Kind:       sym.Kind,
			FilePath:   sym.FilePath,
			Line:       sym.Line,
			EndLine:    sym.EndLine,
			Language:   sym.Language,
			Signature:  sym.Signature,
			ParentName: sym.ParentName,
		})
	}
	writeJSON(w, http.StatusOK, openapi.SymbolSearchResponse{
		Results: results,
		Total:   len(results),
	})
}

// SearchDefinitions — POST /api/v1/projects/{path}/search/definitions.
func (s *Server) SearchDefinitions(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	var body openapi.DefinitionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.Symbol == "" {
		writeError(w, http.StatusUnprocessableEntity, "symbol is required")
		return
	}
	limit := derefIntOrDefault(body.Limit, 10)
	kind := derefString(body.Kind)
	filePath := derefString(body.FilePath)

	syms, err := symbolindex.SearchDefinitions(r.Context(), s.Deps.DB, p.HostPath, body.Symbol, kind, filePath, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]openapi.DefinitionItem, 0, len(syms))
	for _, sym := range syms {
		results = append(results, openapi.DefinitionItem{
			Name:       sym.Name,
			Kind:       sym.Kind,
			FilePath:   sym.FilePath,
			Line:       sym.Line,
			EndLine:    sym.EndLine,
			Language:   sym.Language,
			Signature:  sym.Signature,
			ParentName: sym.ParentName,
		})
	}
	writeJSON(w, http.StatusOK, openapi.DefinitionResponse{
		Results: results,
		Total:   len(results),
	})
}

// SearchReferences — POST /api/v1/projects/{path}/search/references.
func (s *Server) SearchReferences(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	var body openapi.ReferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.Symbol == "" {
		writeError(w, http.StatusUnprocessableEntity, "symbol is required")
		return
	}
	limit := derefIntOrDefault(body.Limit, 50)
	filePath := derefString(body.FilePath)

	refs, err := symbolindex.SearchReferences(r.Context(), s.Deps.DB, p.HostPath, body.Symbol, filePath, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]openapi.ReferenceItem, 0, len(refs))
	for _, ref := range refs {
		results = append(results, openapi.ReferenceItem{
			FilePath:   ref.FilePath,
			StartLine:  ref.Line,
			EndLine:    ref.Line,
			Content:    "",
			ChunkType:  openapi.ReferenceItemChunkType("reference"),
			SymbolName: ref.Name,
			Language:   ref.Language,
		})
	}
	writeJSON(w, http.StatusOK, openapi.ReferenceResponse{
		Results: results,
		Total:   len(results),
	})
}

// SearchFiles — POST /api/v1/projects/{path}/search/files.
func (s *Server) SearchFiles(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	var body openapi.FileSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusUnprocessableEntity, "query is required")
		return
	}
	limit := derefIntOrDefault(body.Limit, 20)

	rows, err := s.Deps.DB.QueryContext(r.Context(),
		`SELECT file_path FROM file_hashes WHERE project_path = ? AND file_path LIKE ? ORDER BY file_path LIMIT ?`,
		p.HostPath, "%"+body.Query+"%", limit,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]openapi.FileResultItem, 0, limit)
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
		results = append(results, openapi.FileResultItem{
			FilePath: fp,
			Language: langPtr,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows.Close()
	writeJSON(w, http.StatusOK, openapi.FileSearchResponse{
		Results: results,
		Total:   len(results),
	})
}

// ---------------------------------------------------------------------------
// Project summary
// ---------------------------------------------------------------------------

// GetProjectSummary — GET /api/v1/projects/{path}/summary.
func (s *Server) GetProjectSummary(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}

	dirCount := map[string]int{}
	{
		rows, err := s.Deps.DB.QueryContext(r.Context(),
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
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		rows.Close()
	}
	topDirs := topNDirs(dirCount, 10)

	var recentSyms []openapi.SymbolEntry
	{
		symRows, err := s.Deps.DB.QueryContext(r.Context(),
			`SELECT name, kind, file_path, language FROM symbols WHERE project_path = ? LIMIT 20`,
			p.HostPath,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for symRows.Next() {
			var e openapi.SymbolEntry
			if err := symRows.Scan(&e.Name, &e.Kind, &e.FilePath, &e.Language); err != nil {
				symRows.Close()
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			recentSyms = append(recentSyms, e)
		}
		if err := symRows.Err(); err != nil {
			symRows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		symRows.Close()
	}
	if recentSyms == nil {
		recentSyms = []openapi.SymbolEntry{}
	}

	var totalSymbols int
	_ = s.Deps.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM symbols WHERE project_path = ?`, p.HostPath,
	).Scan(&totalSymbols)

	langs := p.Languages
	if langs == nil {
		langs = []string{}
	}

	writeJSON(w, http.StatusOK, openapi.ProjectSummary{
		PathHash:       projects.HashPath(p.HostPath),
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

// ---------------------------------------------------------------------------
// Semantic search
// ---------------------------------------------------------------------------

// SemanticSearch — POST /api/v1/projects/{path}/search.
func (s *Server) SemanticSearch(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.VectorStore == nil || s.Deps.EmbeddingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "semantic search not configured")
		return
	}
	var body openapi.SemanticSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		writeError(w, http.StatusUnprocessableEntity, "query is required")
		return
	}
	limit := derefIntOrDefault(body.Limit, 10)
	languages := derefStringSlice(body.Languages)
	paths := derefStringSlice(body.Paths)
	excludes := derefStringSlice(body.Excludes)

	minScore := float32(0.4)
	if body.MinScore != nil {
		minScore = *body.MinScore
	}

	start := time.Now()

	qEmb, err := s.Deps.EmbeddingSvc.EmbedQuery(r.Context(), body.Query)
	if err != nil {
		if retry, busy := embeddings.IsBusy(err); busy {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusServiceUnavailable,
				"GPU is busy processing another embedding request, retry after "+strconv.Itoa(retry)+"s")
			return
		}
		if errors.Is(err, embeddings.ErrDisabled) {
			writeError(w, http.StatusServiceUnavailable, "embeddings disabled")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	langSet := map[string]struct{}{}
	for _, l := range languages {
		langSet[l] = struct{}{}
	}
	applyPostLangFilter := len(languages) > maxFanoutSearch

	var fileGroups []fileGroupResult
	factor := 2
	for {
		n := limit * factor
		rawWrapped, err := fetchVectorResults(
			r.Context(), s.Deps.VectorStore, p.HostPath, qEmb, n, languages,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		filtered := filterToSearchItems(rawWrapped, minScore, paths, excludes, langSet, applyPostLangFilter)
		merged := mergeOverlappingHits(filtered)
		fileGroups = groupByFile(merged)
		if len(fileGroups) >= limit {
			break
		}
		if len(rawWrapped) < n {
			break
		}
		if factor >= maxFactorSearch {
			break
		}
		factor *= 2
	}
	if len(fileGroups) > limit {
		fileGroups = fileGroups[:limit]
	}

	elapsedMS := float64(time.Since(start).Microseconds()) / 1000.0
	elapsedMS = float64(int(elapsedMS*10+0.5)) / 10

	writeJSON(w, http.StatusOK, openapi.SemanticSearchResponse{
		Results:     fileGroupsToOpenAPI(fileGroups),
		Total:       len(fileGroups),
		QueryTimeMs: elapsedMS,
	})
}

// fileGroupsToOpenAPI converts the internal []fileGroupResult into the
// generated []openapi.FileGroupResult. Wire-compat: openapi.FileGroupResult
// uses *string for Language (with omitempty) instead of plain string —
// nil pointer and empty string both produce an absent JSON key.
func fileGroupsToOpenAPI(in []fileGroupResult) []openapi.FileGroupResult {
	out := make([]openapi.FileGroupResult, len(in))
	for i, g := range in {
		var langPtr *string
		if g.Language != "" {
			lang := g.Language
			langPtr = &lang
		}
		matches := make([]openapi.FileMatch, len(g.Matches))
		for j, m := range g.Matches {
			var nested *[]openapi.NestedHit
			if len(m.NestedHits) > 0 {
				ns := make([]openapi.NestedHit, len(m.NestedHits))
				for k, n := range m.NestedHits {
					var symPtr *string
					if n.SymbolName != "" {
						v := n.SymbolName
						symPtr = &v
					}
					ns[k] = openapi.NestedHit{
						StartLine:  n.StartLine,
						EndLine:    n.EndLine,
						SymbolName: symPtr,
						ChunkType:  n.ChunkType,
						Score:      n.Score,
					}
				}
				nested = &ns
			}
			var symPtr *string
			if m.SymbolName != "" {
				v := m.SymbolName
				symPtr = &v
			}
			matches[j] = openapi.FileMatch{
				StartLine:  m.StartLine,
				EndLine:    m.EndLine,
				Content:    m.Content,
				Score:      m.Score,
				ChunkType:  m.ChunkType,
				SymbolName: symPtr,
				NestedHits: nested,
			}
		}
		out[i] = openapi.FileGroupResult{
			FilePath:  g.FilePath,
			Language:  langPtr,
			BestScore: g.BestScore,
			Matches:   matches,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Indexing — three-phase protocol
// ---------------------------------------------------------------------------

// IndexBegin — POST /api/v1/projects/{path}/index/begin.
func (s *Server) IndexBegin(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.Indexer == nil {
		writeError(w, http.StatusServiceUnavailable, "indexer not configured")
		return
	}
	var body openapi.IndexBeginRequest
	// Body is optional — accept empty request.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
	}
	full := body.Full != nil && *body.Full

	runID, stored, err := s.Deps.Indexer.BeginIndexing(r.Context(), p.HostPath, full)
	if err != nil {
		if errors.Is(err, indexer.ErrSessionConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stored == nil {
		stored = map[string]string{}
	}
	writeJSON(w, http.StatusOK, openapi.IndexBeginResponse{
		RunId:        runID,
		StoredHashes: stored,
	})
}

// IndexFiles — POST /api/v1/projects/{path}/index/files.
//
// Honours `Accept: application/x-ndjson` to switch into the streaming
// variant; otherwise returns the legacy single-JSON summary.
func (s *Server) IndexFiles(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash, params openapi.IndexFilesParams) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.Indexer == nil {
		writeError(w, http.StatusServiceUnavailable, "indexer not configured")
		return
	}
	var body openapi.IndexFilesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.RunId == "" {
		writeError(w, http.StatusUnprocessableEntity, "run_id is required")
		return
	}
	if len(body.Files) > maxFilesPerBatch {
		writeError(w, http.StatusUnprocessableEntity, "too many files in batch (max 50)")
		return
	}
	files := make([]indexer.FilePayload, len(body.Files))
	for i, f := range body.Files {
		files[i] = indexer.FilePayload{
			Path:        f.Path,
			Content:     f.Content,
			ContentHash: f.ContentHash,
			Language:    derefString(f.Language),
			Size:        f.Size,
		}
	}

	// Accept-header negotiation. We re-read directly from the header rather
	// than trusting params.Accept alone because chi-server passes the
	// header verbatim — old clients that omit Accept get the JSON branch,
	// new clients that send `application/x-ndjson` get the stream.
	if acceptsNDJSON(r.Header.Get("Accept")) {
		indexFilesStreamingHandler(s.Deps, p, body.RunId, files, w, r)
		return
	}

	accepted, chunks, total, err := s.Deps.Indexer.ProcessFiles(r.Context(), p.HostPath, body.RunId, files)
	if err != nil {
		if retry, busy := embeddings.IsBusy(err); busy {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusServiceUnavailable,
				"GPU is busy processing another embedding request, retry after "+strconv.Itoa(retry)+"s")
			return
		}
		if errors.Is(err, indexer.ErrNoSession) || errors.Is(err, indexer.ErrProjectMismatch) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, openapi.IndexFilesResponse{
		FilesAccepted:       accepted,
		ChunksCreated:       chunks,
		FilesProcessedTotal: total,
	})
}

// IndexFinish — POST /api/v1/projects/{path}/index/finish.
func (s *Server) IndexFinish(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.Indexer == nil {
		writeError(w, http.StatusServiceUnavailable, "indexer not configured")
		return
	}
	var body openapi.IndexFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	if body.RunId == "" {
		writeError(w, http.StatusUnprocessableEntity, "run_id is required")
		return
	}
	deletedPaths := derefStringSlice(body.DeletedPaths)
	totalDiscovered := derefIntOrDefault(body.TotalFilesDiscovered, 0)

	status, files, chunks, err := s.Deps.Indexer.FinishIndexing(
		r.Context(), p.HostPath, body.RunId, deletedPaths, totalDiscovered,
	)
	if err != nil {
		if errors.Is(err, indexer.ErrNoSession) || errors.Is(err, indexer.ErrProjectMismatch) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, openapi.IndexFinishResponse{
		Status:         openapi.IndexFinishResponseStatus(status),
		FilesProcessed: files,
		ChunksCreated:  chunks,
	})
}

// IndexCancel — POST /api/v1/projects/{path}/index/cancel.
func (s *Server) IndexCancel(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.Indexer == nil {
		writeJSON(w, http.StatusOK, openapi.IndexCancelResponse{Cancelled: false})
		return
	}
	cancelled, err := s.Deps.Indexer.CancelIndexing(r.Context(), p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, openapi.IndexCancelResponse{Cancelled: cancelled})
}

// IndexStatus — GET /api/v1/projects/{path}/index/status.
func (s *Server) IndexStatus(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	p := s.lookupProject(w, r, path)
	if p == nil {
		return
	}
	if s.Deps.Indexer == nil {
		writeJSON(w, http.StatusOK, openapi.IndexProgressResponse{Status: "idle"})
		return
	}
	prog := s.Deps.Indexer.GetProgress(p.HostPath)
	if prog != nil {
		// Active session — emit the full progress payload. We use a
		// raw map (not openapi.IndexProgressInfo) to preserve the
		// historical wire shape: every field is present even when
		// integer-zero (Python emitted these keys unconditionally).
		writeJSON(w, http.StatusOK, openapi.IndexProgressResponse{
			Status: openapi.IndexProgressResponseStatus(prog.Status),
			Progress: &openapi.IndexProgressInfo{
				Phase:           progressPhasePtr(prog.Phase),
				FilesDiscovered: ptrInt(prog.FilesDiscovered),
				FilesProcessed:  ptrInt(prog.FilesProcessed),
				FilesTotal:      ptrInt(prog.FilesTotal),
				ChunksCreated:   ptrInt(prog.ChunksCreated),
				ElapsedSeconds:  ptrFloat64(roundFloat1(prog.ElapsedSeconds)),
				RunId:           ptrString(prog.RunID),
			},
		})
		return
	}
	// Fall back to last run row.
	row := s.Deps.DB.QueryRowContext(r.Context(),
		`SELECT status, files_processed, files_total, chunks_created
		 FROM index_runs WHERE project_path = ? ORDER BY started_at DESC LIMIT 1`,
		p.HostPath,
	)
	var status string
	var filesProcessed, filesTotal, chunks int
	if err := row.Scan(&status, &filesProcessed, &filesTotal, &chunks); err != nil {
		writeJSON(w, http.StatusOK, openapi.IndexProgressResponse{Status: "idle"})
		return
	}
	writeJSON(w, http.StatusOK, openapi.IndexProgressResponse{
		Status: openapi.IndexProgressResponseStatus(status),
		Progress: &openapi.IndexProgressInfo{
			FilesProcessed: ptrInt(filesProcessed),
			FilesTotal:     ptrInt(filesTotal),
			ChunksCreated:  ptrInt(chunks),
		},
	})
}

// ---------------------------------------------------------------------------
// Internal helpers — type conversion + legacy lookup wrapper
// ---------------------------------------------------------------------------

// lookupProject resolves the {path} URL parameter. Wraps resolveProjectFromHash
// so generated method signatures stay clean.
func (s *Server) lookupProject(w http.ResponseWriter, r *http.Request, _ openapi.ProjectHash) *projects.Project {
	// Use the helper that already exists in search.go — it pulls the
	// {path} chi URL param from r and writes a 404 on miss.
	return resolveProjectFromHash(w, r, s.Deps)
}

// projectToOpenAPI converts the internal projects.Project (string dates,
// flat Settings/Stats) into the generated openapi.Project (time.Time dates,
// embedded openapi.ProjectSettings/Stats).
//
// Wire-compat: projects.Project.CreatedAt/UpdatedAt/LastIndexedAt are
// RFC3339Nano strings produced by indexer.nowUTC (time.RFC3339Nano). Go's
// time.Time MarshalJSON also emits RFC3339Nano; round-tripping through
// time.Parse → time.Time produces byte-identical JSON output.
func projectToOpenAPI(p *projects.Project) openapi.Project {
	langs := p.Languages
	if langs == nil {
		langs = []string{}
	}
	created, _ := time.Parse(time.RFC3339Nano, p.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, p.UpdatedAt)
	var lastIndexed *time.Time
	if p.LastIndexedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *p.LastIndexedAt)
		if err == nil {
			lastIndexed = &t
		}
	}
	out := openapi.Project{
		PathHash:      projects.HashPath(p.HostPath),
		HostPath:      p.HostPath,
		ContainerPath: p.ContainerPath,
		Languages:     langs,
		Settings: openapi.ProjectSettings{
			ExcludePatterns: p.Settings.ExcludePatterns,
			MaxFileSize:     p.Settings.MaxFileSize,
		},
		Stats: openapi.ProjectStats{
			TotalFiles:   p.Stats.TotalFiles,
			IndexedFiles: p.Stats.IndexedFiles,
			TotalChunks:  p.Stats.TotalChunks,
			TotalSymbols: p.Stats.TotalSymbols,
		},
		Status:        openapi.ProjectStatus(p.Status),
		CreatedAt:     created,
		UpdatedAt:     updated,
		LastIndexedAt: lastIndexed,
	}
	if p.IndexedWithModel != nil {
		v := *p.IndexedWithModel
		out.IndexedWithModel = &v
	}
	return out
}

// topNDirs sorts a path→count map descending and returns the top n entries
// as openapi.DirEntry. Replaces the closure-private topN that lived in
// search.go's projectSummaryHandler.
func topNDirs(m map[string]int, n int) []openapi.DirEntry {
	type kv struct {
		k string
		v int
	}
	kvs := make([]kv, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	sort.SliceStable(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	if n > len(kvs) {
		n = len(kvs)
	}
	out := make([]openapi.DirEntry, n)
	for i := 0; i < n; i++ {
		out[i] = openapi.DirEntry{Path: kvs[i].k, FileCount: kvs[i].v}
	}
	return out
}

// progressPhasePtr converts a phase string into a typed pointer; empty
// strings collapse to nil so omitempty drops the key from the wire payload.
func progressPhasePtr(s string) *openapi.IndexProgressInfoPhase {
	if s == "" {
		return nil
	}
	v := openapi.IndexProgressInfoPhase(s)
	return &v
}

func ptrInt(v int) *int             { return &v }
func ptrFloat64(v float64) *float64 { return &v }
func ptrString(v string) *string    { return &v }

// derefString returns the string pointed to by p, or "" if p is nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefStringSlice returns the slice pointed to by p, or nil if p is nil.
// Use this for optional array fields where downstream code accepts nil.
func derefStringSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// derefIntOrDefault returns the int pointed to by p when present and > 0;
// otherwise returns def. This matches the original handlers' behaviour
// `if body.Limit <= 0 { body.Limit = default }`.
func derefIntOrDefault(p *int, def int) int {
	if p == nil || *p <= 0 {
		return def
	}
	return *p
}
