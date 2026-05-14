package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/projects"
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
func (s *Server) ListProjectWorkspaces(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	hash := string(path)
	proj, err := projects.GetByHash(r.Context(), s.Deps.DB, hash)
	if err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
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
