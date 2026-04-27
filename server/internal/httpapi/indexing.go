package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/projects"
)

// ---------------------------------------------------------------------------
// Request / response types — match api/app/schemas/indexing.py exactly.
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

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/index/begin
// ---------------------------------------------------------------------------

func indexBeginHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.Indexer == nil {
			writeError(w, http.StatusServiceUnavailable, "indexer not configured")
			return
		}

		var body indexBeginRequest
		// Body is optional — accept empty request.
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid request body")
				return
			}
		}

		runID, stored, err := d.Indexer.BeginIndexing(r.Context(), p.HostPath, body.Full)
		if err != nil {
			// C2 — another session is already active for this project.
			if errors.Is(err, indexer.ErrSessionConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if stored == nil {
			stored = map[string]string{}
		}
		writeJSON(w, http.StatusOK, indexBeginResponse{RunID: runID, StoredHashes: stored})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/index/files
// ---------------------------------------------------------------------------

// maxFilesPerBatch matches Python schemas.IndexFilesRequest max_length=50.
const maxFilesPerBatch = 50

func indexFilesHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.Indexer == nil {
			writeError(w, http.StatusServiceUnavailable, "indexer not configured")
			return
		}

		var body indexFilesRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.RunID == "" {
			writeError(w, http.StatusUnprocessableEntity, "run_id is required")
			return
		}
		if len(body.Files) > maxFilesPerBatch {
			writeError(w, http.StatusUnprocessableEntity, "too many files in batch (max 50)")
			return
		}

		files := make([]indexer.FilePayload, len(body.Files))
		for i, f := range body.Files {
			files[i] = indexer.FilePayload{
				Path:        f.Path,
				Content:     f.Content,
				ContentHash: f.ContentHash,
				Language:    f.Language,
				Size:        f.Size,
			}
		}

		// Negotiate streaming vs single-JSON via Accept header. Old CLIs do
		// not advertise application/x-ndjson, so they keep getting the legacy
		// blocking response. New CLIs explicitly request the stream and get
		// per-file progress + heartbeats.
		if acceptsNDJSON(r.Header.Get("Accept")) {
			indexFilesStreamingHandler(d, p, body.RunID, files, w, r)
			return
		}

		accepted, chunks, total, err := d.Indexer.ProcessFiles(r.Context(), p.HostPath, body.RunID, files)
		if err != nil {
			if retry, busy := embeddings.IsBusy(err); busy {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeError(w, http.StatusServiceUnavailable,
					"GPU is busy processing another embedding request, retry after "+strconv.Itoa(retry)+"s")
				return
			}
			if errors.Is(err, indexer.ErrNoSession) || errors.Is(err, indexer.ErrProjectMismatch) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, indexFilesResponse{
			FilesAccepted:       accepted,
			ChunksCreated:       chunks,
			FilesProcessedTotal: total,
		})
	}
}

// acceptsNDJSON returns true when the Accept header advertises
// application/x-ndjson. Comma-separated values are inspected; q-values are
// ignored (presence is sufficient — the client opted in).
func acceptsNDJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		// Strip parameters (q=…) and surrounding whitespace.
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

// streamingHeartbeatInterval is how often we emit a heartbeat event when no
// file-level progress has been sent. Idle on the wire ≤ heartbeatInterval +
// embedder slack, well under the client's default 30s read deadline. Var
// (not const) so tests can shrink it to keep the suite fast.
var streamingHeartbeatInterval = 10 * time.Second

// streamingDisconnectCancelTimeout bounds how long we spend cleaning up a
// session after the client disconnects.
const streamingDisconnectCancelTimeout = 5 * time.Second

// indexFilesStreamingHandler writes one NDJSON event per line with per-file
// progress and 10-second heartbeats. When the client disconnects mid-batch
// we call CancelIndexing so the session lock is released immediately rather
// than lingering until the 1-hour TTL.
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
		// httptest.ResponseRecorder and a few mock servers don't implement
		// Flusher. Falling back to writeError keeps tests readable while
		// still pointing at the misuse.
		writeError(w, http.StatusInternalServerError, "streaming not supported by HTTP transport")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	// X-Accel-Buffering disables proxy buffering on nginx; harmless elsewhere.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progress := make(chan indexer.ProgressEvent, 32)

	// streamCtx is a child of r.Context() so client-disconnect propagation
	// works automatically, but we keep our own cancel handle so a *write*
	// failure (broken pipe before Go's read goroutine notices the FIN) can
	// also unblock the indexer goroutine immediately. Otherwise the embedder
	// would keep computing wasted GPU work until r.Context() eventually fires.
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

	// markClientGone is the single place where we transition into the "drain
	// progress until the indexer exits" mode. Cancelling streamCtx makes the
	// embedder's ctx.Done() select fire so the indexer returns within ms
	// rather than completing wasted work.
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
				// ProcessFilesStreaming has returned and closed the channel.
				// If the client disconnected mid-flight, free the session
				// lock immediately so a follow-up reindex doesn't hit 409.
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
				continue // drain to let ProcessFilesStreaming finish
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
			// Client disconnected (or request context cancelled by router).
			// Set clientGone and cancel the indexer's ctx so it returns now.
			d.Logger.Debug("streaming: r.Context() done", "run_id", runID, "err", r.Context().Err())
			markClientGone()
		}
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/index/finish
// ---------------------------------------------------------------------------

func indexFinishHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.Indexer == nil {
			writeError(w, http.StatusServiceUnavailable, "indexer not configured")
			return
		}

		var body indexFinishRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.RunID == "" {
			writeError(w, http.StatusUnprocessableEntity, "run_id is required")
			return
		}

		status, files, chunks, err := d.Indexer.FinishIndexing(
			r.Context(), p.HostPath, body.RunID, body.DeletedPaths, body.TotalFilesDiscovered,
		)
		if err != nil {
			if errors.Is(err, indexer.ErrNoSession) || errors.Is(err, indexer.ErrProjectMismatch) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, indexFinishResponse{
			Status:         status,
			FilesProcessed: files,
			ChunksCreated:  chunks,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects/{path}/index/cancel
// ---------------------------------------------------------------------------

type indexCancelResponse struct {
	Cancelled bool `json:"cancelled"`
}

// indexCancelHandler terminates any in-flight session for the project.
// Idempotent: returns {cancelled: false} when no session is active, so the
// CLI stale-session guard at startup can call this unconditionally.
func indexCancelHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.Indexer == nil {
			writeJSON(w, http.StatusOK, indexCancelResponse{Cancelled: false})
			return
		}

		cancelled, err := d.Indexer.CancelIndexing(r.Context(), p.HostPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, indexCancelResponse{Cancelled: cancelled})
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/projects/{path}/index/status
// ---------------------------------------------------------------------------

func indexStatusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := resolveProjectFromHash(w, r, d)
		if p == nil {
			return
		}
		if d.Indexer == nil {
			writeJSON(w, http.StatusOK, indexProgressResponse{Status: "idle"})
			return
		}

		progress := d.Indexer.GetProgress(p.HostPath)
		if progress != nil {
			// m4 — match Python's progress payload. Python emits
			// files_discovered alongside files_processed (routers/indexing.py).
			writeJSON(w, http.StatusOK, indexProgressResponse{
				Status: progress.Status,
				Progress: map[string]any{
					"phase":            progress.Phase,
					"files_discovered": progress.FilesDiscovered,
					"files_processed":  progress.FilesProcessed,
					"files_total":      progress.FilesTotal,
					"chunks_created":   progress.ChunksCreated,
					"elapsed_seconds":  roundFloat1(progress.ElapsedSeconds),
					"run_id":           progress.RunID,
				},
			})
			return
		}

		// Fall back to last run row.
		row := d.DB.QueryRowContext(r.Context(),
			`SELECT status, files_processed, files_total, chunks_created
			 FROM index_runs WHERE project_path = ? ORDER BY started_at DESC LIMIT 1`,
			p.HostPath,
		)
		var status string
		var filesProcessed, filesTotal, chunks int
		if err := row.Scan(&status, &filesProcessed, &filesTotal, &chunks); err != nil {
			writeJSON(w, http.StatusOK, indexProgressResponse{Status: "idle"})
			return
		}
		writeJSON(w, http.StatusOK, indexProgressResponse{
			Status: status,
			Progress: map[string]any{
				"files_processed": filesProcessed,
				"files_total":     filesTotal,
				"chunks_created":  chunks,
			},
		})
	}
}

// roundFloat1 rounds to 1 decimal place — matches Python round(x, 1).
func roundFloat1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
