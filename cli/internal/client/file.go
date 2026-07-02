package client

import "fmt"

// FileContent mirrors the server's FileContent schema: a file's contents
// (whole or a line range) read from an external project's on-disk checkout.
type FileContent struct {
	FilePath   string  `json:"file_path"`
	Language   *string `json:"language,omitempty"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	TotalLines int     `json:"total_lines"`
	Truncated  bool    `json:"truncated"`
	Content    string  `json:"content"`
}

// TreeEntry is one directory entry in a DirectoryListing.
type TreeEntry struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // "file" | "dir"
	Size     *int    `json:"size,omitempty"`
	Language *string `json:"language,omitempty"`
}

// DirectoryListing mirrors the server's DirectoryListing schema (one level).
type DirectoryListing struct {
	Dir       string      `json:"dir"`
	Entries   []TreeEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// ReadFile reads a file from an external project's server-side checkout. start
// and end are 1-based inclusive line numbers; pass 0 for either to read from
// the start / to the end of the file.
func (c *Client) ReadFile(projectPath, file string, start, end int) (*FileContent, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{"file": file}
	if start > 0 {
		body["start"] = start
	}
	if end > 0 {
		body["end"] = end
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/file", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result FileContent
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListDir lists one level of a directory in an external project's server-side
// checkout. Pass an empty dir for the repository root.
func (c *Client) ListDir(projectPath, dir string) (*DirectoryListing, error) {
	encodedPath := encodeProjectPath(projectPath)

	body := map[string]interface{}{}
	if dir != "" {
		body["dir"] = dir
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/tree", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result DirectoryListing
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
