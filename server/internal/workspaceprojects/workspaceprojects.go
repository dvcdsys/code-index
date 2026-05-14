// Package workspaceprojects is the service layer for the
// workspace_projects junction table — the many-to-many mapping between
// workspaces and projects. A row exists when a project is currently a
// member of a workspace; Link adds one, Unlink removes one. The project
// itself is unaffected by either operation (deletion happens through
// the projects service, which cascades here via FK ON DELETE CASCADE).
//
// This is the only place in the codebase that knows how workspace
// membership is represented. The gitrepos service knows about clone
// metadata; the projects service knows about indexed content; this
// service knows about "which workspace holds which project".
package workspaceprojects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors.
var (
	ErrNotFound           = errors.New("workspace project membership not found")
	ErrDuplicate          = errors.New("project is already linked to this workspace")
	ErrProjectNotIndexed  = errors.New("project is not yet indexed — wait for indexing before linking")
	ErrProjectMissing     = errors.New("project does not exist")
	ErrWorkspaceMissing   = errors.New("workspace does not exist")
)

// Membership is the wire-friendly row shape.
type Membership struct {
	WorkspaceID string
	ProjectPath string
	AddedAt     time.Time
}

// Service wraps the workspace_projects table.
type Service struct {
	DB *sql.DB
}

func New(db *sql.DB) *Service { return &Service{DB: db} }

// Link inserts (workspace_id, project_path) into workspace_projects.
// The project must exist and be in status='indexed' so workspace search
// has something to fan out to. Duplicates return ErrDuplicate; missing
// targets return ErrWorkspaceMissing / ErrProjectMissing.
func (s *Service) Link(ctx context.Context, workspaceID, projectPath string) (Membership, error) {
	// Existence checks up front so the caller gets a precise error
	// instead of a raw FK violation.
	var wsCount int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE id = ?`, workspaceID,
	).Scan(&wsCount); err != nil {
		return Membership{}, fmt.Errorf("check workspace: %w", err)
	}
	if wsCount == 0 {
		return Membership{}, ErrWorkspaceMissing
	}

	var status sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT status FROM projects WHERE host_path = ?`, projectPath,
	).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Membership{}, ErrProjectMissing
		}
		return Membership{}, fmt.Errorf("check project: %w", err)
	}
	if status.String != "indexed" {
		return Membership{}, ErrProjectNotIndexed
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO workspace_projects (workspace_id, project_path, added_at)
		VALUES (?, ?, ?)`,
		workspaceID, projectPath, now,
	); err != nil {
		if isUniqueConstraintViolation(err) {
			return Membership{}, ErrDuplicate
		}
		return Membership{}, fmt.Errorf("insert workspace_project: %w", err)
	}
	addedAt, _ := time.Parse(time.RFC3339Nano, now)
	return Membership{
		WorkspaceID: workspaceID,
		ProjectPath: projectPath,
		AddedAt:     addedAt,
	}, nil
}

// Unlink removes a (workspace_id, project_path) row. Returns ErrNotFound
// when no such row exists so the handler can distinguish "deleted" from
// "wasn't there" — both can become 204 in the API but tests need the
// signal.
func (s *Service) Unlink(ctx context.Context, workspaceID, projectPath string) error {
	res, err := s.DB.ExecContext(ctx, `
		DELETE FROM workspace_projects
		 WHERE workspace_id = ? AND project_path = ?`,
		workspaceID, projectPath)
	if err != nil {
		return fmt.Errorf("delete workspace_project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByWorkspace returns every project_path attached to a workspace,
// newest membership first.
func (s *Service) ListByWorkspace(ctx context.Context, workspaceID string) ([]Membership, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT workspace_id, project_path, added_at
		  FROM workspace_projects
		 WHERE workspace_id = ?
		 ORDER BY added_at DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace_projects by workspace: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListByProject returns every workspace this project participates in.
// Used by the project detail page to render "Workspaces" chips.
func (s *Service) ListByProject(ctx context.Context, projectPath string) ([]Membership, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT wp.workspace_id, wp.project_path, wp.added_at
		  FROM workspace_projects wp
		 WHERE wp.project_path = ?
		 ORDER BY wp.added_at DESC`, projectPath)
	if err != nil {
		return nil, fmt.Errorf("list workspace_projects by project: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// --- helpers ---

func scanRows(rows *sql.Rows) ([]Membership, error) {
	out := []Membership{}
	for rows.Next() {
		var (
			m         Membership
			addedAt   string
		)
		if err := rows.Scan(&m.WorkspaceID, &m.ProjectPath, &addedAt); err != nil {
			return nil, fmt.Errorf("scan workspace_project: %w", err)
		}
		m.AddedAt, _ = time.Parse(time.RFC3339Nano, addedAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE") ||
		strings.Contains(msg, "constraint failed: PRIMARY KEY")
}
