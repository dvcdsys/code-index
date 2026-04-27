package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
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

// SendFiles sends a batch of files to be indexed. It is now a thin wrapper
// over SendFilesStreaming with a no-op event callback and a background
// context — kept for tests and for callers that don't want progress events.
//
// Note: even though the response is streamed under the hood, this wrapper
// blocks until the server closes the stream and returns only the final
// summary, matching the pre-streaming public surface.
func (c *Client) SendFiles(path string, runID string, files []FilePayload) (*SendFilesResponse, error) {
	return c.SendFilesStreaming(context.Background(), path, runID, files, nil)
}

// SendFilesStreaming sends a batch of files and streams NDJSON progress
// events from the server. The onEvent callback is invoked for every event;
// pass nil if you only want the final summary.
//
// On HTTP 503 (GPU busy) or 429 (rate limited) the request is retried with
// exponential backoff up to maxSendRetries times BEFORE the stream begins.
// Once the stream has started (i.e. the server responded with NDJSON), the
// caller is in a long-lived single attempt — failures during the stream
// surface to the caller without a retry.
//
// Returns ErrLegacyServer if the server doesn't speak NDJSON (Content-Type
// negotiation failed). The CLI surfaces this as "upgrade your server".
//
// Returns ErrIdleTimeout if no data arrives for streamingIdleTimeout — the
// connection is forcibly closed and the caller should treat the run as
// failed (the server will see ctx cancellation and free the session lock).
func (c *Client) SendFilesStreaming(
	ctx context.Context,
	path string,
	runID string,
	files []FilePayload,
	onEvent func(ProgressEvent),
) (*SendFilesResponse, error) {
	encodedPath := encodeProjectPath(path)
	url := c.baseURL + fmt.Sprintf("/api/v1/projects/%s/index/files", encodedPath)

	body := map[string]interface{}{
		"run_id": runID,
		"files":  files,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		// Wrap caller ctx so the idle watchdog can cancel without touching
		// the original. callerErr() distinguishes "caller cancelled us"
		// from "watchdog cancelled us" when reporting errors.
		streamCtx, streamCancel := context.WithCancel(ctx)

		req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			streamCancel()
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/x-ndjson")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		resp, err := c.streamingClient.Do(req)
		if err != nil {
			streamCancel()
			return nil, fmt.Errorf("do request: %w", err)
		}

		// Retryable backpressure responses — short body, no streaming begun.
		if resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusTooManyRequests {
			header := resp.Header.Get("Retry-After")
			resp.Body.Close()
			streamCancel()
			delay := retryAfterDelay(header, sendRetryDelay(attempt))
			fmt.Printf("  GPU busy — retrying in %s (attempt %d/%d)...\n",
				delay.Round(time.Second), attempt+1, maxSendRetries)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		// Any non-200 here is a hard error (bad run_id, project missing, …).
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			defer streamCancel()
			respBody, _ := io.ReadAll(resp.Body)
			var errResp struct {
				Detail string `json:"detail"`
			}
			if json.Unmarshal(respBody, &errResp) == nil && errResp.Detail != "" {
				return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Detail)
			}
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
		}

		// Hard fail if the server returned plain JSON (legacy build) instead
		// of NDJSON. We deliberately do not attempt a fallback parse — the
		// operator is expected to upgrade the server first.
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/x-ndjson") {
			resp.Body.Close()
			streamCancel()
			return nil, ErrLegacyServer
		}

		// At this point we have an open NDJSON stream. The retry loop ends.
		result, err := readStream(streamCtx, streamCancel, resp.Body, onEvent, c.streamingIdleTimeout, ctx)
		streamCancel()
		return result, err
	}

	return nil, fmt.Errorf("GPU still busy after %d retries — try again later", maxSendRetries)
}

// readStream consumes NDJSON lines from body, invokes onEvent for each, and
// returns the SendFilesResponse harvested from the terminal batch_done event.
// streamCancel is called whenever readStream wants to abort the connection
// (idle timeout, decode error, fatal server event).
func readStream(
	streamCtx context.Context,
	streamCancel context.CancelFunc,
	body io.ReadCloser,
	onEvent func(ProgressEvent),
	idleTimeout time.Duration,
	callerCtx context.Context,
) (*SendFilesResponse, error) {
	defer body.Close()

	// Idle watchdog — fires streamCancel if no line arrives for idleTimeout.
	// idleTimeout=0 disables the watchdog (used by tests when convenient).
	lineRead := make(chan struct{}, 1)
	if idleTimeout > 0 {
		go func() {
			timer := time.NewTimer(idleTimeout)
			defer timer.Stop()
			for {
				select {
				case <-lineRead:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(idleTimeout)
				case <-timer.C:
					streamCancel()
					return
				case <-streamCtx.Done():
					return
				}
			}
		}()
	}

	scanner := bufio.NewScanner(body)
	// Some chunks may be very large (long file paths or error messages);
	// give the scanner room. 1 MiB max-line should cover anything realistic.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var final *SendFilesResponse
	var fatalErr error

	for scanner.Scan() {
		// Notify watchdog: line arrived, reset idle timer.
		select {
		case lineRead <- struct{}{}:
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev ProgressEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("decode ndjson line: %w (line=%q)", err, line)
		}

		if onEvent != nil {
			onEvent(ev)
		}

		switch ev.Event {
		case EventBatchDone:
			// Don't return yet — there may be a trailing newline. The
			// scanner.Scan() loop will exit naturally on EOF.
			final = &SendFilesResponse{
				FilesAccepted:       ev.FilesAccepted,
				ChunksCreated:       ev.ChunksCreated,
				FilesProcessedTotal: ev.FilesProcessedTotal,
			}
		case EventError:
			if ev.Fatal {
				fatalErr = fmt.Errorf("server error: %s", ev.Message)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		// Distinguish caller cancel vs idle timeout vs network error.
		if callerCtx.Err() != nil {
			return nil, callerCtx.Err()
		}
		if streamCtx.Err() == context.Canceled && idleTimeout > 0 {
			return nil, ErrIdleTimeout
		}
		return nil, fmt.Errorf("scan ndjson: %w", err)
	}

	if fatalErr != nil {
		return nil, fatalErr
	}
	if final == nil {
		// Stream ended cleanly but no batch_done — server bug or partial
		// write. Surface it so the caller can retry the batch.
		return nil, fmt.Errorf("ndjson stream ended without batch_done event")
	}
	return final, nil
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
