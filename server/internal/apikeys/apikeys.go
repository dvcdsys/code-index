// Package apikeys implements named, owner-scoped API keys for CLI/SDK
// access. Replaces the single-CIX_API_KEY model with one row per issued
// key, each tied to a user. Plaintext keys are returned exactly once at
// Generate time; only sha256(key) is persisted, so a stolen DB never
// leaks live credentials.
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/users"
)

// KeyPrefix is the recognisable prefix every issued key starts with —
// makes accidental leaks easy to grep for in logs and source control
// (a la GitHub's "ghp_..." pattern).
const KeyPrefix = "cix_"

// PrefixDisplayLen is how many characters of the full key are stored
// verbatim in the prefix column for UI display ("cix_a1b2c3d4..."). Must
// be small enough that the displayed prefix is not itself sufficient to
// recover the key, but large enough that it's distinguishable in lists.
const PrefixDisplayLen = 12 // KeyPrefix("cix_") + 8 random hex chars

var (
	ErrNotFound       = errors.New("api key not found")
	ErrInvalidKey     = errors.New("invalid api key")
	ErrAlreadyRevoked = errors.New("api key already revoked")
	ErrUserDisabled   = errors.New("api key owner is disabled")
)

// ApiKey is the metadata view of a key. The plaintext value is only ever
// returned by Generate (in a separate string) — once issued, the server
// never sees the plaintext again.
type ApiKey struct {
	ID          string
	OwnerUserID string
	Name        string
	Prefix      string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedIP  string
	LastUsedUA  string
	RevokedAt   *time.Time
}

// Service wraps the api_keys table.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Generate issues a new key for ownerUserID. Returns (fullKey, ApiKey).
// Save the fullKey somewhere safe NOW — it will not be retrievable
// later. The ApiKey returned has prefix populated for UI display.
func (s *Service) Generate(ctx context.Context, ownerUserID, name string) (string, ApiKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ApiKey{}, fmt.Errorf("api key name required")
	}
	full, err := newKey()
	if err != nil {
		return "", ApiKey{}, fmt.Errorf("generate key: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	prefix := full[:PrefixDisplayLen]
	hash := hashKey(full)

	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO api_keys (id, owner_user_id, name, prefix, hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, ownerUserID, name, prefix, hash, now,
	)
	if err != nil {
		return "", ApiKey{}, fmt.Errorf("insert api key: %w", err)
	}

	ak, err := s.GetByID(ctx, id)
	if err != nil {
		return "", ApiKey{}, err
	}
	return full, ak, nil
}

// ImportLegacy seeds the table with an externally-provided key value
// (used once at bootstrap to migrate the single CIX_API_KEY env var into
// a real api_keys row). Same hashing as Generate; the only difference is
// that fullKey is supplied by the caller rather than freshly generated.
func (s *Service) ImportLegacy(ctx context.Context, ownerUserID, name, fullKey string) (ApiKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ApiKey{}, fmt.Errorf("api key name required")
	}
	if fullKey == "" {
		return ApiKey{}, fmt.Errorf("fullKey required")
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	prefix := fullKey
	if len(prefix) > PrefixDisplayLen {
		prefix = prefix[:PrefixDisplayLen]
	}
	hash := hashKey(fullKey)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO api_keys (id, owner_user_id, name, prefix, hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, ownerUserID, name, prefix, hash, now)
	if err != nil {
		return ApiKey{}, fmt.Errorf("insert legacy api key: %w", err)
	}
	return s.GetByID(ctx, id)
}

// Authenticate looks up a key by its plaintext value, returning the
// owning user if the key is valid and active. Constant-time within the
// hash compare; we rely on sha256 not bcrypt because keys are 256 bits
// of entropy and brute-forcing the hash is irrelevant.
func (s *Service) Authenticate(ctx context.Context, fullKey string) (users.User, ApiKey, error) {
	if !strings.HasPrefix(fullKey, KeyPrefix) {
		return users.User{}, ApiKey{}, ErrInvalidKey
	}
	hash := hashKey(fullKey)
	row := s.DB.QueryRowContext(ctx,
		`SELECT k.id, k.owner_user_id, k.name, k.prefix, k.created_at,
		        k.last_used_at, k.last_used_ip, k.last_used_ua, k.revoked_at,
		        u.email, u.role, u.must_change_password,
		        u.created_at, u.updated_at, u.disabled_at
		   FROM api_keys k
		   JOIN users u ON u.id = k.owner_user_id
		  WHERE k.hash = ?`, hash)

	var (
		ak                                  ApiKey
		createdAt                           string
		lastUsedAt, revokedAt               sql.NullString
		lastUsedIP, lastUsedUA              sql.NullString
		uEmail, uRole                       string
		uMcp                                int
		uCreatedAt, uUpdatedAt              string
		uDisabledAt                         sql.NullString
	)
	err := row.Scan(
		&ak.ID, &ak.OwnerUserID, &ak.Name, &ak.Prefix, &createdAt,
		&lastUsedAt, &lastUsedIP, &lastUsedUA, &revokedAt,
		&uEmail, &uRole, &uMcp, &uCreatedAt, &uUpdatedAt, &uDisabledAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return users.User{}, ApiKey{}, ErrInvalidKey
		}
		return users.User{}, ApiKey{}, fmt.Errorf("scan api key: %w", err)
	}
	ak.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsedAt.String)
		ak.LastUsedAt = &t
	}
	ak.LastUsedIP = lastUsedIP.String
	ak.LastUsedUA = lastUsedUA.String
	if revokedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, revokedAt.String)
		ak.RevokedAt = &t
		return users.User{}, ApiKey{}, ErrInvalidKey
	}

	u := users.User{
		ID:                 ak.OwnerUserID,
		Email:              uEmail,
		Role:               uRole,
		MustChangePassword: uMcp == 1,
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, uCreatedAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, uUpdatedAt)
	if uDisabledAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, uDisabledAt.String)
		u.DisabledAt = &t
		return users.User{}, ApiKey{}, ErrUserDisabled
	}
	return u, ak, nil
}

// GetByID returns one key. ErrNotFound when absent.
func (s *Service) GetByID(ctx context.Context, id string) (ApiKey, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, owner_user_id, name, prefix, created_at,
		        last_used_at, last_used_ip, last_used_ua, revoked_at
		   FROM api_keys WHERE id = ?`, id)
	return scanKeyRow(row)
}

// ListForOwner returns every key owned by a user, including revoked ones
// (UI fades them out — but the operator should see history).
func (s *Service) ListForOwner(ctx context.Context, ownerUserID string) ([]ApiKey, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, owner_user_id, name, prefix, created_at,
		        last_used_at, last_used_ip, last_used_ua, revoked_at
		   FROM api_keys WHERE owner_user_id = ? ORDER BY created_at DESC`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	return scanKeyRows(rows)
}

// ListAll is the admin view: every key in the system, newest first.
func (s *Service) ListAll(ctx context.Context) ([]ApiKey, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, owner_user_id, name, prefix, created_at,
		        last_used_at, last_used_ip, last_used_ua, revoked_at
		   FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all api keys: %w", err)
	}
	defer rows.Close()
	return scanKeyRows(rows)
}

// CountActiveForOwner returns how many non-revoked keys a user has.
// Used by bootstrap to decide whether to seed an env-imported key.
func (s *Service) CountActiveForOwner(ctx context.Context, ownerUserID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM api_keys WHERE owner_user_id = ? AND revoked_at IS NULL`,
		ownerUserID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count keys: %w", err)
	}
	return n, nil
}

// Revoke marks a key as revoked. Subsequent Authenticate calls fail with
// ErrInvalidKey. Idempotent — re-revoking returns ErrAlreadyRevoked but
// does not modify the row.
func (s *Service) Revoke(ctx context.Context, id string) error {
	ak, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if ak.RevokedAt != nil {
		return ErrAlreadyRevoked
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

// Touch updates the last-used metadata for a key. Called by middleware
// on every successful Bearer auth.
func (s *Service) Touch(ctx context.Context, id, ip, ua string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ?, last_used_ip = ?, last_used_ua = ? WHERE id = ?`,
		now, nullableString(ip), nullableString(ua), id)
	if err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}

// --- helpers ---

// newKey returns a fresh `cix_<43 random base64url chars>` token.
// 32 random bytes → 43 base64url chars = 256 bits of entropy. The
// length matches GitHub-class personal-access-token verbosity and
// puts brute-force comfortably out of reach for any attacker, on
// any timescale, even against a non-stretched hash. Older keys
// issued at the previous 192-bit length keep working — the hash
// column is the lookup key, not the length.
func newKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// hashKey returns hex(sha256(fullKey)). SHA-256 is the right
// primitive here — NOT bcrypt/argon2/PBKDF2 — because the pre-image
// is 256 bits of CSPRNG output (server-issued; never user-chosen).
// At that entropy floor, brute-forcing the hash is computationally
// indistinguishable from brute-forcing the underlying random bytes,
// and adding a slow KDF would only tax every authenticated request
// (~25–250 ms each at typical bcrypt costs) without raising the
// security floor a single bit. This is the same pattern GitHub /
// Stripe / AWS use for their API tokens. CodeQL's
// `go/insufficient-password-hash` rule is heuristic and treats any
// SHA-256 over a string-typed value as a password hash — that
// heuristic does not apply to high-entropy machine-issued tokens.
func hashKey(fullKey string) string {
	h := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(h[:])
}

func scanKeyRow(r interface {
	Scan(dest ...any) error
}) (ApiKey, error) {
	var (
		ak                       ApiKey
		createdAt                string
		lastUsedAt, revokedAt    sql.NullString
		lastUsedIP, lastUsedUA   sql.NullString
	)
	err := r.Scan(&ak.ID, &ak.OwnerUserID, &ak.Name, &ak.Prefix, &createdAt,
		&lastUsedAt, &lastUsedIP, &lastUsedUA, &revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApiKey{}, ErrNotFound
		}
		return ApiKey{}, err
	}
	ak.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsedAt.String)
		ak.LastUsedAt = &t
	}
	ak.LastUsedIP = lastUsedIP.String
	ak.LastUsedUA = lastUsedUA.String
	if revokedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, revokedAt.String)
		ak.RevokedAt = &t
	}
	return ak, nil
}

func scanKeyRows(rows *sql.Rows) ([]ApiKey, error) {
	var out []ApiKey
	for rows.Next() {
		ak, err := scanKeyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ak)
	}
	return out, rows.Err()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
