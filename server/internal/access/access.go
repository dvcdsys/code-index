// Package access resolves the auth model's resource visibility: who can see a
// project or workspace, and the share junctions (project_group_shares,
// workspace_group_shares) that grant view-group members read access.
//
// These helpers express the rules for a REGULAR user. Admins bypass them
// entirely (they see everything) — the HTTP layer checks role first and only
// calls in here for non-admins.
//
// Access rule for a project P / user U (admin already handled):
//   - P.owner_user_id == U   → full access (personal project)
//   - P shared to a view-group U belongs to → read/search (external project)
//   - else → hidden
//
// Workspace W / user U: visible if owner or shared to a group U belongs to.
// Per-project access is always re-checked when listing/searching a workspace,
// so a hidden project is simply dropped from the result.
package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// --- project share CRUD ---

// ShareProjectToGroup grants a view-group read access to an external project.
// Idempotent (INSERT OR IGNORE).
func ShareProjectToGroup(ctx context.Context, db *sql.DB, projectPath, groupID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO project_group_shares (project_path, group_id, created_at)
		 VALUES (?, ?, ?)`, projectPath, groupID, now)
	if err != nil {
		return fmt.Errorf("share project to group: %w", err)
	}
	return nil
}

// UnshareProjectFromGroup revokes a project↔group share. Idempotent.
func UnshareProjectFromGroup(ctx context.Context, db *sql.DB, projectPath, groupID string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM project_group_shares WHERE project_path = ? AND group_id = ?`,
		projectPath, groupID)
	if err != nil {
		return fmt.Errorf("unshare project from group: %w", err)
	}
	return nil
}

// ListProjectShareGroupIDs returns the group ids a project is shared to.
func ListProjectShareGroupIDs(ctx context.Context, db *sql.DB, projectPath string) ([]string, error) {
	return queryIDs(ctx, db,
		`SELECT group_id FROM project_group_shares WHERE project_path = ? ORDER BY created_at ASC`,
		projectPath)
}

// --- workspace share CRUD ---

// ShareWorkspaceToGroup shares a workspace with a view-group. Idempotent.
func ShareWorkspaceToGroup(ctx context.Context, db *sql.DB, workspaceID, groupID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO workspace_group_shares (workspace_id, group_id, created_at)
		 VALUES (?, ?, ?)`, workspaceID, groupID, now)
	if err != nil {
		return fmt.Errorf("share workspace to group: %w", err)
	}
	return nil
}

// UnshareWorkspaceFromGroup revokes a workspace↔group share. Idempotent.
func UnshareWorkspaceFromGroup(ctx context.Context, db *sql.DB, workspaceID, groupID string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM workspace_group_shares WHERE workspace_id = ? AND group_id = ?`,
		workspaceID, groupID)
	if err != nil {
		return fmt.Errorf("unshare workspace from group: %w", err)
	}
	return nil
}

// ListWorkspaceShareGroupIDs returns the group ids a workspace is shared to.
func ListWorkspaceShareGroupIDs(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	return queryIDs(ctx, db,
		`SELECT group_id FROM workspace_group_shares WHERE workspace_id = ? ORDER BY created_at ASC`,
		workspaceID)
}

// --- resolution ---

// ProjectSharedToUser reports whether the project is shared to any view-group
// the user belongs to.
func ProjectSharedToUser(ctx context.Context, db *sql.DB, projectPath, userID string) (bool, error) {
	return exists(ctx, db, `
		SELECT 1
		  FROM project_group_shares s
		  JOIN view_group_members m ON m.group_id = s.group_id
		 WHERE s.project_path = ? AND m.user_id = ?
		 LIMIT 1`, projectPath, userID)
}

// WorkspaceSharedToUser reports whether the workspace is shared to any
// view-group the user belongs to.
func WorkspaceSharedToUser(ctx context.Context, db *sql.DB, workspaceID, userID string) (bool, error) {
	return exists(ctx, db, `
		SELECT 1
		  FROM workspace_group_shares s
		  JOIN view_group_members m ON m.group_id = s.group_id
		 WHERE s.workspace_id = ? AND m.user_id = ?
		 LIMIT 1`, workspaceID, userID)
}

// IsProjectExternal reports whether a project has a git_repos peer (i.e. is an
// external, server-cloned repo rather than a personal local project).
func IsProjectExternal(ctx context.Context, db *sql.DB, projectPath string) (bool, error) {
	return exists(ctx, db,
		`SELECT 1 FROM git_repos WHERE project_path = ? LIMIT 1`, projectPath)
}

// AccessibleProjectHostPaths returns the host_paths a regular user may see:
// the projects they own UNION the external projects shared to a group they
// belong to. Used to filter the project list.
func AccessibleProjectHostPaths(ctx context.Context, db *sql.DB, userID string) ([]string, error) {
	return queryIDs(ctx, db, `
		SELECT host_path FROM projects WHERE owner_user_id = ?
		UNION
		SELECT s.project_path
		  FROM project_group_shares s
		  JOIN view_group_members m ON m.group_id = s.group_id
		 WHERE m.user_id = ?`, userID, userID)
}

// VisibleWorkspaceIDs returns the workspace ids a regular user may see: the
// workspaces they own UNION the workspaces shared to a group they belong to.
func VisibleWorkspaceIDs(ctx context.Context, db *sql.DB, userID string) ([]string, error) {
	return queryIDs(ctx, db, `
		SELECT id FROM workspaces WHERE owner_user_id = ?
		UNION
		SELECT s.workspace_id
		  FROM workspace_group_shares s
		  JOIN view_group_members m ON m.group_id = s.group_id
		 WHERE m.user_id = ?`, userID, userID)
}

// --- helpers ---

func exists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("exists check: %w", err)
	}
	return true, nil
}

func queryIDs(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query ids: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
