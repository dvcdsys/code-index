// Package workspaces is the service layer for the workspaces table — the
// top-level entity of the workspaces feature. A workspace groups one or
// more projects (each backed by an indexed `projects` row, optionally
// with a `git_repos` peer for GitHub-cloned projects) for cross-project
// semantic search powered by hybrid BM25 + dense fan-out across the
// memberships in `workspace_projects`.
//
// Scope of this package: bare workspace CRUD. Membership lives in
// `workspaceprojects`; clone + webhook metadata lives in `gitrepos`.
// Visibility model is server-wide shared: every authenticated user can
// list/create/modify any workspace. The decision is captured in the
// workspaces.md plan; revisit if a per-user ACL becomes necessary.
package workspaces

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Errors. ErrNotFound is the not-found sentinel used by handlers; ErrNameTaken
// surfaces UNIQUE-name collisions so handlers can return 409 instead of 500.
var (
	ErrNotFound   = errors.New("workspace not found")
	ErrNameTaken  = errors.New("workspace name already in use")
	ErrNameEmpty  = errors.New("workspace name is required")
)

// Workspace is the metadata view. Pointers are NOT used for description
// because zero-string "" is the desired absent representation (the column
// is nullable but the JSON shape sends "" — see openapi.yaml).
type Workspace struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Service wraps the workspaces table.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create inserts a new workspace. Name must be non-empty and unique.
func (s *Service) Create(ctx context.Context, name, description string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, ErrNameEmpty
	}
	description = strings.TrimSpace(description)

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, name, nullableString(description), now, now,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Workspace{}, ErrNameTaken
		}
		return Workspace{}, fmt.Errorf("insert workspace: %w", err)
	}
	return s.GetByID(ctx, id)
}

// GetByID returns one workspace. ErrNotFound when absent.
func (s *Service) GetByID(ctx context.Context, id string) (Workspace, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at
		   FROM workspaces WHERE id = ?`, id)
	return scanRow(row)
}

// List returns every workspace, newest first.
func (s *Service) List(ctx context.Context) ([]Workspace, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, description, created_at, updated_at
		   FROM workspaces ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// Update accepts pointers so callers can express "leave this field alone".
// A pointer-to-empty-string clears description; nil keeps the prior value.
// Name nil = no change; name "" returns ErrNameEmpty.
func (s *Service) Update(ctx context.Context, id string, name *string, description *string) (Workspace, error) {
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return Workspace{}, err
	}
	newName := current.Name
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return Workspace{}, ErrNameEmpty
		}
		newName = trimmed
	}
	newDesc := current.Description
	if description != nil {
		newDesc = strings.TrimSpace(*description)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.ExecContext(ctx,
		`UPDATE workspaces SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		newName, nullableString(newDesc), now, id)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Workspace{}, ErrNameTaken
		}
		return Workspace{}, fmt.Errorf("update workspace: %w", err)
	}
	return s.GetByID(ctx, id)
}

// Delete removes a workspace. Idempotent — deleting an absent workspace
// returns ErrNotFound so the handler can choose between 404 and 204.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- helpers ---

func scanRow(r interface{ Scan(dest ...any) error }) (Workspace, error) {
	var (
		w               Workspace
		description     sql.NullString
		createdAt       string
		updatedAt       string
	)
	err := r.Scan(&w.ID, &w.Name, &description, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("scan workspace: %w", err)
	}
	w.Description = description.String
	w.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	w.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return w, nil
}

func scanRows(rows *sql.Rows) ([]Workspace, error) {
	out := []Workspace{}
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueConstraintViolation detects sqlite UNIQUE-failures by the prefix
// modernc.org/sqlite emits ("constraint failed: UNIQUE ..."). Brittle to a
// driver change but the canonical match used elsewhere in this codebase
// (e.g. users.Create) — keep this in sync with that pattern.
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
