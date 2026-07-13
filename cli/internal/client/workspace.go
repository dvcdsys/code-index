package client

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// WorkspaceSearchProject mirrors the OpenAPI WorkspaceSearchProject
// schema — one entry per surviving project in the hybrid candidacy
// ranking.
type WorkspaceSearchProject struct {
	ProjectPath  string  `json:"project_path"`
	Label        string  `json:"label"`
	ProjectScore float32 `json:"project_score"`
	NumHits      int     `json:"num_hits"`
	BM25Score    float32 `json:"bm25_score"`
	DenseScore   float32 `json:"dense_score"`
}

// WorkspaceSearchChunk mirrors WorkspaceSearchChunk.
type WorkspaceSearchChunk struct {
	ProjectPath string  `json:"project_path"`
	FilePath    string  `json:"file_path"`
	StartLine   int     `json:"start_line"`
	EndLine     int     `json:"end_line"`
	SymbolName  string  `json:"symbol_name,omitempty"`
	Language    string  `json:"language,omitempty"`
	Score       float32 `json:"score"`
	Content     string  `json:"content"`
}

// WorkspaceSearchStaleFTSRepo names a repo whose BM25 index hasn't
// been backfilled yet (indexed before chunks_fts existed); hybrid
// degrades to dense-only for that entry until reindex.
type WorkspaceSearchStaleFTSRepo struct {
	ProjectPath string `json:"project_path"`
}

// WorkspaceSearchResponse mirrors WorkspaceSearchResponse.
type WorkspaceSearchResponse struct {
	Status        string                        `json:"status"`
	Projects      []WorkspaceSearchProject      `json:"projects"`
	Chunks        []WorkspaceSearchChunk        `json:"chunks"`
	StaleFTSRepos []WorkspaceSearchStaleFTSRepo `json:"stale_fts_repos,omitempty"`
}

// Workspace is the metadata projection of a workspace row.
type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WorkspaceListResponse is the GET /workspaces shape.
type WorkspaceListResponse struct {
	Workspaces []Workspace `json:"workspaces"`
	Total      int         `json:"total"`
}

// WorkspaceProject mirrors the server's WorkspaceProject — the embedded
// project (full Project shape, defined in projects.go) plus the
// membership-added timestamp.
type WorkspaceProject struct {
	AddedAt time.Time `json:"added_at"`
	Project Project   `json:"project"`
}

// WorkspaceProjectListResponse is the GET /workspaces/{id}/projects shape.
type WorkspaceProjectListResponse struct {
	Projects []WorkspaceProject `json:"projects"`
	Total    int                `json:"total"`
}

// ListWorkspaces — GET /api/v1/workspaces. Returns
// ServiceUnavailable as a typed error so callers can render a hint when
// CIX_WORKSPACES_ENABLED is off on the server side.
func (c *Client) ListWorkspaces() (*WorkspaceListResponse, error) {
	resp, err := c.do("GET", "/api/v1/workspaces", nil)
	if err != nil {
		return nil, err
	}
	var out WorkspaceListResponse
	if err := parseResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListWorkspaceProjects — GET /api/v1/workspaces/{id}/projects. Returns
// every linked project with its current status (created / indexing /
// indexed / error), host path, path hash, and membership timestamp so
// the CLI can render a readable per-project summary without a second
// round-trip.
func (c *Client) ListWorkspaceProjects(workspaceID string) (*WorkspaceProjectListResponse, error) {
	resp, err := c.do("GET", "/api/v1/workspaces/"+url.PathEscape(workspaceID)+"/projects", nil)
	if err != nil {
		return nil, err
	}
	var out WorkspaceProjectListResponse
	if err := parseResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateWorkspace — POST /api/v1/workspaces. Creates a workspace owned by
// the caller. An empty description is omitted from the body. The server
// also stamps timestamps + owner_user_id on the returned payload, which the
// minimal Workspace struct simply ignores.
func (c *Client) CreateWorkspace(name, description string) (*Workspace, error) {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	resp, err := c.do("POST", "/api/v1/workspaces", body)
	if err != nil {
		return nil, err
	}
	var out Workspace
	if err := parseResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateWorkspace — PATCH /api/v1/workspaces/{id}. Both fields are optional:
// a nil pointer omits the field so the server leaves it unchanged; a non-nil
// value is sent verbatim (an empty description string clears it). Callers
// should pass at least one non-nil pointer or the request is a no-op.
func (c *Client) UpdateWorkspace(id string, name, description *string) (*Workspace, error) {
	body := map[string]any{}
	if name != nil {
		body["name"] = *name
	}
	if description != nil {
		body["description"] = *description
	}
	resp, err := c.do("PATCH", "/api/v1/workspaces/"+url.PathEscape(id), body)
	if err != nil {
		return nil, err
	}
	var out Workspace
	if err := parseResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWorkspace — DELETE /api/v1/workspaces/{id}. Removes the workspace and
// its project-membership rows; the projects themselves are untouched.
// Returns nil on 204 and a formatted error otherwise (including 404 when the
// workspace is already gone).
func (c *Client) DeleteWorkspace(id string) error {
	resp, err := c.do("DELETE", "/api/v1/workspaces/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// LinkProjectToWorkspace — POST /api/v1/workspaces/{id}/projects. Adds an
// already-indexed project (addressed by its 16-hex path_hash) to the
// workspace. The server rejects not-yet-indexed projects (422) and
// duplicates (409); both surface as formatted errors from parseResponse.
func (c *Client) LinkProjectToWorkspace(id, projectHash string) error {
	body := map[string]string{"project_hash": projectHash}
	resp, err := c.do("POST", "/api/v1/workspaces/"+url.PathEscape(id)+"/projects", body)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// UnlinkProjectFromWorkspace — DELETE
// /api/v1/workspaces/{id}/projects/{hash}. Removes the membership row; the
// project itself is untouched. A 404 (unknown project, or not linked to this
// workspace) surfaces as a formatted error.
func (c *Client) UnlinkProjectFromWorkspace(id, projectHash string) error {
	resp, err := c.do("DELETE", "/api/v1/workspaces/"+url.PathEscape(id)+"/projects/"+url.PathEscape(projectHash), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// WorkspaceSearch — GET /api/v1/workspaces/{id}/search. id is the
// workspace's opaque ULID/UUID returned by ListWorkspaces. minScore is
// optional: nil omits the parameter so the server applies its default
// (0.4); a non-nil value (including 0 for an intentional broad sweep) is
// sent verbatim.
func (c *Client) WorkspaceSearch(id, query string, topProjects, topChunks int, minScore *float64) (*WorkspaceSearchResponse, error) {
	v := url.Values{}
	v.Set("q", query)
	if topProjects > 0 {
		v.Set("top_projects", fmt.Sprintf("%d", topProjects))
	}
	if topChunks > 0 {
		v.Set("top_chunks", fmt.Sprintf("%d", topChunks))
	}
	if minScore != nil {
		v.Set("min_score", strconv.FormatFloat(*minScore, 'f', -1, 64))
	}
	path := "/api/v1/workspaces/" + url.PathEscape(id) + "/search?" + v.Encode()
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var out WorkspaceSearchResponse
	if err := parseResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
