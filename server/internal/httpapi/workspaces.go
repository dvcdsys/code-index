package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// workspacePayload is the JSON shape sent back to clients. It mirrors the
// generated openapi.Workspace exactly (no oneOf magic), keeping the surface
// stable across regeneration cycles.
type workspacePayload struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func workspaceToPayload(w workspaces.Workspace) workspacePayload {
	return workspacePayload{
		ID:          w.ID,
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

// workspacesUnavailable returns 503 when the feature flag is off OR the
// service is nil. Single source for the message so the dashboard's
// "feature off" UI key is stable.
func (s *Server) workspacesUnavailable(w http.ResponseWriter) bool {
	if s.Deps.Workspaces == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces service is not configured on this server")
		return true
	}
	return false
}

// ListWorkspaces — GET /api/v1/workspaces.
func (s *Server) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if s.workspacesUnavailable(w) {
		return
	}
	list, err := s.Deps.Workspaces.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspaces")
		return
	}
	out := make([]workspacePayload, 0, len(list))
	for _, ws := range list {
		out = append(out, workspaceToPayload(ws))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspaces": out,
		"total":      len(out),
	})
}

// CreateWorkspace — POST /api/v1/workspaces.
func (s *Server) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.workspacesUnavailable(w) {
		return
	}
	var body openapi.CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	description := ""
	if body.Description != nil {
		description = *body.Description
	}
	ws, err := s.Deps.Workspaces.Create(r.Context(), body.Name, description)
	if err != nil {
		switch {
		case errors.Is(err, workspaces.ErrNameEmpty):
			writeError(w, http.StatusUnprocessableEntity, "name is required")
		case errors.Is(err, workspaces.ErrNameTaken):
			writeError(w, http.StatusConflict, "workspace name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not create workspace")
		}
		return
	}
	writeJSON(w, http.StatusCreated, workspaceToPayload(ws))
}

// GetWorkspace — GET /api/v1/workspaces/{id}.
func (s *Server) GetWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspacesUnavailable(w) {
		return
	}
	ws, err := s.Deps.Workspaces.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, workspaces.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	writeJSON(w, http.StatusOK, workspaceToPayload(ws))
}

// UpdateWorkspace — PATCH /api/v1/workspaces/{id}.
func (s *Server) UpdateWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspacesUnavailable(w) {
		return
	}
	var body openapi.UpdateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	ws, err := s.Deps.Workspaces.Update(r.Context(), id, body.Name, body.Description)
	if err != nil {
		switch {
		case errors.Is(err, workspaces.ErrNotFound):
			writeError(w, http.StatusNotFound, "workspace not found")
		case errors.Is(err, workspaces.ErrNameEmpty):
			writeError(w, http.StatusUnprocessableEntity, "name is required")
		case errors.Is(err, workspaces.ErrNameTaken):
			writeError(w, http.StatusConflict, "workspace name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not update workspace")
		}
		return
	}
	writeJSON(w, http.StatusOK, workspaceToPayload(ws))
}

// DeleteWorkspace — DELETE /api/v1/workspaces/{id}.
func (s *Server) DeleteWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspacesUnavailable(w) {
		return
	}
	if err := s.Deps.Workspaces.Delete(r.Context(), id); err != nil {
		if errors.Is(err, workspaces.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete workspace")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
