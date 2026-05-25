// Package groups is the service layer for view-groups: admin-managed sets of
// users used to share external projects and workspaces. A user who belongs to
// a group gets read/search access to whatever is shared to that group.
//
// Scope: group CRUD + membership. The share junctions (project_group_shares,
// workspace_group_shares) and the access resolution that consumes them live
// with the projects/workspaces access layer, not here.
package groups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound  = errors.New("group not found")
	ErrNameTaken = errors.New("group name already in use")
	ErrNameEmpty = errors.New("group name is required")
	// ErrUserNotFound is returned by AddMember when the target user does not
	// exist (the FK insert fails).
	ErrUserNotFound = errors.New("user not found")
)

// Group is the metadata view of a view-group.
type Group struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Member is one row of a group's membership, joined with the user for display.
type Member struct {
	UserID  string
	Email   string
	Role    string
	AddedAt time.Time
}

// Service wraps the view_groups + view_group_members tables.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create inserts a new group. Name must be non-empty and unique.
func (s *Service) Create(ctx context.Context, name, description string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Group{}, ErrNameEmpty
	}
	description = strings.TrimSpace(description)

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO view_groups (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, name, nullableString(description), now, now,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("insert group: %w", err)
	}
	return s.GetByID(ctx, id)
}

// GetByID returns one group. ErrNotFound when absent.
func (s *Service) GetByID(ctx context.Context, id string) (Group, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, name, description, created_at, updated_at
		   FROM view_groups WHERE id = ?`, id)
	return scanRow(row)
}

// List returns every group, newest first.
func (s *Service) List(ctx context.Context) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, description, created_at, updated_at
		   FROM view_groups ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListForUser returns the groups the given user is a member of, newest first.
// Backs the non-admin GET /groups (so the workspace-share picker can list the
// caller's groups) and the access checks.
func (s *Service) ListForUser(ctx context.Context, userID string) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT g.id, g.name, g.description, g.created_at, g.updated_at
		   FROM view_groups g
		   JOIN view_group_members m ON m.group_id = g.id
		  WHERE m.user_id = ?
		  ORDER BY g.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list groups for user: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// Update changes name/description. Pointer-nil = leave alone.
func (s *Service) Update(ctx context.Context, id string, name *string, description *string) (Group, error) {
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return Group{}, err
	}
	newName := current.Name
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return Group{}, ErrNameEmpty
		}
		newName = trimmed
	}
	newDesc := current.Description
	if description != nil {
		newDesc = strings.TrimSpace(*description)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.ExecContext(ctx,
		`UPDATE view_groups SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		newName, nullableString(newDesc), now, id)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("update group: %w", err)
	}
	return s.GetByID(ctx, id)
}

// Delete removes a group (cascades to memberships and shares). ErrNotFound
// when absent so the handler can choose 404 vs 204.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM view_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
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

// AddMember adds a user to a group. Idempotent (INSERT OR IGNORE). Returns
// ErrNotFound if the group is absent, ErrUserNotFound if the user is absent.
func (s *Service) AddMember(ctx context.Context, groupID, userID string) error {
	if _, err := s.GetByID(ctx, groupID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx,
		`INSERT OR IGNORE INTO view_group_members (group_id, user_id, added_at)
		 VALUES (?, ?, ?)`, groupID, userID, now)
	if err != nil {
		if isForeignKeyViolation(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("add group member: %w", err)
	}
	return nil
}

// RemoveMember removes a user from a group. Idempotent — removing an absent
// membership is a no-op (no error).
func (s *Service) RemoveMember(ctx context.Context, groupID, userID string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM view_group_members WHERE group_id = ? AND user_id = ?`,
		groupID, userID)
	if err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	return nil
}

// ListMembers returns the members of a group joined with user email/role.
func (s *Service) ListMembers(ctx context.Context, groupID string) ([]Member, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT u.id, u.email, u.role, m.added_at
		   FROM view_group_members m
		   JOIN users u ON u.id = m.user_id
		  WHERE m.group_id = ?
		  ORDER BY m.added_at ASC`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var (
			m       Member
			addedAt string
		)
		if err := rows.Scan(&m.UserID, &m.Email, &m.Role, &addedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.AddedAt, _ = time.Parse(time.RFC3339Nano, addedAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

// IsMember reports whether a user belongs to a group.
func (s *Service) IsMember(ctx context.Context, userID, groupID string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM view_group_members WHERE group_id = ? AND user_id = ?`,
		groupID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is member: %w", err)
	}
	return true, nil
}

// --- helpers ---

func scanRow(r interface{ Scan(dest ...any) error }) (Group, error) {
	var (
		g           Group
		description sql.NullString
		createdAt   string
		updatedAt   string
	)
	err := r.Scan(&g.ID, &g.Name, &description, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Group{}, ErrNotFound
		}
		return Group{}, fmt.Errorf("scan group: %w", err)
	}
	g.Description = description.String
	g.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	g.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return g, nil
}

func scanRows(rows *sql.Rows) ([]Group, error) {
	out := []Group{}
	for rows.Next() {
		g, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "FOREIGN KEY constraint failed") ||
		strings.Contains(msg, "constraint failed: FOREIGN KEY")
}
