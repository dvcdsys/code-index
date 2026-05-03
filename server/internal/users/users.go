// Package users implements the dashboard's user-account model: email +
// bcrypt password + role. Replaces the old single-CIX_API_KEY auth, which
// could not distinguish actors. CLI access still flows through Bearer
// tokens, but those tokens now belong to a user via internal/apikeys.
package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Roles. Kept open-coded (string constants) rather than a typed enum so
// SQL queries and HTTP handlers can compare without import gymnastics.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// BcryptCost is the work factor for password hashing. 12 is the current
// industry default — tunable here without touching call sites if the
// hardware moves.
const BcryptCost = 12

var (
	ErrNotFound       = errors.New("user not found")
	ErrEmailTaken     = errors.New("email already in use")
	ErrInvalidLogin   = errors.New("invalid email or password")
	ErrUserDisabled   = errors.New("user account is disabled")
	ErrLastAdminBlock = errors.New("cannot remove the last active admin")
	ErrInvalidRole    = errors.New("invalid role")
)

// User is the row shape returned by Service. password_hash never leaves
// the package — callers only see metadata + the role bit they need.
type User struct {
	ID                 string
	Email              string
	Role               string
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DisabledAt         *time.Time
}

// Service wraps the users table. Stateless — safe to share across handlers.
type Service struct {
	DB *sql.DB
}

// New returns a Service bound to db.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Count returns the total number of users (including disabled). Used by the
// bootstrap path in main.go to decide whether to seed an admin from env.
func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// Create inserts a new user with the given plaintext password (hashed
// here). mustChangePassword=true is the right call for any account whose
// initial password came from somewhere other than the user themselves
// (env bootstrap, admin invite).
func (s *Service) Create(ctx context.Context, email, password, role string, mustChangePassword bool) (User, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, fmt.Errorf("email required")
	}
	if !validRole(role) {
		return User{}, ErrInvalidRole
	}
	if password == "" {
		return User{}, fmt.Errorf("password required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mcp := 0
	if mustChangePassword {
		mcp = 1
	}

	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, role, must_change_password, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, email, string(hash), role, mcp, now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	return s.GetByID(ctx, id)
}

// GetByID returns a user by id. ErrNotFound when absent.
func (s *Service) GetByID(ctx context.Context, id string) (User, error) {
	return s.scanOne(ctx, `WHERE id = ?`, id)
}

// GetByEmail returns a user by email (case-insensitive). ErrNotFound when absent.
func (s *Service) GetByEmail(ctx context.Context, email string) (User, error) {
	return s.scanOne(ctx, `WHERE email = ? COLLATE NOCASE`, normalizeEmail(email))
}

// List returns every user, ordered oldest-first (admin UI usually wants
// stable ordering, and created_at is monotonic).
func (s *Service) List(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, listSelect+` ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Authenticate verifies email+password. Returns ErrInvalidLogin for any
// auth failure (bad password OR missing user) — never leak which one.
// Disabled accounts return ErrUserDisabled.
func (s *Service) Authenticate(ctx context.Context, email, password string) (User, error) {
	email = normalizeEmail(email)
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, password_hash, role, must_change_password, created_at, updated_at, disabled_at, email
		   FROM users WHERE email = ? COLLATE NOCASE`, email)

	var (
		u            User
		hash         string
		mcp          int
		disabledAt   sql.NullString
		createdAt    string
		updatedAt    string
		emailOut     string
	)
	if err := row.Scan(&u.ID, &hash, &u.Role, &mcp, &createdAt, &updatedAt, &disabledAt, &emailOut); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Match the timing of a hash-compare to mitigate user-enumeration
			// via response time. CompareHashAndPassword on a known-bad hash
			// burns the same cost as a real login.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvali"), []byte(password))
			return User{}, ErrInvalidLogin
		}
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	if disabledAt.Valid {
		return User{}, ErrUserDisabled
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return User{}, ErrInvalidLogin
	}
	u.Email = emailOut
	u.MustChangePassword = mcp == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if disabledAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, disabledAt.String)
		u.DisabledAt = &t
	}
	return u, nil
}

// UpdatePassword sets a new password and clears must_change_password. The
// caller is responsible for invalidating any old sessions if desired —
// see internal/sessions DeleteAllForUser.
func (s *Service) UpdatePassword(ctx context.Context, id, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("new password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), BcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, must_change_password = 0, updated_at = ? WHERE id = ?`,
		string(hash), now, id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRole changes a user's role. Refuses to demote the last active admin
// to keep the system reachable.
func (s *Service) SetRole(ctx context.Context, id, role string) error {
	if !validRole(role) {
		return ErrInvalidRole
	}
	if role != RoleAdmin {
		if err := s.guardLastAdmin(ctx, id); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.ExecContext(ctx,
		`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, now, id)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDisabled flips the disabled flag. Disabled users cannot authenticate.
// Refuses to disable the last active admin (mirrors SetRole's guard).
func (s *Service) SetDisabled(ctx context.Context, id string, disabled bool) error {
	if disabled {
		if err := s.guardLastAdmin(ctx, id); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var res sql.Result
	var err error
	if disabled {
		res, err = s.DB.ExecContext(ctx,
			`UPDATE users SET disabled_at = ?, updated_at = ? WHERE id = ?`, now, now, id)
	} else {
		res, err = s.DB.ExecContext(ctx,
			`UPDATE users SET disabled_at = NULL, updated_at = ? WHERE id = ?`, now, id)
	}
	if err != nil {
		return fmt.Errorf("update disabled_at: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a user (cascades to sessions + api_keys via FK).
// Refuses to delete the last active admin.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.guardLastAdmin(ctx, id); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// guardLastAdmin returns ErrLastAdminBlock if id is the only enabled
// admin in the system. Used by demotion / disable / delete to keep at
// least one admin reachable.
func (s *Service) guardLastAdmin(ctx context.Context, id string) error {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Role != RoleAdmin || u.DisabledAt != nil {
		return nil
	}
	var n int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&n); err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if n <= 1 {
		return ErrLastAdminBlock
	}
	return nil
}

// --- helpers ---

const listSelect = `SELECT id, email, role, must_change_password, created_at, updated_at, disabled_at FROM users`

func (s *Service) scanOne(ctx context.Context, where string, args ...any) (User, error) {
	row := s.DB.QueryRowContext(ctx, listSelect+" "+where, args...)
	u, err := scanUserRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUserRow(r rowScanner) (User, error) {
	var (
		u                       User
		mcp                     int
		createdAt, updatedAt    string
		disabledAt              sql.NullString
	)
	if err := r.Scan(&u.ID, &u.Email, &u.Role, &mcp, &createdAt, &updatedAt, &disabledAt); err != nil {
		return User{}, err
	}
	u.MustChangePassword = mcp == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if disabledAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, disabledAt.String)
		u.DisabledAt = &t
	}
	return u, nil
}

func normalizeEmail(s string) string { return strings.TrimSpace(strings.ToLower(s)) }

func validRole(r string) bool { return r == RoleAdmin || r == RoleViewer }

// isUniqueViolation matches modernc.org/sqlite's UNIQUE-constraint error
// without taking a hard import dependency on its error type. The driver
// formats these as "constraint failed: UNIQUE constraint failed: ...".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
