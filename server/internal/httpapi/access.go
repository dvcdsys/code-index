package httpapi

import (
	"errors"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// callerIdentity returns the authenticated user id and whether the caller is
// an admin. When auth is disabled (CIX_AUTH_DISABLED), every request is
// treated as admin — matching mustBeAdmin's dev-mode short-circuit.
func (s *Server) callerIdentity(r *http.Request) (userID string, isAdmin bool) {
	if s.Deps.AuthDisabled {
		return "", true
	}
	ac, ok := authFromCtx(r.Context())
	if !ok {
		return "", false
	}
	return ac.User.ID, ac.User.Role == users.RoleAdmin
}

// requireLocalProjectActions gates the operations a user can be restricted
// from: creating a local project and indexing/reindexing. Admins and the
// CIX_AUTH_DISABLED dev mode are always exempt (an admin is never blocked by a
// per-user flag) — only a regular user with local_project_disabled=1 is
// rejected with 403. Search and workspace creation never call this. On
// rejection it writes the response and returns false:
//
//	if !s.requireLocalProjectActions(w, r) { return }
//
// Mid-indexing edge: the flag is checked per request, so flipping it on while
// a user has an in-flight index session (begin succeeded) makes the next
// index/files or index/finish 403 and strands that session. This is benign —
// index/cancel is deliberately NOT gated (see IndexCancel) so the user/CLI can
// still release the run lock, and an abandoned session expires on its own TTL.
// We accept the rare stranded-session window rather than special-casing
// in-flight runs.
func (s *Server) requireLocalProjectActions(w http.ResponseWriter, r *http.Request) bool {
	if s.Deps.AuthDisabled {
		return true
	}
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return false
	}
	if ac.User.Role == users.RoleAdmin {
		return true
	}
	if ac.User.LocalProjectDisabled {
		writeError(w, http.StatusForbidden, "your account is not permitted to create or index local projects")
		return false
	}
	return true
}

// canAccessProject reports whether the caller has READ access to an
// already-loaded project: admin, owner, or a member of a view-group it is
// shared to. Used where the project is resolved by the handler (not the
// {path} URL param) — e.g. linking a project into a workspace.
func (s *Server) canAccessProject(r *http.Request, p *projects.Project) bool {
	userID, isAdmin := s.callerIdentity(r)
	if isAdmin {
		return true
	}
	if userID != "" && p.OwnerUserID != nil && *p.OwnerUserID == userID {
		return true
	}
	if userID != "" {
		shared, _ := access.ProjectSharedToUser(r.Context(), s.Deps.DB, p.HostPath, userID)
		return shared
	}
	return false
}

// requireProjectAccess resolves the project named by the {path} hash and
// enforces READ access: admin, owner, or a member of a view-group the project
// is shared to. On any failure it writes the response (404 to hide existence
// from callers without access) and returns nil.
func (s *Server) requireProjectAccess(w http.ResponseWriter, r *http.Request) *projects.Project {
	p := resolveProjectFromHash(w, r, s.Deps)
	if p == nil {
		return nil
	}
	userID, isAdmin := s.callerIdentity(r)
	if isAdmin {
		return p
	}
	if userID != "" && p.OwnerUserID != nil && *p.OwnerUserID == userID {
		return p
	}
	if userID != "" {
		shared, err := access.ProjectSharedToUser(r.Context(), s.Deps.DB, p.HostPath, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "access check failed")
			return nil
		}
		if shared {
			return p
		}
	}
	writeError(w, http.StatusNotFound, "project not found")
	return nil
}

// requireProjectOwnership resolves the project and enforces WRITE access:
// admin or the project's owner. External projects (ownerless) are admin-only.
// Returns 403 when the caller can read the project but does not own it, 404
// when they cannot see it at all.
func (s *Server) requireProjectOwnership(w http.ResponseWriter, r *http.Request) *projects.Project {
	p := resolveProjectFromHash(w, r, s.Deps)
	if p == nil {
		return nil
	}
	userID, isAdmin := s.callerIdentity(r)
	if isAdmin {
		return p
	}
	if userID != "" && p.OwnerUserID != nil && *p.OwnerUserID == userID {
		return p
	}
	if userID != "" {
		if shared, _ := access.ProjectSharedToUser(r.Context(), s.Deps.DB, p.HostPath, userID); shared {
			writeError(w, http.StatusForbidden, "this action requires the project owner or an admin")
			return nil
		}
	}
	writeError(w, http.StatusNotFound, "project not found")
	return nil
}

// loadWorkspace fetches the workspace and writes 404/500 on error, returning
// ok=false. Shared by the visibility/ownership guards below.
func (s *Server) loadWorkspace(w http.ResponseWriter, r *http.Request, id string) (workspaces.Workspace, bool) {
	ws, err := s.Deps.Workspaces.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, workspaces.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load workspace")
		}
		return workspaces.Workspace{}, false
	}
	return ws, true
}

// requireWorkspaceVisible enforces READ visibility: admin, owner, or a member
// of a view-group the workspace is shared to. 404 hides non-visible workspaces.
func (s *Server) requireWorkspaceVisible(w http.ResponseWriter, r *http.Request, id string) (workspaces.Workspace, bool) {
	ws, ok := s.loadWorkspace(w, r, id)
	if !ok {
		return ws, false
	}
	userID, isAdmin := s.callerIdentity(r)
	if isAdmin {
		return ws, true
	}
	if userID != "" && ws.OwnerUserID != nil && *ws.OwnerUserID == userID {
		return ws, true
	}
	if userID != "" {
		if shared, _ := access.WorkspaceSharedToUser(r.Context(), s.Deps.DB, id, userID); shared {
			return ws, true
		}
	}
	writeError(w, http.StatusNotFound, "workspace not found")
	return workspaces.Workspace{}, false
}

// requireWorkspaceOwnership enforces WRITE access: admin or the owner. 403 when
// the caller can see the workspace but does not own it, 404 when hidden.
func (s *Server) requireWorkspaceOwnership(w http.ResponseWriter, r *http.Request, id string) (workspaces.Workspace, bool) {
	ws, ok := s.loadWorkspace(w, r, id)
	if !ok {
		return ws, false
	}
	userID, isAdmin := s.callerIdentity(r)
	if isAdmin {
		return ws, true
	}
	if userID != "" && ws.OwnerUserID != nil && *ws.OwnerUserID == userID {
		return ws, true
	}
	if userID != "" {
		if shared, _ := access.WorkspaceSharedToUser(r.Context(), s.Deps.DB, id, userID); shared {
			writeError(w, http.StatusForbidden, "this action requires the workspace owner or an admin")
			return workspaces.Workspace{}, false
		}
	}
	writeError(w, http.StatusNotFound, "workspace not found")
	return workspaces.Workspace{}, false
}
