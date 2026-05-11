package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// workspaceSearchCommunityPayload mirrors WorkspaceSearchCommunity from
// the OpenAPI spec. Hand-rolled (vs the generated type) so we can keep
// project_paths as a real []string instead of a generated alias.
type workspaceSearchCommunityPayload struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Score        float32  `json:"score"`
	ProjectPaths []string `json:"project_paths"`
	MemberCount  int      `json:"member_count"`
}

type workspaceSearchChunkPayload struct {
	ProjectPath    string  `json:"project_path"`
	FilePath       string  `json:"file_path"`
	StartLine      int     `json:"start_line"`
	EndLine        int     `json:"end_line"`
	SymbolName     string  `json:"symbol_name,omitempty"`
	Language       string  `json:"language,omitempty"`
	Score          float32 `json:"score"`
	CommunityID    string  `json:"community_id"`
	CommunityLabel string  `json:"community_label,omitempty"`
	Content        string  `json:"content"`
}

// WorkspaceSearch — GET /api/v1/workspaces/{id}/search.
//
// Two-stage search:
//   - Stage 1: embed the query, hit the workspace's centroid collection,
//     keep top_communities best.
//   - Stage 2: for each (community, project_path), fetch the chunks
//     whose symbol_name is in that community's members from the
//     per-project chromem collection. Merge globally by similarity to
//     the query and return top_chunks.
//
// "Stage 2 fan-out" is bounded by top_communities × #project_paths per
// community. In practice that's ≤ 5 × 3 ≈ 15 chromem queries per
// workspace search — typical p50 well under 500ms even on a cold cache.
func (s *Server) WorkspaceSearch(w http.ResponseWriter, r *http.Request, id string, params openapi.WorkspaceSearchParams) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if s.Deps.VectorStore == nil || s.Deps.EmbeddingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "embeddings or vectorstore not configured — workspace search requires both")
		return
	}
	// Workspace existence check — leaks fewer signals than a stage-1
	// query against a non-existent collection.
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
	topCommunities := 5
	if params.TopCommunities != nil {
		topCommunities = *params.TopCommunities
	}
	topChunks := 20
	if params.TopChunks != nil {
		topChunks = *params.TopChunks
	}

	// --- Stage 1 ---
	queryEmbedding, err := s.Deps.EmbeddingSvc.EmbedQuery(r.Context(), params.Q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not embed query: "+err.Error())
		return
	}
	communityHits, err := s.Deps.VectorStore.SearchCentroids(r.Context(), id, queryEmbedding, topCommunities)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "centroid search failed: "+err.Error())
		return
	}
	if len(communityHits) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "communities_not_built",
			"communities": []workspaceSearchCommunityPayload{},
			"chunks":      []workspaceSearchChunkPayload{},
		})
		return
	}

	// --- Stage 2 — load member symbol names per (community, project) ---
	membersByCommProject, err := loadCommunityMembers(r.Context(), s.Deps.DB, communityHits)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load community members: "+err.Error())
		return
	}

	// Fan out per (community, project) to the per-project chromem
	// collection. For each combo we query with the user's embedding +
	// pull a generous limit, then filter to chunks whose symbol_name is
	// in the community's member set. This avoids the chromem
	// single-equality `where` limitation while keeping fanout bounded.
	const overFetchMultiplier = 4
	perQueryLimit := topChunks * overFetchMultiplier
	if perQueryLimit < 50 {
		perQueryLimit = 50
	}

	communityByID := map[string]vectorstore.CentroidResult{}
	communityPayloads := make([]workspaceSearchCommunityPayload, 0, len(communityHits))
	for _, c := range communityHits {
		communityByID[c.CommunityID] = c
		communityPayloads = append(communityPayloads, workspaceSearchCommunityPayload{
			ID:           c.CommunityID,
			Label:        c.Label,
			Score:        c.Score,
			ProjectPaths: c.ProjectPaths,
			MemberCount:  c.MemberCount,
		})
	}

	allChunks := make([]workspaceSearchChunkPayload, 0, topChunks*2)
	for commID, byProject := range membersByCommProject {
		comm := communityByID[commID]
		for projectPath, nameSet := range byProject {
			results, qerr := s.Deps.VectorStore.Search(r.Context(), projectPath, queryEmbedding, perQueryLimit, nil)
			if qerr != nil {
				// Swallow per-project failures — return partial results
				// rather than failing the whole search.
				s.Deps.Logger.Warn("workspaces search: per-project query failed",
					"workspace_id", id,
					"project_path", projectPath,
					"err", qerr)
				continue
			}
			for _, res := range results {
				if _, ok := nameSet[res.SymbolName]; !ok {
					continue
				}
				allChunks = append(allChunks, workspaceSearchChunkPayload{
					ProjectPath:    projectPath,
					FilePath:       res.FilePath,
					StartLine:      res.StartLine,
					EndLine:        res.EndLine,
					SymbolName:     res.SymbolName,
					Language:       res.Language,
					Score:          res.Score,
					CommunityID:    commID,
					CommunityLabel: comm.Label,
					Content:        res.Content,
				})
			}
		}
	}

	// --- Merge + global top-K ---
	sort.SliceStable(allChunks, func(i, j int) bool {
		return allChunks[i].Score > allChunks[j].Score
	})
	dedupKey := func(c workspaceSearchChunkPayload) string {
		return c.ProjectPath + "|" + c.FilePath + "|" +
			itoa(c.StartLine) + "-" + itoa(c.EndLine)
	}
	seen := map[string]struct{}{}
	merged := make([]workspaceSearchChunkPayload, 0, topChunks)
	for _, c := range allChunks {
		k := dedupKey(c)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		merged = append(merged, c)
		if len(merged) >= topChunks {
			break
		}
	}

	status := "ok"
	if len(merged) == 0 {
		status = "empty"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      status,
		"communities": communityPayloads,
		"chunks":      merged,
	})
}

// loadCommunityMembers fetches every (project_path, symbol_name) for the
// communities returned by stage 1, grouped by (community_id, project_path)
// for fast membership lookup during stage 2.
//
// One SQL query joins community_members → symbols across all selected
// communities; modernc.org/sqlite handles the IN() clause cleanly at
// these sizes (typical: ≤5 communities × ≤200 members each).
func loadCommunityMembers(
	ctx context.Context,
	db *sql.DB,
	communities []vectorstore.CentroidResult,
) (map[string]map[string]map[string]struct{}, error) {
	if len(communities) == 0 {
		return nil, nil
	}
	ids := make([]any, 0, len(communities))
	for _, c := range communities {
		ids = append(ids, c.CommunityID)
	}
	placeholders := "?" + repeat(",?", len(communities)-1)
	rows, err := db.QueryContext(ctx, `
		SELECT cm.community_id, cm.project_path, s.name
		  FROM community_members cm
		  JOIN symbols s ON s.id = cm.symbol_id AND s.project_path = cm.project_path
		 WHERE cm.community_id IN (`+placeholders+`)`,
		ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]map[string]struct{}{}
	for rows.Next() {
		var commID, projectPath, name string
		if err := rows.Scan(&commID, &projectPath, &name); err != nil {
			return nil, err
		}
		if out[commID] == nil {
			out[commID] = map[string]map[string]struct{}{}
		}
		if out[commID][projectPath] == nil {
			out[commID][projectPath] = map[string]struct{}{}
		}
		out[commID][projectPath][name] = struct{}{}
	}
	return out, rows.Err()
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func itoa(n int) string {
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
