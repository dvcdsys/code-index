package client

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New creates a new API client
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 600 * time.Second,
		},
	}
}

// do performs an HTTP request with auth
func (c *Client) do(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	return resp, nil
}

// encodeProjectPath returns SHA1 hash (first 16 hex chars) of the project path.
// This avoids all URL encoding issues with slashes in paths.
func encodeProjectPath(path string) string {
	h := sha1.Sum([]byte(path))
	return fmt.Sprintf("%x", h)[:16]
}

// parseResponse reads and unmarshals JSON response
func parseResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Detail != "" {
			return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Detail)
		}
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

// Health checks if the API server is running
func (c *Client) Health() error {
	resp, err := c.httpClient.Get(c.baseURL + "/health")
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}

	return nil
}

// Status gets the API server status
func (c *Client) Status() (*StatusResponse, error) {
	resp, err := c.do("GET", "/api/v1/status", nil)
	if err != nil {
		return nil, err
	}

	var status StatusResponse
	if err := parseResponse(resp, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

type StatusResponse struct {
	Status             string `json:"status"`
	ModelLoaded        bool   `json:"model_loaded"`
	Projects           int    `json:"projects"`
	ActiveIndexingJobs int    `json:"active_indexing_jobs"`
}
