package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/projects"
)

// ---------------------------------------------------------------------------
// Wire-format types kept as test fixtures.
//
// The Server methods in server.go construct openapi.* equivalents; the
// types below are byte-compatible JSON shapes that *_test.go files
// unmarshal into. Removing them would force every indexing test to be
// rewritten — keeping them costs nothing.
// ---------------------------------------------------------------------------

type indexBeginRequest struct {
	Full bool `json:"full"`
}

type indexBeginResponse struct {
	RunID        string            `json:"run_id"`
	StoredHashes map[string]string `json:"stored_hashes"`
}

type filePayloadJSON struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	Language    string `json:"language,omitempty"`
	Size        int    `json:"size"`
}

type indexFilesRequest struct {
	RunID string            `json:"run_id"`
	Files []filePayloadJSON `json:"files"`
}

type indexFilesResponse struct {
	FilesAccepted       int `json:"files_accepted"`
	ChunksCreated       int `json:"chunks_created"`
	FilesProcessedTotal int `json:"files_processed_total"`
}

type indexFinishRequest struct {
	RunID                string   `json:"run_id"`
	DeletedPaths         []string `json:"deleted_paths"`
	TotalFilesDiscovered int      `json:"total_files_discovered"`
}

type indexFinishResponse struct {
	Status         string `json:"status"`
	FilesProcessed int    `json:"files_processed"`
	ChunksCreated  int    `json:"chunks_created"`
}

type indexProgressResponse struct {
	Status   string         `json:"status"`
	Progress map[string]any `json:"progress,omitempty"`
}

type indexCancelResponse struct {
	Cancelled bool `json:"cancelled"`
}

// Suppress "declared but not used" warnings for the request shapes — they
// are populated only via JSON unmarshal in tests, so static analysis cannot
// see the writes.
var (
	_ = indexBeginRequest{}
	_ = indexFilesRequest{}
	_ = indexFinishRequest{}
)

// ---------------------------------------------------------------------------
// Constants + helpers shared with server.go (Server.IndexFiles).
// ---------------------------------------------------------------------------

// maxFilesPerBatch matches Python schemas.IndexFilesRequest max_length=50.
const maxFilesPerBatch = 50

// streamingHeartbeatInterval is how often we emit a heartbeat event when no
// file-level progress has been sent. Idle on the wire ≤ heartbeatInterval +
// embedder slack, well under the client's default 30s read deadline. Var
// (not const) so tests can shrink it to keep the suite fast.
var streamingHeartbeatInterval = 10 * time.Second

// streamingDisconnectCancelTimeout bounds how long we spend cleaning up a
// session after the client disconnects.
const streamingDisconnectCancelTimeout = 5 * time.Second

// acceptsNDJSON returns true when the Accept header advertises
// application/x-ndjson. Comma-separated values are inspected; q-values are
// ignored (presence is sufficient — the client opted in).
func acceptsNDJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(part)
		if i := strings.IndexByte(mediaType, ';'); i >= 0 {
			mediaType = strings.TrimSpace(mediaType[:i])
		}
		if strings.EqualFold(mediaType, "application/x-ndjson") {
			return true
		}
	}
	return false
}

// indexFilesStreamingHandler writes one NDJSON event per line with per-file
// progress and 10-second heartbeats. When the client disconnects mid-batch
// we call CancelIndexing so the session lock is released immediately rather
// than lingering until the 1-hour TTL.
//
// Called from Server.IndexFiles when the client requests the streaming
// content type. Lives in this file because the JSON wire format and the
// indexer-channel plumbing belong together.
func indexFilesStreamingHandler(
	d Deps,
	p *projects.Project,
	runID string,
	files []indexer.FilePayload,
	w http.ResponseWriter,
	r *http.Request,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by HTTP transport")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progress := make(chan indexer.ProgressEvent, 32)

	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()

	go func() {
		defer close(progress)
		_, _, _, _ = d.Indexer.ProcessFilesStreaming(streamCtx, p.HostPath, runID, files, progress)
	}()

	ticker := time.NewTicker(streamingHeartbeatInterval)
	defer ticker.Stop()

	encoder := json.NewEncoder(w)
	clientGone := false

	markClientGone := func() {
		if clientGone {
			return
		}
		clientGone = true
		cancelStream()
	}

	for {
		select {
		case ev, open := <-progress:
			if !open {
				if clientGone {
					d.Logger.Warn("streaming: client disconnected mid-batch, cancelling session",
						"run_id", runID, "project", p.HostPath)
					cancelCtx, cancel := context.WithTimeout(
						context.Background(), streamingDisconnectCancelTimeout)
					_, _ = d.Indexer.CancelIndexing(cancelCtx, p.HostPath)
					cancel()
				}
				return
			}
			if clientGone {
				continue
			}
			if err := encoder.Encode(ev); err != nil {
				markClientGone()
				continue
			}
			flusher.Flush()
		case <-ticker.C:
			if clientGone {
				continue
			}
			if err := encoder.Encode(indexer.ProgressEvent{
				Event: indexer.EventHeartbeat,
				TS:    indexer.NowTS(),
			}); err != nil {
				markClientGone()
				continue
			}
			flusher.Flush()
		case <-r.Context().Done():
			d.Logger.Debug("streaming: r.Context() done", "run_id", runID, "err", r.Context().Err())
			markClientGone()
		}
	}
}

// roundFloat1 rounds to 1 decimal place — matches Python round(x, 1).
// Used by Server.IndexStatus for elapsed_seconds.
func roundFloat1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
