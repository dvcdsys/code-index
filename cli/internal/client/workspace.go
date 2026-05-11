package client

import (
	"fmt"
	"net/url"
)

// WorkspaceSearchCommunity mirrors the OpenAPI WorkspaceSearchCommunity schema.
type WorkspaceSearchCommunity struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Score        float32  `json:"score"`
	ProjectPaths []string `json:"project_paths"`
	MemberCount  int      `json:"member_count"`
}

// WorkspaceSearchChunk mirrors WorkspaceSearchChunk.
type WorkspaceSearchChunk struct {
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

// WorkspaceSearchResponse mirrors WorkspaceSearchResponse.
type WorkspaceSearchResponse struct {
	Status      string                     `json:"status"`
	Communities []WorkspaceSearchCommunity `json:"communities"`
	Chunks      []WorkspaceSearchChunk     `json:"chunks"`
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

// WorkspaceSearch — GET /api/v1/workspaces/{id}/search. id is the
// workspace's opaque ULID/UUID returned by ListWorkspaces.
func (c *Client) WorkspaceSearch(id, query string, topCommunities, topChunks int) (*WorkspaceSearchResponse, error) {
	v := url.Values{}
	v.Set("q", query)
	if topCommunities > 0 {
		v.Set("top_communities", fmt.Sprintf("%d", topCommunities))
	}
	if topChunks > 0 {
		v.Set("top_chunks", fmt.Sprintf("%d", topChunks))
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
