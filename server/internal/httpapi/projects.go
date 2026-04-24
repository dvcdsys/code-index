package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// ---------------------------------------------------------------------------
// JSON request / response types (match Python schemas exactly)
// ---------------------------------------------------------------------------

type projectSettingsJSON struct {
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize     int      `json:"max_file_size"`
}

type projectStatsJSON struct {
	TotalFiles   int `json:"total_files"`
	IndexedFiles int `json:"indexed_files"`
	TotalChunks  int `json:"total_chunks"`
	TotalSymbols int `json:"total_symbols"`
}

type projectResponse struct {
	HostPath      string               `json:"host_path"`
	ContainerPath string               `json:"container_path"`
	Languages     []string             `json:"languages"`
	Settings      projectSettingsJSON  `json:"settings"`
	Stats         projectStatsJSON     `json:"stats"`
	Status        string               `json:"status"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
	LastIndexedAt *string              `json:"last_indexed_at"`
}

type projectListResponse struct {
	Projects []projectResponse `json:"projects"`
	Total    int               `json:"total"`
}

type createProjectRequest struct {
	HostPath string `json:"host_path"`
}

type updateProjectRequest struct {
	Settings *projectSettingsJSON `json:"settings"`
}

// ---------------------------------------------------------------------------
// Converters
// ---------------------------------------------------------------------------

func projectToResponse(p *projects.Project) projectResponse {
	langs := p.Languages
	if langs == nil {
		langs = []string{}
	}
	return projectResponse{
		HostPath:      p.HostPath,
		ContainerPath: p.ContainerPath,
		Languages:     langs,
		Settings: projectSettingsJSON{
			ExcludePatterns: p.Settings.ExcludePatterns,
			MaxFileSize:     p.Settings.MaxFileSize,
		},
		Stats: projectStatsJSON{
			TotalFiles:   p.Stats.TotalFiles,
			IndexedFiles: p.Stats.IndexedFiles,
			TotalChunks:  p.Stats.TotalChunks,
			TotalSymbols: p.Stats.TotalSymbols,
		},
		Status:        p.Status,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
		LastIndexedAt: p.LastIndexedAt,
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// POST /api/v1/projects
func createProjectHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body createProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}
		if body.HostPath == "" {
			writeError(w, http.StatusUnprocessableEntity, "host_path is required")
			return
		}

		p, err := projects.Create(r.Context(), d.DB, projects.CreateRequest{HostPath: body.HostPath})
		if err != nil {
			if errors.Is(err, projects.ErrConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, projectToResponse(p))
	}
}

// GET /api/v1/projects
func listProjectsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := projects.List(r.Context(), d.DB)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := make([]projectResponse, 0, len(list))
		for i := range list {
			resp = append(resp, projectToResponse(&list[i]))
		}
		writeJSON(w, http.StatusOK, projectListResponse{Projects: resp, Total: len(resp)})
	}
}

// GET /api/v1/projects/{path}
func getProjectHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathHash := chi.URLParam(r, "path")
		p, err := projects.GetByHash(r.Context(), d.DB, pathHash)
		if err != nil {
			if errors.Is(err, projects.ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, projectToResponse(p))
	}
}

// PATCH /api/v1/projects/{path}
func patchProjectHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathHash := chi.URLParam(r, "path")
		p, err := projects.GetByHash(r.Context(), d.DB, pathHash)
		if err != nil {
			if errors.Is(err, projects.ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		var body updateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid request body")
			return
		}

		var settingsPtr *projects.Settings
		if body.Settings != nil {
			s := projects.Settings{
				ExcludePatterns: body.Settings.ExcludePatterns,
				MaxFileSize:     body.Settings.MaxFileSize,
			}
			settingsPtr = &s
		}

		updated, err := projects.Patch(r.Context(), d.DB, p.HostPath, projects.UpdateRequest{Settings: settingsPtr})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, projectToResponse(updated))
	}
}

// DELETE /api/v1/projects/{path}
func deleteProjectHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pathHash := chi.URLParam(r, "path")
		p, err := projects.GetByHash(r.Context(), d.DB, pathHash)
		if err != nil {
			if errors.Is(err, projects.ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		if err := projects.Delete(r.Context(), d.DB, p.HostPath); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// Error helper
// ---------------------------------------------------------------------------

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"detail": msg})
}
