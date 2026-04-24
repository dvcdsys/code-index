package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/indexer"
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
			writeJSON(w, http.StatusOK, indexProgressResponse{
				Status: progress.Status,
				Progress: map[string]any{
					"phase":            progress.Phase,
					"files_processed":  progress.FilesProcessed,
					"chunks_created":   progress.ChunksCreated,
					"elapsed_seconds":  roundFloat1(progress.ElapsedSeconds),
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
