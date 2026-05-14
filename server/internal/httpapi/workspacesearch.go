package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/dvcdsys/code-index/server/internal/chunksfts"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// Tuning constants for the hybrid workspace search.
//
//   - perProjectLimit / bm25Limit: per-side retrieval depth per project.
//     50 leaves room for RRF fusion to differentiate the top candidates
//     without making rare-but-real later hits unreachable.
//   - topNPerProject: how many of a project's strongest hits feed into
//     the per-side aggregate signal used for candidacy.
//   - topProjectsDefault: default "Top projects" panel size.
//   - perProjectChunkCap: max chunks from any single project in the
//     final flat chunks list. Prevents the dominant repo from eating
//     every slot.
//   - alpha: weight of the BM25 (sparse) signal in the candidacy
//     blend. 0.5 = equal weighting. BM25 carries the project-gating
//     signal (a project with zero literal token matches is a strong
//     "irrelevant" cue dense alone can't produce) so we don't tilt
//     toward dense even when scores look more authoritative there.
//   - relativeProjThreshold: surviving projects must score ≥ best *
//     this fraction. Relative-not-absolute so the gate stays useful
//     across queries of varying strength.
//   - rrfK: standard RRF constant from Cormack 2009. 60 is the
//     widely-used default — small enough that rank-1 dominates,
//     large enough that ranks 5-10 still contribute.
const (
	workspaceSearchPerProjectLimit = 50
	workspaceSearchBM25Limit       = 50
	workspaceSearchTopNPerProject  = 5
	workspaceSearchTopProjects     = 10
	workspaceSearchPerProjChunkCap = 5
	workspaceSearchAlpha           = 0.5
	workspaceSearchProjThreshold   = 0.4
	rrfK                           = 60
)

// workspaceSearchProjectPayload mirrors WorkspaceSearchProject from
// the OpenAPI spec. Hand-rolled so the JSON shape stays plain Go
// types rather than the generated alias indirection.
type workspaceSearchProjectPayload struct {
	ProjectPath  string  `json:"project_path"`
	Label        string  `json:"label"`
	ProjectScore float32 `json:"project_score"`
	NumHits      int     `json:"num_hits"`
	// BM25Score and DenseScore are the per-signal aggregates that
	// feed into ProjectScore. Surfaced so the dashboard can show
	// "this repo ranked high because BM25 matched literal tokens" vs.
	// "ranked on dense semantic similarity only".
	BM25Score  float32 `json:"bm25_score"`
	DenseScore float32 `json:"dense_score"`
}

type workspaceSearchChunkPayload struct {
	ProjectPath string  `json:"project_path"`
	FilePath    string  `json:"file_path"`
	StartLine   int     `json:"start_line"`
	EndLine     int     `json:"end_line"`
	SymbolName  string  `json:"symbol_name,omitempty"`
	Language    string  `json:"language,omitempty"`
	Score       float32 `json:"score"`
	Content     string  `json:"content"`
}

type workspaceSearchPendingRepoPayload struct {
	ProjectPath string `json:"project_path"`
	Status      string `json:"status"`
}

type workspaceSearchFailedRepoPayload struct {
	ProjectPath string `json:"project_path"`
	Reason      string `json:"reason"`
}

// workspaceSearchStaleFTSRepoPayload reports a repo that was indexed
// before the chunks_fts mirror existed: dense search works, BM25
// returns nothing for it, hybrid degrades to pure-dense for that one
// entry. Dashboard renders a banner telling the operator to reindex.
type workspaceSearchStaleFTSRepoPayload struct {
	ProjectPath string `json:"project_path"`
}

// projectHits is the per-project intermediate state accumulated across
// the parallel fan-out. Dense and BM25 sides arrive separately and are
// fused inside the goroutine before being collected.
type projectHits struct {
	ProjectPath string
	// FusedChunks are the per-project chunks ranked by RRF over the
	// dense + BM25 lists. Highest fused rank first.
	FusedChunks []workspaceSearchChunkPayload
	// DenseSignal is the mean of the top-N dense scores in the
	// project (cosine, [0,1]).
	DenseSignal float32
	// BM25Signal is the mean of the top-N BM25 scores in the project
	// (positive, unbounded — SQLite's bm25() flipped via -bm25 at
	// the chunksfts boundary). Normalized into candidacy via
	// per-query min-max before being blended.
	BM25Signal float32
	// Candidacy is the α-blended, per-query-normalized score the
	// projects panel ranks by; recomputed after every project's
	// fan-out completes so the normalization sees all candidates.
	Candidacy float32
}

// WorkspaceSearch — GET /api/v1/workspaces/{id}/search.
//
// Hybrid BM25+dense fan-out. Each project runs two queries in
// parallel: dense (chromem cosine) and sparse (SQLite FTS5 BM25 over
// chunks_fts). Per project, the two ranked lists are fused via
// Reciprocal Rank Fusion. Across projects, an α-blended candidacy
// score (with per-query min-max normalization on both signals) plus
// a relative threshold (`candidacy ≥ best × 0.4`) keeps the result
// set focused on repos that actually share vocabulary or semantics
// with the query — pure-dense fan-out leaked every workspace repo at
// noise-level cosine similarity, since chromem returns the N nearest
// vectors regardless of how far away "nearest" actually is.
//
// Observed pre-hybrid: in a workspace of N repos, the repos that
// contained zero literal mentions of the query term still surfaced
// 50 chunks each at noise-level cosine (0.17-0.27). With hybrid +
// project threshold those repos drop out, restoring the cross-project
// signal the user needs to scope an agent's follow-up search.
func (s *Server) WorkspaceSearch(w http.ResponseWriter, r *http.Request, id string, params openapi.WorkspaceSearchParams) {
	if s.workspaceProjectsUnavailable(w) {
		return
	}
	if s.Deps.VectorStore == nil || s.Deps.EmbeddingSvc == nil {
		writeError(w, http.StatusServiceUnavailable,
			"embeddings or vectorstore not configured — workspace search requires both")
		return
	}
	if _, err := s.Deps.Workspaces.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, workspaces.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}

	if params.Q == "" {
		writeError(w, http.StatusUnprocessableEntity, "q is required")
		return
	}
	topProjects := clampInt(params.TopProjects, workspaceSearchTopProjects, 1, 50)
	topChunks := clampInt(params.TopChunks, 20, 1, 200)
	// Default 0.4 matches per-project SemanticSearch default so a query
	// that returns nothing from a single project doesn't surface a wall
	// of weak-cosine noise when broadcast across the workspace. Cross-
	// project sweeps that want long-tail recall must pass min_score=0
	// explicitly.
	minScore := clampFloat32(params.MinScore, 0.4, 0, 1)

	queryEmbedding, err := s.Deps.EmbeddingSvc.EmbedQuery(r.Context(), params.Q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not embed query: "+err.Error())
		return
	}
	if len(queryEmbedding) == 0 {
		writeError(w, http.StatusServiceUnavailable, "embedder returned empty vector")
		return
	}

	// Pull the workspace's project memberships joined with the projects
	// table so we can split into indexed vs pending in one pass. The
	// junction lives in workspace_projects; status lives on projects.
	rows, err := s.Deps.DB.QueryContext(r.Context(), `
		SELECT p.host_path, p.status
		  FROM workspace_projects wp
		  JOIN projects p ON p.host_path = wp.project_path
		 WHERE wp.workspace_id = ?
		 ORDER BY wp.added_at DESC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace projects: "+err.Error())
		return
	}
	type memberRow struct {
		ProjectPath string
		Status      string
	}
	var members []memberRow
	for rows.Next() {
		var m memberRow
		if scanErr := rows.Scan(&m.ProjectPath, &m.Status); scanErr != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "scan workspace project row: "+scanErr.Error())
			return
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "iterate workspace projects: "+err.Error())
		return
	}
	if len(members) == 0 {
		writeJSON(w, http.StatusOK, workspaceSearchResponse(
			"empty",
			[]workspaceSearchProjectPayload{},
			[]workspaceSearchChunkPayload{},
			nil,
			nil,
			nil,
		))
		return
	}

	seenProjects := make(map[string]struct{}, len(members))
	projectPaths := make([]string, 0, len(members))
	pendingRepos := make([]workspaceSearchPendingRepoPayload, 0)
	for _, m := range members {
		if m.Status != "indexed" {
			pendingRepos = append(pendingRepos, workspaceSearchPendingRepoPayload{
				ProjectPath: m.ProjectPath,
				Status:      m.Status,
			})
			continue
		}
		if _, ok := seenProjects[m.ProjectPath]; ok {
			continue
		}
		seenProjects[m.ProjectPath] = struct{}{}
		projectPaths = append(projectPaths, m.ProjectPath)
	}

	if len(projectPaths) == 0 {
		writeJSON(w, http.StatusOK, workspaceSearchResponse(
			"empty",
			[]workspaceSearchProjectPayload{},
			[]workspaceSearchChunkPayload{},
			pendingRepos,
			nil,
			nil,
		))
		return
	}

	// Detect repos that were indexed before chunks_fts existed:
	// file_hashes has rows for them (so they're "indexed") but
	// chunks_meta is empty, meaning the BM25 side is permanently 0
	// until a reindex backfills it. We still run the search (dense
	// works) but surface the list so the dashboard can prompt for a
	// reindex — otherwise the operator sees no observable difference
	// from the pre-hybrid algorithm and assumes the change didn't
	// take effect.
	staleRepos := s.detectStaleFTSRepos(r.Context(), projectPaths)

	hits, failedRepos, err := s.fanOutHybrid(r.Context(), id, projectPaths, params.Q, queryEmbedding, minScore)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fan-out search failed: "+err.Error())
		return
	}

	// Per-query min-max normalization on each signal independently,
	// then α-blend. Both signals are >=0; using raw/max instead of
	// (raw-min)/(max-min) means a project at 60% of best gets 0.6
	// candidacy rather than being projected to 0 (which the strict
	// min-max form would do whenever the workspace has even one weak
	// project).
	var bm25Max, denseMax float32
	for _, ph := range hits {
		if ph.BM25Signal > bm25Max {
			bm25Max = ph.BM25Signal
		}
		if ph.DenseSignal > denseMax {
			denseMax = ph.DenseSignal
		}
	}
	for i := range hits {
		var bm25Norm, denseNorm float32
		if bm25Max > 0 {
			bm25Norm = hits[i].BM25Signal / bm25Max
		}
		if denseMax > 0 {
			denseNorm = hits[i].DenseSignal / denseMax
		}
		hits[i].Candidacy = workspaceSearchAlpha*bm25Norm + (1-workspaceSearchAlpha)*denseNorm
	}

	var bestCand float32
	for _, ph := range hits {
		if ph.Candidacy > bestCand {
			bestCand = ph.Candidacy
		}
	}
	threshold := bestCand * workspaceSearchProjThreshold
	surviving := make([]projectHits, 0, len(hits))
	for _, ph := range hits {
		// A project with zero chunks contributes nothing regardless of
		// candidacy — keeping the entry would create a row in the
		// projects panel with num_hits=0 which is just visual noise.
		if len(ph.FusedChunks) == 0 {
			continue
		}
		if ph.Candidacy < threshold || ph.Candidacy <= 0 {
			continue
		}
		surviving = append(surviving, ph)
	}

	if len(surviving) == 0 {
		status := "empty"
		if len(failedRepos) > 0 {
			status = "partial_failure"
		}
		writeJSON(w, http.StatusOK, workspaceSearchResponse(
			status,
			[]workspaceSearchProjectPayload{},
			[]workspaceSearchChunkPayload{},
			pendingRepos,
			failedRepos,
			staleRepos,
		))
		return
	}

	// Build the projects panel + the flat chunk list. Per-project cap
	// is applied to each project's fused chunk list so one dominant
	// repo can't take every slot in the round-robin interleave below;
	// the projects panel sees every surviving project (its num_hits
	// reflects the post-cap count so the UI doesn't dangle a "10
	// hits" badge against a chunk list with 5 entries).
	for i := range surviving {
		if len(surviving[i].FusedChunks) > workspaceSearchPerProjChunkCap {
			surviving[i].FusedChunks = surviving[i].FusedChunks[:workspaceSearchPerProjChunkCap]
		}
	}

	projectPayloads := make([]workspaceSearchProjectPayload, 0, len(surviving))
	for _, ph := range surviving {
		projectPayloads = append(projectPayloads, workspaceSearchProjectPayload{
			ProjectPath:  ph.ProjectPath,
			Label:        projectLabel(ph.ProjectPath),
			ProjectScore: round4(ph.Candidacy),
			NumHits:      len(ph.FusedChunks),
			BM25Score:    round4(ph.BM25Signal),
			DenseScore:   round4(ph.DenseSignal),
		})
	}

	sort.SliceStable(projectPayloads, func(i, j int) bool {
		return projectPayloads[i].ProjectScore > projectPayloads[j].ProjectScore
	})
	if len(projectPayloads) > topProjects {
		projectPayloads = projectPayloads[:topProjects]
	}

	// Restrict the interleave to projects that survived the panel
	// truncation. Otherwise a workspace with > top_projects surviving
	// repos can surface chunks whose project_path is absent from
	// projects[] — agents lose access to bm25_score/dense_score and
	// the response looks inconsistent. Filter to the panel before
	// round-robin.
	panelSet := make(map[string]struct{}, len(projectPayloads))
	for _, p := range projectPayloads {
		panelSet[p.ProjectPath] = struct{}{}
	}
	panelSurviving := make([]projectHits, 0, len(projectPayloads))
	for _, ph := range surviving {
		if _, ok := panelSet[ph.ProjectPath]; ok {
			panelSurviving = append(panelSurviving, ph)
		}
	}

	// Round-robin across surviving projects so rank-1 from each
	// project lands in the first N slots, then rank-2, etc. This
	// gives every surviving repo a chance to surface its top chunk
	// before any repo's tail entries appear — matches the project-
	// picker use case where the user wants to see each project's
	// most-relevant hit before diving into the dominant repo's tail.
	merged := interleaveByRank(panelSurviving, topChunks)

	status := "ok"
	if len(merged) == 0 {
		status = "empty"
		if len(failedRepos) > 0 {
			status = "partial_failure"
		}
	}
	writeJSON(w, http.StatusOK, workspaceSearchResponse(
		status,
		projectPayloads,
		merged,
		pendingRepos,
		failedRepos,
		staleRepos,
	))
}

// interleaveByRank returns up to `limit` chunks by walking the surviving
// projects round-robin — rank-1 from every project before any rank-2,
// then rank-2, and so on. Projects are visited in candidacy-desc order
// so the strongest project still leads, but every other surviving
// project gets a chance to surface its top chunk before tail entries
// from the leader appear.
//
// Inside the same rank tier, dedupe keeps the natural workspace order
// so two chunks of identical content from different projects still
// both appear (with their respective project_path).
func interleaveByRank(projects []projectHits, limit int) []workspaceSearchChunkPayload {
	if limit <= 0 || len(projects) == 0 {
		return []workspaceSearchChunkPayload{}
	}
	ordered := make([]projectHits, len(projects))
	copy(ordered, projects)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Candidacy > ordered[j].Candidacy
	})

	out := make([]workspaceSearchChunkPayload, 0, limit)
	dedupKey := func(c workspaceSearchChunkPayload) string {
		return c.ProjectPath + "|" + c.FilePath + "|" +
			strconv.Itoa(c.StartLine) + "-" + strconv.Itoa(c.EndLine)
	}
	seen := make(map[string]struct{}, limit)
	// rank index walks 0,1,2,... ; we stop when no project has a
	// chunk at this rank (every list exhausted).
	for r := 0; ; r++ {
		progressed := false
		for _, p := range ordered {
			if r >= len(p.FusedChunks) {
				continue
			}
			c := p.FusedChunks[r]
			if c.ProjectPath == "" {
				c.ProjectPath = p.ProjectPath
			}
			k := dedupKey(c)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, c)
			progressed = true
			if len(out) >= limit {
				return out
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

// workspaceSearchResponse builds the final JSON payload. Single
// builder so the early-empty path and the happy path can't drift on
// which optional fields they include.
func workspaceSearchResponse(
	status string,
	projects []workspaceSearchProjectPayload,
	chunks []workspaceSearchChunkPayload,
	pending []workspaceSearchPendingRepoPayload,
	failed []workspaceSearchFailedRepoPayload,
	stale []workspaceSearchStaleFTSRepoPayload,
) map[string]any {
	out := map[string]any{
		"status":   status,
		"projects": projects,
		"chunks":   chunks,
	}
	if len(pending) > 0 {
		out["pending_repos"] = pending
	}
	if len(failed) > 0 {
		out["failed_repos"] = failed
	}
	if len(stale) > 0 {
		out["stale_fts_repos"] = stale
	}
	return out
}

// detectStaleFTSRepos returns the subset of projectPaths whose
// chunks_meta is empty but file_hashes has at least one row — meaning
// the project was indexed before the FTS5 mirror existed and needs a
// reindex before BM25 can contribute. A best-effort detector: if any
// SQL probe errors out we log + return nil rather than fail the
// request, since the warning is informational, not load-bearing.
func (s *Server) detectStaleFTSRepos(ctx context.Context, projectPaths []string) []workspaceSearchStaleFTSRepoPayload {
	out := make([]workspaceSearchStaleFTSRepoPayload, 0)
	for _, pp := range projectPaths {
		var nMeta, nFiles int
		if err := s.Deps.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM chunks_meta WHERE project_path = ? LIMIT 1`, pp).Scan(&nMeta); err != nil {
			s.Deps.Logger.Warn("workspaces search: stale-fts probe (chunks_meta)",
				"project_path", pp, "err", err)
			return nil
		}
		if nMeta > 0 {
			continue
		}
		if err := s.Deps.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_hashes WHERE project_path = ? LIMIT 1`, pp).Scan(&nFiles); err != nil {
			s.Deps.Logger.Warn("workspaces search: stale-fts probe (file_hashes)",
				"project_path", pp, "err", err)
			return nil
		}
		if nFiles > 0 {
			out = append(out, workspaceSearchStaleFTSRepoPayload{ProjectPath: pp})
		}
	}
	return out
}

// fanOutHybrid runs dense + BM25 in parallel per project, fuses each
// project's two ranked lists via RRF, and returns the per-project
// aggregates the candidacy step needs. Bounded by NumCPU goroutines
// across the workspace; each project is one slot regardless of
// whether it issues one or two sub-queries.
//
// Per-project failures: a BM25-side error is logged but does not mark
// the project as failed (FTS5 might not be populated yet for a
// pre-existing install; dense still works). A dense-side error is
// surfaced via failed_repos and dense_signal is left at 0 — the
// project can still be retained if BM25 alone is strong.
func (s *Server) fanOutHybrid(
	ctx context.Context,
	workspaceID string,
	projectPaths []string,
	rawQuery string,
	queryEmbedding []float32,
	minScore float32,
) ([]projectHits, []workspaceSearchFailedRepoPayload, error) {
	concurrency := runtime.NumCPU()
	if concurrency < 1 {
		concurrency = 1
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	results := make([]projectHits, len(projectPaths))
	failures := make([]workspaceSearchFailedRepoPayload, len(projectPaths))
	failed := make([]bool, len(projectPaths))
	var mu sync.Mutex

	for i, pp := range projectPaths {
		i, pp := i, pp
		g.Go(func() error {
			var (
				denseRes []workspaceSearchChunkPayload
				bm25Res  []workspaceSearchChunkPayload
				denseErr error
			)

			rawDense, derr := s.Deps.VectorStore.Search(gctx, pp, queryEmbedding, workspaceSearchPerProjectLimit, nil)
			if derr != nil {
				denseErr = derr
				s.Deps.Logger.Warn("workspaces search: dense query failed",
					"workspace_id", workspaceID,
					"project_path", pp,
					"err", derr)
			} else {
				denseRes = make([]workspaceSearchChunkPayload, 0, len(rawDense))
				for _, h := range rawDense {
					if h.Score < minScore {
						continue
					}
					denseRes = append(denseRes, workspaceSearchChunkPayload{
						ProjectPath: pp,
						FilePath:    h.FilePath,
						StartLine:   h.StartLine,
						EndLine:     h.EndLine,
						SymbolName:  h.SymbolName,
						Language:    h.Language,
						Score:       h.Score,
						Content:     h.Content,
					})
				}
			}

			rawBM25, berr := chunksfts.SearchProject(gctx, s.Deps.DB, pp, rawQuery, workspaceSearchBM25Limit)
			if berr != nil {
				s.Deps.Logger.Warn("workspaces search: bm25 query failed",
					"workspace_id", workspaceID,
					"project_path", pp,
					"err", berr)
			} else {
				bm25Res = make([]workspaceSearchChunkPayload, 0, len(rawBM25))
				for _, h := range rawBM25 {
					bm25Res = append(bm25Res, workspaceSearchChunkPayload{
						ProjectPath: pp,
						FilePath:    h.FilePath,
						StartLine:   h.StartLine,
						EndLine:     h.EndLine,
						SymbolName:  h.SymbolName,
						Language:    h.Language,
						// Score field carries the dense cosine for the
						// merged chunk; for BM25-only hits we leave it
						// at 0 (BM25 score is on a different scale and
						// would mislead a client reading "score" as
						// cosine).
						Score:   0,
						Content: h.Content,
					})
				}
			}

			fused := fuseRRF(denseRes, bm25Res)
			denseSig := meanTopN(denseScoresOf(denseRes), workspaceSearchTopNPerProject)
			bm25Sig := meanTopN(bm25ScoresOf(rawBM25), workspaceSearchTopNPerProject)

			mu.Lock()
			if denseErr != nil {
				failures[i] = workspaceSearchFailedRepoPayload{
					ProjectPath: pp,
					Reason:      "vectorstore_error",
				}
				failed[i] = true
			}
			results[i] = projectHits{
				ProjectPath: pp,
				FusedChunks: fused,
				DenseSignal: float32(denseSig),
				BM25Signal:  float32(bm25Sig),
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	failedOut := make([]workspaceSearchFailedRepoPayload, 0)
	for i, f := range failed {
		if f {
			failedOut = append(failedOut, failures[i])
		}
	}
	return results, failedOut, nil
}

// fuseRRF returns chunks ranked by Reciprocal Rank Fusion over the two
// per-project lists. RRF score per chunk is sum(1/(k+rank_i)) across
// the lists where it appears. Chunks present in both lists naturally
// bubble to the top; chunks unique to one list still score positively.
//
// Chunk identity is (project_path, file_path, start_line, end_line) —
// matching chunks across the two lists must be the same span. The
// dense-side payload is preferred when both exist (it carries the
// non-zero `score` field).
func fuseRRF(dense, bm25 []workspaceSearchChunkPayload) []workspaceSearchChunkPayload {
	type entry struct {
		c   workspaceSearchChunkPayload
		rrf float64
	}
	key := func(c workspaceSearchChunkPayload) string {
		return c.ProjectPath + "|" + c.FilePath + "|" +
			strconv.Itoa(c.StartLine) + "-" + strconv.Itoa(c.EndLine)
	}
	byKey := make(map[string]*entry)
	for rank, c := range dense {
		k := key(c)
		byKey[k] = &entry{c: c, rrf: 1.0 / float64(rrfK+rank+1)}
	}
	for rank, c := range bm25 {
		k := key(c)
		add := 1.0 / float64(rrfK+rank+1)
		if e, ok := byKey[k]; ok {
			e.rrf += add
			continue
		}
		byKey[k] = &entry{c: c, rrf: add}
	}
	out := make([]entry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, *e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].rrf > out[j].rrf
	})
	chunks := make([]workspaceSearchChunkPayload, len(out))
	for i, e := range out {
		chunks[i] = e.c
	}
	return chunks
}

func denseScoresOf(chunks []workspaceSearchChunkPayload) []float64 {
	out := make([]float64, len(chunks))
	for i, c := range chunks {
		out[i] = float64(c.Score)
	}
	return out
}

func bm25ScoresOf(hits []chunksfts.Hit) []float64 {
	out := make([]float64, len(hits))
	for i, h := range hits {
		out[i] = h.Score
	}
	return out
}

// meanTopN returns the arithmetic mean of the top-n values in xs.
// Returns 0 when xs is empty. xs is sorted in descending order
// in-place; callers must pass a slice they own.
func meanTopN(xs []float64, n int) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(xs)))
	if n > len(xs) {
		n = len(xs)
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += xs[i]
	}
	return sum / float64(n)
}

// projectLabel derives a short display label from a project_path.
// The path convention is `host/owner/repo@branch`; we strip everything
// up to the last `/` so the dashboard's "Top projects" panel shows
// compact, recognisable entries.
func projectLabel(projectPath string) string {
	for i := len(projectPath) - 1; i >= 0; i-- {
		if projectPath[i] == '/' {
			return projectPath[i+1:]
		}
	}
	return projectPath
}

// round4 rounds f to 4 decimal places — matches the chunk-side
// rounding chromem already applies, so scores in the response look
// consistent across nested fields.
func round4(f float32) float32 {
	if math.IsNaN(float64(f)) {
		return 0
	}
	const scale = 10000
	return float32(int(f*scale+0.5)) / scale
}

func clampInt(v *int, def, min, max int) int {
	if v == nil {
		return def
	}
	if *v < min {
		return min
	}
	if *v > max {
		return max
	}
	return *v
}

func clampFloat32(v *float32, def, min, max float32) float32 {
	if v == nil {
		return def
	}
	if *v < min {
		return min
	}
	if *v > max {
		return max
	}
	return *v
}
