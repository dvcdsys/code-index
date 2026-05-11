// Package githubtokens manages GitHub Personal Access Tokens used by the
// workspaces feature to clone private repositories and (optionally) register
// webhooks. Tokens are encrypted at rest via internal/secrets — the plaintext
// is supplied by the dashboard exactly once on POST /api/v1/github-tokens
// and never returned in any subsequent response.
//
// Server-wide shared model: any authenticated user may create / select / delete
// tokens. Track of "owner" is omitted in PR1 by design — when the feature
// matures we'll re-introduce ownership if multi-team semantics are wanted.
//
// Scope validation against the real GitHub API is deferred to PR2 (which
// brings in the go-github client). PR1 stores `scopes` as a free-form JSON
// string so the field is stable in the schema.
package githubtokens

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/secrets"
)

// Errors.
var (
	ErrNotFound  = errors.New("github token not found")
	ErrNameTaken = errors.New("github token name already in use")
	ErrNameEmpty = errors.New("github token name is required")
	ErrEmpty     = errors.New("github token value is required")
)

// Token is the metadata view. Plaintext is NEVER stored in this struct — use
// Reveal() to obtain it for an outbound API call (e.g. webhook registration).
type Token struct {
	ID         string
	Name       string
	Scopes     []string  // best-effort; empty until validated by the GitHub API in PR2
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Service wraps the github_tokens table + secrets.Service for encryption.
type Service struct {
	DB      *sql.DB
	Secrets *secrets.Service
}

// New returns a Service. Both DB and Secrets are required.
func New(db *sql.DB, sec *secrets.Service) *Service {
	return &Service{DB: db, Secrets: sec}
}

// Create stores a new token. The plaintext value is encrypted via
// Secrets.Encrypt and never persisted in cleartext. Returns the metadata
// view — the caller already has the plaintext and is responsible for not
// echoing it back to the user.
func (s *Service) Create(ctx context.Context, name, plaintext string, scopes []string) (Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Token{}, ErrNameEmpty
	}
	if strings.TrimSpace(plaintext) == "" {
		return Token{}, ErrEmpty
	}
	if s.Secrets == nil {
		return Token{}, fmt.Errorf("secrets service not configured")
	}

	encrypted, err := s.Secrets.Encrypt([]byte(plaintext))
	if err != nil {
		return Token{}, fmt.Errorf("encrypt: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	scopesJSON, err := encodeScopes(scopes)
	if err != nil {
		return Token{}, err
	}

	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO github_tokens (id, name, encrypted, scopes, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, name, encrypted, nullableString(scopesJSON), now,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return Token{}, ErrNameTaken
		}
		return Token{}, fmt.Errorf("insert github token: %w", err)
	}
	return s.GetByID(ctx, id)
}

// GetByID returns metadata for a token. Plaintext is not loaded — use
// Reveal for that.
func (s *Service) GetByID(ctx context.Context, id string) (Token, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, name, scopes, created_at, last_used_at
		   FROM github_tokens WHERE id = ?`, id)
	return scanRow(row)
}

// List returns all tokens, newest first. Plaintext is never loaded.
func (s *Service) List(ctx context.Context) ([]Token, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, scopes, created_at, last_used_at
		   FROM github_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list github tokens: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// Reveal loads the encrypted blob for a token and decrypts it. Used by the
// workspaces clone / webhook flows when an outbound GitHub API call needs
// the plaintext. ErrNotFound when the row is absent; decryption errors are
// wrapped to discourage oracle-style probing.
func (s *Service) Reveal(ctx context.Context, id string) (string, error) {
	if s.Secrets == nil {
		return "", fmt.Errorf("secrets service not configured")
	}
	row := s.DB.QueryRowContext(ctx, `SELECT encrypted FROM github_tokens WHERE id = ?`, id)
	var enc []byte
	if err := row.Scan(&enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("load github token: %w", err)
	}
	plain, err := s.Secrets.Decrypt(enc)
	if err != nil {
		return "", fmt.Errorf("decrypt github token: %w", err)
	}
	return string(plain), nil
}

// Touch updates last_used_at on every successful Reveal-callsite. Not
// invoked by Reveal itself because some callers (e.g. background cloners)
// hit the same token many times in a tight loop and we'd rather take one
// timestamp at the end of the batch.
func (s *Service) Touch(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx,
		`UPDATE github_tokens SET last_used_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("touch github token: %w", err)
	}
	return nil
}

// Delete removes a token. ErrNotFound when absent.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM github_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete github token: %w", err)
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

// CountWithEncryption returns the number of rows in github_tokens. Used at
// startup to decide whether a missing encryption key is fatal (any encrypted
// row would otherwise be unreadable).
func (s *Service) CountWithEncryption(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM github_tokens`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count github tokens: %w", err)
	}
	return n, nil
}

// --- helpers ---

func scanRow(r interface{ Scan(dest ...any) error }) (Token, error) {
	var (
		t          Token
		scopesJSON sql.NullString
		createdAt  string
		lastUsedAt sql.NullString
	)
	err := r.Scan(&t.ID, &t.Name, &scopesJSON, &createdAt, &lastUsedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, ErrNotFound
		}
		return Token{}, fmt.Errorf("scan github token: %w", err)
	}
	t.Scopes = decodeScopes(scopesJSON.String)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsedAt.Valid {
		ts, _ := time.Parse(time.RFC3339Nano, lastUsedAt.String)
		t.LastUsedAt = &ts
	}
	return t, nil
}

func scanRows(rows *sql.Rows) ([]Token, error) {
	out := []Token{}
	for rows.Next() {
		t, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func encodeScopes(scopes []string) (string, error) {
	if len(scopes) == 0 {
		return "", nil
	}
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", fmt.Errorf("encode scopes: %w", err)
	}
	return string(b), nil
}

func decodeScopes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
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
