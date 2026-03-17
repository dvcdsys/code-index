package client

import "fmt"

// --- Three-phase indexing protocol types ---

// BeginIndexResponse is the response from POST /index/begin
type BeginIndexResponse struct {
	RunID        string            `json:"run_id"`
	StoredHashes map[string]string `json:"stored_hashes"` // path -> sha256
}

// FilePayload represents a file to be indexed
type FilePayload struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	Language    string `json:"language,omitempty"`
	Size        int    `json:"size"`
}

// SendFilesResponse is the response from POST /index/files
type SendFilesResponse struct {
	FilesAccepted       int `json:"files_accepted"`
	ChunksCreated       int `json:"chunks_created"`
	FilesProcessedTotal int `json:"files_processed_total"`
}

// FinishIndexResponse is the response from POST /index/finish
type FinishIndexResponse struct {
	Status         string `json:"status"`
	FilesProcessed int    `json:"files_processed"`
	ChunksCreated  int    `json:"chunks_created"`
}

// BeginIndex starts a new indexing session and returns stored hashes for diffing.
func (c *Client) BeginIndex(path string, full bool) (*BeginIndexResponse, error) {
	encodedPath := encodeProjectPath(path)
	body := map[string]interface{}{
		"full": full,
	}
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/begin", encodedPath), body)
	if err != nil {
		return nil, err
	}
	var result BeginIndexResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendFiles sends a batch of files to be indexed in the given run.
func (c *Client) SendFiles(path string, runID string, files []FilePayload) (*SendFilesResponse, error) {
	encodedPath := encodeProjectPath(path)
	body := map[string]interface{}{
		"run_id": runID,
		"files":  files,
	}
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/files", encodedPath), body)
	if err != nil {
		return nil, err
	}
	var result SendFilesResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// FinishIndex completes the indexing session, removing deleted files.
func (c *Client) FinishIndex(path string, runID string, deletedPaths []string, totalFiles int) (*FinishIndexResponse, error) {
	encodedPath := encodeProjectPath(path)
	body := map[string]interface{}{
		"run_id":                 runID,
		"deleted_paths":          deletedPaths,
		"total_files_discovered": totalFiles,
	}
	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/finish", encodedPath), body)
	if err != nil {
		return nil, err
	}
	var result FinishIndexResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// IndexTriggerResponse represents the response from triggering indexing
type IndexTriggerResponse struct {
	RunID   string `json:"run_id"`
	Message string `json:"message"`
}

// IndexProgress represents indexing progress
type IndexProgress struct {
	Status   string                 `json:"status"`
	Progress map[string]interface{} `json:"progress,omitempty"`
}

// TriggerIndex triggers project indexing
func (c *Client) TriggerIndex(path string, full bool) (*IndexTriggerResponse, error) {
	return c.TriggerIndexWithBatch(path, full, 0)
}

// TriggerIndexWithBatch triggers project indexing with a custom batch size.
// batch_size=0 means use server default.
func (c *Client) TriggerIndexWithBatch(path string, full bool, batchSize int) (*IndexTriggerResponse, error) {
	encodedPath := encodeProjectPath(path)

	body := map[string]interface{}{
		"full": full,
	}
	if batchSize > 0 {
		body["batch_size"] = batchSize
	}

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index", encodedPath), body)
	if err != nil {
		return nil, err
	}

	var result IndexTriggerResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// GetIndexStatus gets indexing status for a project
func (c *Client) GetIndexStatus(path string) (*IndexProgress, error) {
	encodedPath := encodeProjectPath(path)

	resp, err := c.do("GET", fmt.Sprintf("/api/v1/projects/%s/index/status", encodedPath), nil)
	if err != nil {
		return nil, err
	}

	var progress IndexProgress
	if err := parseResponse(resp, &progress); err != nil {
		return nil, err
	}

	return &progress, nil
}

// CancelIndex cancels ongoing indexing
func (c *Client) CancelIndex(path string) error {
	encodedPath := encodeProjectPath(path)

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/cancel", encodedPath), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result map[string]string
	if err := parseResponse(resp, &result); err != nil {
		return err
	}

	return nil
}
