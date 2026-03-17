package client

import "fmt"

// SearchResult represents a code search result
type SearchResult struct {
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
	ChunkType  string  `json:"chunk_type"`
	SymbolName string  `json:"symbol_name"`
	Language   string  `json:"language"`
}

// SearchResponse represents the search response
type SearchResponse struct {
	Results      []SearchResult `json:"results"`
	Total        int            `json:"total"`
	QueryTimeMS  float64        `json:"query_time_ms"`
}

// SymbolResult represents a symbol search result
type SymbolResult struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"` // function, class, method, type
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	Language   string  `json:"language"`
	Signature  *string `json:"signature,omitempty"`
	ParentName *string `json:"parent_name,omitempty"`
}

// SymbolSearchResponse represents symbol search response
type SymbolSearchResponse struct {
	Results []SymbolResult `json:"results"`
	Total   int            `json:"total"`
}

// SearchOptions contains options for code search
type SearchOptions struct {
	Limit     int      `json:"limit"`
	Languages []string `json:"languages,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	MinScore  float64  `json:"min_score,omitempty"`
}

// Search performs semantic code search
func (c *Client) Search(projectPath, query string, opts SearchOptions) (*SearchResponse, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{
		"query": query,
		"limit": opts.Limit,
	}

	if len(opts.Languages) > 0 {
		body["languages"] = opts.Languages
	}
	if len(opts.Paths) > 0 {
		body["paths"] = opts.Paths
	}
	if opts.MinScore > 0 {
		body["min_score"] = opts.MinScore
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/search", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result SearchResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// SearchSymbols searches for symbols by name
func (c *Client) SearchSymbols(projectPath, query string, kinds []string, limit int) (*SymbolSearchResponse, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{
		"query": query,
		"limit": limit,
	}

	if len(kinds) > 0 {
		body["kinds"] = kinds
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/search/symbols", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result SymbolSearchResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ProjectSummary represents project summary information
type ProjectSummary struct {
	HostPath       string                   `json:"host_path"`
	Status         string                   `json:"status"`
	Languages      []string                 `json:"languages"`
	TotalFiles     int                      `json:"total_files"`
	TotalChunks    int                      `json:"total_chunks"`
	TotalSymbols   int                      `json:"total_symbols"`
	TopDirectories []map[string]interface{} `json:"top_directories"`
	RecentSymbols  []map[string]interface{} `json:"recent_symbols"`
}

// FileResult represents a file search result
type FileResult struct {
	Path     string `json:"path"`
	Language string `json:"language"`
}

// FileSearchResponse represents file search response
type FileSearchResponse struct {
	Files []FileResult `json:"files"`
	Total int          `json:"total"`
}

// SearchFiles searches for files by path pattern
func (c *Client) SearchFiles(projectPath, pattern string, limit int) (*FileSearchResponse, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{
		"query": pattern,
		"limit": limit,
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/search/files", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result FileSearchResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// GetSummary gets project summary
func (c *Client) GetSummary(projectPath string) (*ProjectSummary, error) {
	encodedPath := encodeProjectPath(projectPath)

	resp, err := c.do("GET", fmt.Sprintf("/api/v1/projects/%s/summary", encodedPath), nil)
	if err != nil {
		return nil, err
	}

	var summary ProjectSummary
	if err := parseResponse(resp, &summary); err != nil {
		return nil, err
	}

	return &summary, nil
}

// DefinitionResult represents a definition search result
type DefinitionResult struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line"`
	Language   string  `json:"language"`
	Signature  *string `json:"signature,omitempty"`
	ParentName *string `json:"parent_name,omitempty"`
}

// DefinitionResponse represents definition search response
type DefinitionResponse struct {
	Results []DefinitionResult `json:"results"`
	Total   int                `json:"total"`
}

// ReferenceResult represents a reference search result
type ReferenceResult struct {
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Content    string `json:"content"`
	ChunkType  string `json:"chunk_type"`
	SymbolName string `json:"symbol_name"`
	Language   string `json:"language"`
}

// ReferenceResponse represents reference search response
type ReferenceResponse struct {
	Results []ReferenceResult `json:"results"`
	Total   int               `json:"total"`
}

// SearchDefinitions finds where a symbol is defined
func (c *Client) SearchDefinitions(projectPath, symbol string, kind string, filePath string, limit int) (*DefinitionResponse, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{
		"symbol": symbol,
		"limit":  limit,
	}
	if kind != "" {
		body["kind"] = kind
	}
	if filePath != "" {
		body["file_path"] = filePath
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/search/definitions", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result DefinitionResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// SearchReferences finds where a symbol is used
func (c *Client) SearchReferences(projectPath, symbol string, filePath string, limit int) (*ReferenceResponse, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{
		"symbol": symbol,
		"limit":  limit,
	}
	if filePath != "" {
		body["file_path"] = filePath
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/search/references", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result ReferenceResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
