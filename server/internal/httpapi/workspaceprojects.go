package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
)

// workspaceProjectsUnavailable returns 503 when any required service
// is nil. Main always wires both; this defensive check guards tests
// that pass a partial Deps.
func (s *Server) workspaceProjectsUnavailable(w http.ResponseWriter) bool {
	if s.Deps.WorkspaceProjects == nil || s.Deps.Workspaces == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces service is not configured on this server")
		return true
	}
	return false
}

// ListWorkspaceProjects — GET /api/v1/workspaces/{id}/projects. Visible to
// owner/group-members/admin; per the decoupled model, projects the caller
// cannot access are dropped from the listing rather than blocking the whole
// workspace.
func (s *Server) ListWorkspaceProjects(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspaceProjectsUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceVisible(w, r, id); !ok {
		return
	}
	memberships, err := s.Deps.WorkspaceProjects.ListByWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspace projects: "+err.Error())
		return
	}
	userID, isAdmin := s.callerIdentity(r)
	var allowed map[string]struct{}
	if !isAdmin {
		hosts, aerr := access.AccessibleProjectHostPaths(r.Context(), s.Deps.DB, userID)
		if aerr != nil {
			writeError(w, http.StatusInternalServerError, "access check failed")
			return
		}
		allowed = make(map[string]struct{}, len(hosts))
		for _, hp := range hosts {
			allowed[hp] = struct{}{}
		}
	}
	out := make([]map[string]any, 0, len(memberships))
	for _, m := range memberships {
		if !isAdmin {
			if _, ok := allowed[m.ProjectPath]; !ok {
				continue // hidden from this caller
			}
		}
		proj, perr := projects.Get(r.Context(), s.Deps.DB, m.ProjectPath)
		if perr != nil {
			// The FK should prevent dangling rows, but if the project
			// disappeared between SELECT and Get we just skip it.
			s.Deps.Logger.Warn("workspaceprojects: project missing for membership",
				"workspace_id", id, "project_path", m.ProjectPath, "err", perr)
			continue
		}
		out = append(out, map[string]any{
			"project":  projectToOpenAPI(proj),
			"added_at": m.AddedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": out,
		"total":    len(out),
	})
}

// LinkProjectToWorkspace — POST /api/v1/workspaces/{id}/projects.
//
// Adds an existing indexed project to a workspace. The project must be
// in status='indexed'; pending/cloning projects return 422 so the
// dashboard can prompt the user to wait.
func (s *Server) LinkProjectToWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspaceProjectsUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceOwnership(w, r, id); !ok {
		return
	}
	var body openapi.LinkProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	proj, perr := projects.GetByHash(r.Context(), s.Deps.DB, body.ProjectHash)
	if perr != nil {
		if errors.Is(perr, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+perr.Error())
		return
	}
	// The caller may only link projects they can access (hide others as 404).
	if !s.canAccessProject(r, proj) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	m, err := s.Deps.WorkspaceProjects.Link(r.Context(), id, proj.HostPath)
	if err != nil {
		switch {
		case errors.Is(err, workspaceprojects.ErrProjectNotIndexed):
			writeError(w, http.StatusUnprocessableEntity,
				"project is not yet indexed — wait for indexing to complete before linking")
		case errors.Is(err, workspaceprojects.ErrProjectMissing):
			writeError(w, http.StatusNotFound, "project not found")
		case errors.Is(err, workspaceprojects.ErrWorkspaceMissing):
			writeError(w, http.StatusNotFound, "workspace not found")
		case errors.Is(err, workspaceprojects.ErrDuplicate):
			writeError(w, http.StatusConflict, "project is already linked to this workspace")
		default:
			writeError(w, http.StatusInternalServerError, "could not link project: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"workspace_id": m.WorkspaceID,
		"project_path": m.ProjectPath,
		"added_at":     m.AddedAt,
	})
}

// UnlinkProjectFromWorkspace — DELETE /api/v1/workspaces/{id}/projects/{hash}.
//
// Removes the workspace_projects row. The project itself is untouched.
// 204 on success, 404 when the project (or the membership) doesn't exist.
func (s *Server) UnlinkProjectFromWorkspace(w http.ResponseWriter, r *http.Request, id string, hash openapi.ProjectHash) {
	if s.workspaceProjectsUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceOwnership(w, r, id); !ok {
		return
	}
	proj, perr := projects.GetByHash(r.Context(), s.Deps.DB, string(hash))
	if perr != nil {
		if errors.Is(perr, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+perr.Error())
		return
	}
	if err := s.Deps.WorkspaceProjects.Unlink(r.Context(), id, proj.HostPath); err != nil {
		if errors.Is(err, workspaceprojects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project is not linked to this workspace")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not unlink project: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
