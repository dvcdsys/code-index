package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/projects"
)

// ---------------------------------------------------------------------------
// Payloads (hand-built so date formatting stays RFC3339Nano UTC, matching the
// rest of the API).
// ---------------------------------------------------------------------------

type groupPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func groupToPayload(g groups.Group) groupPayload {
	return groupPayload{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		CreatedAt:   g.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   g.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type groupMemberPayload struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	AddedAt string `json:"added_at"`
}

// groupsUnavailable returns 503 when the service is not wired.
func (s *Server) groupsUnavailable(w http.ResponseWriter) bool {
	if s.Deps.Groups == nil {
		writeError(w, http.StatusServiceUnavailable, "groups service is not configured on this server")
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Group CRUD (admin-only except ListGroups, which is member-scoped for users)
// ---------------------------------------------------------------------------

// ListGroups — GET /api/v1/groups. Admins see all; users see their own groups.
func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request) {
	if s.groupsUnavailable(w) {
		return
	}
	userID, isAdmin := s.callerIdentity(r)
	var (
		list []groups.Group
		err  error
	)
	if isAdmin {
		list, err = s.Deps.Groups.List(r.Context())
	} else {
		list, err = s.Deps.Groups.ListForUser(r.Context(), userID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list groups")
		return
	}
	out := make([]groupPayload, 0, len(list))
	for _, g := range list {
		out = append(out, groupToPayload(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out, "total": len(out)})
}

// CreateGroup — POST /api/v1/groups (admin only).
func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	g, err := s.Deps.Groups.Create(r.Context(), body.Name, body.Description)
	if err != nil {
		switch {
		case errors.Is(err, groups.ErrNameEmpty):
			writeError(w, http.StatusUnprocessableEntity, "name is required")
		case errors.Is(err, groups.ErrNameTaken):
			writeError(w, http.StatusConflict, "group name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not create group")
		}
		return
	}
	writeJSON(w, http.StatusCreated, groupToPayload(g))
}

// GetGroup — GET /api/v1/groups/{id} (admin only).
func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	g, err := s.Deps.Groups.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, groups.ErrNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load group")
		return
	}
	writeJSON(w, http.StatusOK, groupToPayload(g))
}

// UpdateGroup — PATCH /api/v1/groups/{id} (admin only).
func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	g, err := s.Deps.Groups.Update(r.Context(), id, body.Name, body.Description)
	if err != nil {
		switch {
		case errors.Is(err, groups.ErrNotFound):
			writeError(w, http.StatusNotFound, "group not found")
		case errors.Is(err, groups.ErrNameEmpty):
			writeError(w, http.StatusUnprocessableEntity, "name is required")
		case errors.Is(err, groups.ErrNameTaken):
			writeError(w, http.StatusConflict, "group name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not update group")
		}
		return
	}
	writeJSON(w, http.StatusOK, groupToPayload(g))
}

// DeleteGroup — DELETE /api/v1/groups/{id} (admin only).
func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	if err := s.Deps.Groups.Delete(r.Context(), id); err != nil {
		if errors.Is(err, groups.ErrNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Membership (admin only)
// ---------------------------------------------------------------------------

// ListGroupMembers — GET /api/v1/groups/{id}/members (admin only).
func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	if _, err := s.Deps.Groups.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, groups.ErrNotFound) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load group")
		return
	}
	members, err := s.Deps.Groups.ListMembers(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list members")
		return
	}
	out := make([]groupMemberPayload, 0, len(members))
	for _, m := range members {
		out = append(out, groupMemberPayload{
			UserID:  m.UserID,
			Email:   m.Email,
			Role:    m.Role,
			AddedAt: m.AddedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out, "total": len(out)})
}

// AddGroupMember — POST /api/v1/groups/{id}/members (admin only).
func (s *Server) AddGroupMember(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.UserID == "" {
		writeError(w, http.StatusUnprocessableEntity, "user_id is required")
		return
	}
	if err := s.Deps.Groups.AddMember(r.Context(), id, body.UserID); err != nil {
		switch {
		case errors.Is(err, groups.ErrNotFound):
			writeError(w, http.StatusNotFound, "group not found")
		case errors.Is(err, groups.ErrUserNotFound):
			writeError(w, http.StatusUnprocessableEntity, "user not found")
		default:
			writeError(w, http.StatusInternalServerError, "could not add member")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveGroupMember — DELETE /api/v1/groups/{id}/members/{userId} (admin only).
func (s *Server) RemoveGroupMember(w http.ResponseWriter, r *http.Request, id string, userId string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	if err := s.Deps.Groups.RemoveMember(r.Context(), id, userId); err != nil {
		writeError(w, http.StatusInternalServerError, "could not remove member")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Project shares (admin only; external projects only)
// ---------------------------------------------------------------------------

func (s *Server) loadProjectByHash(w http.ResponseWriter, r *http.Request, hash string) *projects.Project {
	p, err := projects.GetByHash(r.Context(), s.Deps.DB, hash)
	if err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return nil
		}
		writeError(w, http.StatusInternalServerError, "could not load project")
		return nil
	}
	return p
}

// ListProjectShares — GET /api/v1/projects/{hash}/shares (admin only).
func (s *Server) ListProjectShares(w http.ResponseWriter, r *http.Request, hash string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	p := s.loadProjectByHash(w, r, hash)
	if p == nil {
		return
	}
	ids, err := access.ListProjectShareGroupIDs(r.Context(), s.Deps.DB, p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list shares")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group_ids": ids})
}

// ShareProjectToGroup — POST /api/v1/projects/{hash}/shares (admin only).
func (s *Server) ShareProjectToGroup(w http.ResponseWriter, r *http.Request, hash string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.groupsUnavailable(w) {
		return
	}
	p := s.loadProjectByHash(w, r, hash)
	if p == nil {
		return
	}
	external, err := access.IsProjectExternal(r.Context(), s.Deps.DB, p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check project")
		return
	}
	if !external {
		writeError(w, http.StatusUnprocessableEntity, "only external projects can be shared to a group")
		return
	}
	groupID, ok := s.decodeGroupID(w, r)
	if !ok {
		return
	}
	if !s.groupExists(w, r, groupID) {
		return
	}
	if err := access.ShareProjectToGroup(r.Context(), s.Deps.DB, p.HostPath, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not share project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnshareProjectFromGroup — DELETE /api/v1/projects/{hash}/shares/{groupId} (admin only).
func (s *Server) UnshareProjectFromGroup(w http.ResponseWriter, r *http.Request, hash string, groupId string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	p := s.loadProjectByHash(w, r, hash)
	if p == nil {
		return
	}
	if err := access.UnshareProjectFromGroup(r.Context(), s.Deps.DB, p.HostPath, groupId); err != nil {
		writeError(w, http.StatusInternalServerError, "could not unshare project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReassignProjectOwner — PUT /api/v1/projects/{hash}/owner (admin only).
func (s *Server) ReassignProjectOwner(w http.ResponseWriter, r *http.Request, hash string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	p := s.loadProjectByHash(w, r, hash)
	if p == nil {
		return
	}
	external, err := access.IsProjectExternal(r.Context(), s.Deps.DB, p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check project")
		return
	}
	if external {
		writeError(w, http.StatusUnprocessableEntity, "external projects are ownerless and cannot be reassigned")
		return
	}
	var body struct {
		OwnerUserID string `json:"owner_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.OwnerUserID == "" {
		writeError(w, http.StatusUnprocessableEntity, "owner_user_id is required")
		return
	}
	target, err := s.Deps.Users.GetByID(r.Context(), body.OwnerUserID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "owner_user_id does not refer to an existing user")
		return
	}
	if target.DisabledAt != nil {
		writeError(w, http.StatusUnprocessableEntity, "cannot assign ownership to a disabled user")
		return
	}
	if err := projects.SetOwner(r.Context(), s.Deps.DB, p.HostPath, body.OwnerUserID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reassign owner")
		return
	}
	updated, err := projects.Get(r.Context(), s.Deps.DB, p.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load updated project")
		return
	}
	writeJSON(w, http.StatusOK, projectToOpenAPI(updated))
}

// ---------------------------------------------------------------------------
// Workspace shares (owner-in-group or admin)
// ---------------------------------------------------------------------------

// ListWorkspaceShares — GET /api/v1/workspaces/{id}/shares.
func (s *Server) ListWorkspaceShares(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspacesUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceVisible(w, r, id); !ok {
		return
	}
	ids, err := access.ListWorkspaceShareGroupIDs(r.Context(), s.Deps.DB, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list shares")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group_ids": ids})
}

// ShareWorkspaceToGroup — POST /api/v1/workspaces/{id}/shares. The owner may
// share to a group they belong to; an admin may share to any group.
func (s *Server) ShareWorkspaceToGroup(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspacesUnavailable(w) || s.groupsUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceOwnership(w, r, id); !ok {
		return
	}
	groupID, ok := s.decodeGroupID(w, r)
	if !ok {
		return
	}
	if !s.groupExists(w, r, groupID) {
		return
	}
	// A non-admin owner may only share to groups they are a member of.
	userID, isAdmin := s.callerIdentity(r)
	if !isAdmin {
		member, err := s.Deps.Groups.IsMember(r.Context(), userID, groupID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check membership")
			return
		}
		if !member {
			writeError(w, http.StatusForbidden, "you can only share to groups you belong to")
			return
		}
	}
	if err := access.ShareWorkspaceToGroup(r.Context(), s.Deps.DB, id, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not share workspace")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnshareWorkspaceFromGroup — DELETE /api/v1/workspaces/{id}/shares/{groupId}.
func (s *Server) UnshareWorkspaceFromGroup(w http.ResponseWriter, r *http.Request, id string, groupId string) {
	if s.workspacesUnavailable(w) {
		return
	}
	if _, ok := s.requireWorkspaceOwnership(w, r, id); !ok {
		return
	}
	if err := access.UnshareWorkspaceFromGroup(r.Context(), s.Deps.DB, id, groupId); err != nil {
		writeError(w, http.StatusInternalServerError, "could not unshare workspace")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- small shared helpers ---

func (s *Server) decodeGroupID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		GroupID string `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return "", false
	}
	if body.GroupID == "" {
		writeError(w, http.StatusUnprocessableEntity, "group_id is required")
		return "", false
	}
	return body.GroupID, true
}

func (s *Server) groupExists(w http.ResponseWriter, r *http.Request, groupID string) bool {
	if _, err := s.Deps.Groups.GetByID(r.Context(), groupID); err != nil {
		if errors.Is(err, groups.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "group_id does not refer to an existing group")
			return false
		}
		writeError(w, http.StatusInternalServerError, "could not load group")
		return false
	}
	return true
}
