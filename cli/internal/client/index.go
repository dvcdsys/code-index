package client

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// maxSendRetries is the number of times SendFiles will retry on HTTP 503/429.
const maxSendRetries = 8

// sendRetryDelay returns the sleep duration before the given retry attempt
// using exponential backoff (2s, 4s, 8s … capped at 60s) plus random jitter.
func sendRetryDelay(attempt int) time.Duration {
	secs := 1 << uint(attempt+1) // 2, 4, 8, 16, 32, 64 …
	if secs > 60 {
		secs = 60
	}
	// ±1 s jitter to avoid thundering herd when many watchers hit 503 at once
	jitter := rand.Intn(2000) - 1000 // -1000 … +999 ms
	d := time.Duration(secs)*time.Second + time.Duration(jitter)*time.Millisecond
	if d < time.Second {
		d = time.Second
	}
	return d
}

// retryAfterDelay parses the Retry-After header value (integer seconds) and
// returns it as a duration. Falls back to defaultDelay if parsing fails or the
// header is absent. Always adds a small random jitter to spread retries.
func retryAfterDelay(header string, defaultDelay time.Duration) time.Duration {
	if header == "" {
		return defaultDelay
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs <= 0 {
		return defaultDelay
	}
	jitter := rand.Intn(2000) // 0–2000 ms
	d := time.Duration(secs)*time.Second + time.Duration(jitter)*time.Millisecond
	if d > 120*time.Second {
		d = 120 * time.Second
	}
	return d
}

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
// On HTTP 503 (GPU busy) or 429 (rate limited) it retries with exponential
// backoff up to maxSendRetries times before giving up.
func (c *Client) SendFiles(path string, runID string, files []FilePayload) (*SendFilesResponse, error) {
	encodedPath := encodeProjectPath(path)
	body := map[string]interface{}{
		"run_id": runID,
		"files":  files,
	}

	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/files", encodedPath), body)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusTooManyRequests {
			header := resp.Header.Get("Retry-After")
			resp.Body.Close()
			delay := retryAfterDelay(header, sendRetryDelay(attempt))
			fmt.Printf("  GPU busy — retrying in %s (attempt %d/%d)...\n",
				delay.Round(time.Second), attempt+1, maxSendRetries)
			time.Sleep(delay)
			continue
		}

		var result SendFilesResponse
		if err := parseResponse(resp, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}

	return nil, fmt.Errorf("GPU still busy after %d retries — try again later", maxSendRetries)
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

// IndexProgress represents indexing progress.
// Returned by GetIndexStatus / GET /api/v1/projects/{path}/index/status.
type IndexProgress struct {
	Status   string         `json:"status"`
	Progress map[string]any `json:"progress,omitempty"`
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

// CancelIndexResponse matches the server's idempotent cancel reply.
type CancelIndexResponse struct {
	Cancelled bool `json:"cancelled"`
}

// CancelIndex terminates any in-flight indexing session for the given
// project. Idempotent: succeeds with Cancelled=false when no session exists.
// The watcher calls this at startup as a stale-session guard.
func (c *Client) CancelIndex(path string) (*CancelIndexResponse, error) {
	encodedPath := encodeProjectPath(path)

	resp, err := c.do("POST", fmt.Sprintf("/api/v1/projects/%s/index/cancel", encodedPath), nil)
	if err != nil {
		return nil, err
	}

	var result CancelIndexResponse
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
