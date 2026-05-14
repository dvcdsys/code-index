// Package workspaces is the service layer for the workspaces table — the
// top-level entity of the workspaces feature. A workspace groups one or
// more GitHub repos for cross-project semantic search powered by
// community-detection on the call graph (PRs 2–7 of the feature branch).
//
// PR1 scope: bare CRUD. workspace_repos / call_edges / communities land in
// later PRs. Visibility model is server-wide shared: every authenticated
// user can list/create/modify any workspace. The decision is captured in
// the workspaces.md plan; revisit if a per-user ACL becomes necessary.
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
	ErrNotFound         = errors.New("workspace not found")
	ErrNameTaken        = errors.New("workspace name already in use")
	ErrNameEmpty        = errors.New("workspace name is required")
	ErrDefaultProtected = errors.New("the default workspace cannot be deleted — rename it instead if you want a different label")
)

// Workspace is the metadata view. Pointers are NOT used for description
// because zero-string "" is the desired absent representation (the column
// is nullable but the JSON shape sends "" — see openapi.yaml).
type Workspace struct {
	ID          string
	Name        string
	Description string
	// IsDefault marks the singleton workspace that owns repos added via
	// the standalone /git-repos endpoint. Exactly one row should carry
	// it (enforced by a partial UNIQUE index on the DB side). The
	// operator can rename this workspace freely; the flag is the source
	// of truth for "which workspace receives standalone Add repo".
	IsDefault   bool
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
		`SELECT id, name, description, is_default, created_at, updated_at
		   FROM workspaces WHERE id = ?`, id)
	return scanRow(row)
}

// GetDefault returns the singleton default workspace (the one with
// is_default=1). ErrNotFound when EnsureDefault has not yet run on
// this database — handlers should call EnsureDefault on startup or
// fall back to it on first /git-repos hit.
func (s *Service) GetDefault(ctx context.Context) (Workspace, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, name, description, is_default, created_at, updated_at
		   FROM workspaces WHERE is_default = 1 LIMIT 1`)
	return scanRow(row)
}

// EnsureDefault creates the singleton default workspace if it does
// not already exist, otherwise returns the existing one. Idempotent
// — safe to call from server startup on every boot. Name collisions
// with operator-created workspaces named "Personal" are resolved by
// appending a numeric suffix; the operator can rename the workspace
// later through the regular Update flow.
func (s *Service) EnsureDefault(ctx context.Context) (Workspace, error) {
	if existing, err := s.GetDefault(ctx); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Workspace{}, err
	}

	// Pick a non-colliding name. The vast majority of installs end at
	// "Personal" on the first try; the loop handles the (rare) case
	// where an operator already has a manually-created workspace named
	// "Personal" — we don't want to steal it.
	base := "Personal"
	name := base
	for attempt := 1; attempt < 50; attempt++ {
		id := uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := s.DB.ExecContext(ctx,
			`INSERT INTO workspaces (id, name, description, is_default, created_at, updated_at)
			 VALUES (?, ?, ?, 1, ?, ?)`,
			id, name, nullableString("Standalone projects added from the Projects page."), now, now,
		)
		if err == nil {
			return s.GetByID(ctx, id)
		}
		if !isUniqueConstraintViolation(err) {
			return Workspace{}, fmt.Errorf("insert default workspace: %w", err)
		}
		// UNIQUE could be on the name (collision) or on is_default
		// (another goroutine raced us). If a default exists now,
		// return it; otherwise bump the name suffix and retry.
		if existing, ferr := s.GetDefault(ctx); ferr == nil {
			return existing, nil
		}
		name = fmt.Sprintf("%s (%d)", base, attempt+1)
	}
	return Workspace{}, fmt.Errorf("could not allocate a unique name for the default workspace after 50 attempts")
}

// List returns every workspace, newest first.
func (s *Service) List(ctx context.Context) ([]Workspace, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, description, is_default, created_at, updated_at
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
// The default workspace is protected: removing it would orphan the
// /git-repos endpoint until the next boot's EnsureDefault. Callers get
// ErrDefaultProtected and decide how to surface it (typically 409).
func (s *Service) Delete(ctx context.Context, id string) error {
	if w, err := s.GetByID(ctx, id); err == nil && w.IsDefault {
		return ErrDefaultProtected
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
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
		w           Workspace
		description sql.NullString
		isDefault   int
		createdAt   string
		updatedAt   string
	)
	err := r.Scan(&w.ID, &w.Name, &description, &isDefault, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("scan workspace: %w", err)
	}
	w.Description = description.String
	w.IsDefault = isDefault == 1
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
