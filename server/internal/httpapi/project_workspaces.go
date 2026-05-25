package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// projectWorkspaceEntryPayload is the wire shape for one membership.
type projectWorkspaceEntryPayload struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	AddedAt       time.Time `json:"added_at"`
}

// ListProjectWorkspaces — GET /api/v1/projects/{path}/workspaces.
//
// Returns every workspace that this project is currently linked into.
// Used by the project detail page to render "Workspaces" chips. Empty
// list when the project is in no workspace (true for freshly-added
// standalone projects). The workspaces feature flag is NOT consulted —
// returning an empty list is fine even when the flag is off.
//
// Access: gated by requireProjectAccess (admin / owner / shared via group).
// Non-admin callers additionally see only workspaces they themselves can
// see — without that filter a non-owner who could reach the project (e.g.
// via a group share) would also learn the names and IDs of *every* other
// workspace it's linked into, including private ones. Admin sees all.
func (s *Server) ListProjectWorkspaces(w http.ResponseWriter, r *http.Request, _ openapi.ProjectHash) {
	proj := s.requireProjectAccess(w, r)
	if proj == nil {
		return
	}

	// Pre-compute the visible-workspace set for non-admin callers so the
	// result excludes workspaces the user has no business knowing about.
	// Done BEFORE the main query — SQLite ":memory:" backends in tests have
	// a single-connection pool, and overlapping QueryContext calls deadlock.
	userID, isAdmin := s.callerIdentity(r)
	var visible map[string]struct{}
	if !isAdmin {
		ids, vErr := access.VisibleWorkspaceIDs(r.Context(), s.Deps.DB, userID)
		if vErr != nil {
			writeError(w, http.StatusInternalServerError, "access check failed")
			return
		}
		visible = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			visible[id] = struct{}{}
		}
	}

	rows, err := s.Deps.DB.QueryContext(r.Context(), `
		SELECT w.id, w.name, wp.added_at
		  FROM workspaces w
		  JOIN workspace_projects wp ON wp.workspace_id = w.id
		 WHERE wp.project_path = ?
		 ORDER BY w.name`, proj.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspaces: "+err.Error())
		return
	}
	defer rows.Close()

	entries := []projectWorkspaceEntryPayload{}
	for rows.Next() {
		var (
			e       projectWorkspaceEntryPayload
			addedAt string
		)
		if scanErr := rows.Scan(&e.WorkspaceID, &e.WorkspaceName, &addedAt); scanErr != nil {
			writeError(w, http.StatusInternalServerError, "could not read row: "+scanErr.Error())
			return
		}
		if !isAdmin {
			if _, ok := visible[e.WorkspaceID]; !ok {
				continue
			}
		}
		e.AddedAt, _ = time.Parse(time.RFC3339Nano, addedAt)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not scan rows: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspaces": entries,
	})
}
