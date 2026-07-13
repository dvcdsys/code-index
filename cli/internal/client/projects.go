package client

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrProjectGone reports that the server already had no such project (404).
// The delete is a server-side no-op, so idempotent callers can treat it as
// success and proceed with local cleanup.
var ErrProjectGone = errors.New("project not found on server")

// Project represents a code project
type Project struct {
	PathHash         string          `json:"path_hash"`
	HostPath         string          `json:"host_path"`
	ContainerPath    string          `json:"container_path"`
	Languages        []string        `json:"languages"`
	Settings         ProjectSettings `json:"settings"`
	Stats            ProjectStats    `json:"stats"`
	Status           string          `json:"status"` // created, indexing, indexed, error
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	LastIndexedAt    *time.Time      `json:"last_indexed_at"`
	IndexedWithModel *string         `json:"indexed_with_model,omitempty"`
	SqlitePath       *string         `json:"sqlite_path,omitempty"`
	SqliteSizeBytes  *int64          `json:"sqlite_size_bytes,omitempty"`
	ChromaPath       *string         `json:"chroma_path,omitempty"`
	ChromaSizeBytes  *int64          `json:"chroma_size_bytes,omitempty"`
}

type ProjectSettings struct {
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize     int      `json:"max_file_size"`
}

type ProjectStats struct {
	TotalFiles   int `json:"total_files"`
	IndexedFiles int `json:"indexed_files"`
	TotalChunks  int `json:"total_chunks"`
	TotalSymbols int `json:"total_symbols"`
}

// ListProjects lists all projects
func (c *Client) ListProjects() ([]Project, error) {
	resp, err := c.do("GET", "/api/v1/projects", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Projects []Project `json:"projects"`
		Total    int       `json:"total"`
	}

	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Projects, nil
}

// GetProject gets a specific project
func (c *Client) GetProject(path string) (*Project, error) {
	encodedPath := encodeProjectPath(path)
	resp, err := c.do("GET", fmt.Sprintf("/api/v1/projects/%s", encodedPath), nil)
	if err != nil {
		return nil, err
	}

	var project Project
	if err := parseResponse(resp, &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// CreateProject creates a new project
func (c *Client) CreateProject(path string) (*Project, error) {
	// machine_id namespaces the project's identity so the same filesystem path
	// on a different machine/user is a distinct project; machine_label is the
	// hostname for display. The server derives path_hash from
	// local:{machine_id}:{host_path} — matching encodeProjectPath.
	body := map[string]string{
		"host_path":     path,
		"machine_id":    machineID(),
		"machine_label": machineLabel(),
	}

	resp, err := c.do("POST", "/api/v1/projects", body)
	if err != nil {
		return nil, err
	}

	var project Project
	if err := parseResponse(resp, &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// DeleteProject deletes a project and its server-side index (symbols, refs,
// embeddings). Requires the project owner or an admin. A 404 is reported as
// ErrProjectGone so callers can treat an already-deleted project as success.
func (c *Client) DeleteProject(path string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/v1/projects/%s", encodeProjectPath(path)), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrProjectGone
	default:
		return parseResponse(resp, nil)
	}
}
