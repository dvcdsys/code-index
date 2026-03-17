package client

import (
	"fmt"
	"time"
)

// Project represents a code project
type Project struct {
	HostPath      string            `json:"host_path"`
	ContainerPath string            `json:"container_path"`
	Languages     []string          `json:"languages"`
	Settings      ProjectSettings   `json:"settings"`
	Stats         ProjectStats      `json:"stats"`
	Status        string            `json:"status"` // created, indexing, indexed, error
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	LastIndexedAt *time.Time        `json:"last_indexed_at"`
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
	body := map[string]string{
		"host_path": path,
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

// DeleteProject deletes a project
func (c *Client) DeleteProject(path string) error {
	encodedPath := encodeProjectPath(path)
	resp, err := c.do("DELETE", fmt.Sprintf("/api/v1/projects/%s", encodedPath), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}
