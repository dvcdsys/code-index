package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/projects"
)

// projectWorkspaceEntryPayload is the wire shape for one membership.
// Kept JSON-tagged here rather than reusing the generated type so the
// handler can set fields by name without aligning to openapi-codegen's
// nullability quirks for the embedded enums.
type projectWorkspaceEntryPayload struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	RepoID        string `json:"repo_id"`
	Branch        string `json:"branch"`
	Status        string `json:"status"`
	IsLinked      bool   `json:"is_linked"`
}

// ListProjectWorkspaces — GET /api/v1/projects/{path}/workspaces.
//
// Returns every workspace that has this project attached. Used by the
// project detail page to render "Workspaces" chips linking to each
// workspace. Empty list when the project isn't part of any workspace.
//
// The workspaces feature flag is NOT consulted here: even if workspaces
// are disabled, returning an empty membership list is the right
// response — the project page should still render cleanly.
func (s *Server) ListProjectWorkspaces(w http.ResponseWriter, r *http.Request, path openapi.ProjectHash) {
	hash := string(path)

	// Resolve the project first so 404 vs empty membership are clearly
	// distinguishable: unknown hash → 404; known hash with zero
	// memberships → 200 with workspaces=[].
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
		SELECT w.id, w.name, wr.id, wr.branch, wr.status, wr.is_linked
		  FROM workspaces w
		  JOIN workspace_repos wr ON wr.workspace_id = w.id
		 WHERE wr.project_path = ?
		 ORDER BY w.name`, proj.HostPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspaces: "+err.Error())
		return
	}
	defer rows.Close()

	entries := []projectWorkspaceEntryPayload{}
	for rows.Next() {
		var (
			e        projectWorkspaceEntryPayload
			isLinked int
		)
		if scanErr := rows.Scan(&e.WorkspaceID, &e.WorkspaceName, &e.RepoID, &e.Branch, &e.Status, &isLinked); scanErr != nil {
			writeError(w, http.StatusInternalServerError, "could not read row: "+scanErr.Error())
			return
		}
		e.IsLinked = isLinked == 1
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
