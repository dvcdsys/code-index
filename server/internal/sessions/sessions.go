// Package sessions implements the dashboard's cookie-backed login sessions.
//
// A session is identified by a 256-bit random token. The token itself is
// only ever known to the user's browser (it lives in the cix_session
// HttpOnly cookie); the database stores sha256(token) so a leaked snapshot
// cannot be used to impersonate active sessions. Every other API in this
// package speaks the public hash id — generated at Create, returned by
// ListForUser, and accepted by Touch/Delete — so callers do not need to
// hold the raw token after issuing the cookie.
//
// Rolling expiry: every Touch pushes expires_at out by SessionTTL. This
// matches typical browser-tab usage (you stay logged in as long as you
// keep using it) without long-lived bearer tokens.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/dvcdsys/code-index/server/internal/users"
)

// SessionTTL is the rolling lifetime of a session: how long after the
// last request before the cookie expires. 14 days matches typical
// "stay signed in" behaviour for an internal admin tool.
const SessionTTL = 14 * 24 * time.Hour

// CookieName is the name of the HTTP cookie carrying the raw session token.
const CookieName = "cix_session"

var (
	ErrNotFound = errors.New("session not found")
	ErrExpired  = errors.New("session expired")
	ErrDisabled = errors.New("user account is disabled")
)

// Session is a single login session with its associated user attached.
// ID is the public hash id (sha256-hex of the raw token). The raw token
// is only ever returned by Create — every other call works with the hash.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	LastSeenIP string
	LastSeenUA string
}

// Created is the bundle returned by Create: the persisted Session (with
// its public hash id) plus the raw token the caller must put in the
// cookie. The raw token is never persisted — losing it means the user
// can never reuse this session, which is the whole point.
type Created struct {
	Session  Session
	RawToken string
}

// Service wraps the sessions table.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create issues a new session for userID. The returned RawToken is what
// must be set in the browser cookie; only sha256(RawToken) hits the DB.
func (s *Service) Create(ctx context.Context, userID, ip, ua string) (Created, error) {
	raw, err := newRawToken()
	if err != nil {
		return Created{}, fmt.Errorf("generate session token: %w", err)
	}
	hash := HashToken(raw)
	now := time.Now().UTC()
	exp := now.Add(SessionTTL)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, last_seen_ip, last_seen_ua)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hash, userID,
		now.Format(time.RFC3339Nano),
		exp.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		nullableString(ip), nullableString(ua),
	)
	if err != nil {
		return Created{}, fmt.Errorf("insert session: %w", err)
	}
	return Created{
		Session: Session{
			ID:         hash,
			UserID:     userID,
			CreatedAt:  now,
			ExpiresAt:  exp,
			LastSeenAt: now,
			LastSeenIP: ip,
			LastSeenUA: ua,
		},
		RawToken: raw,
	}, nil
}

// Get looks up a session by the raw cookie token (the value the browser
// sends back on every request). Internally hashes the token and queries
// by hash. Returns ErrNotFound for unknown tokens, ErrExpired when the
// session has timed out (Get also deletes expired rows opportunistically),
// and ErrDisabled when the user has been disabled since the session was
// created.
func (s *Service) Get(ctx context.Context, rawToken string) (Session, users.User, error) {
	if rawToken == "" {
		return Session{}, users.User{}, ErrNotFound
	}
	hash := HashToken(rawToken)
	row := s.DB.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.created_at, s.expires_at, s.last_seen_at, s.last_seen_ip, s.last_seen_ua,
		        u.email, u.role, u.must_change_password, u.created_at, u.updated_at, u.disabled_at
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		  WHERE s.id = ?`, hash)

	var (
		sess                             Session
		ip, ua                           sql.NullString
		createdAt, expiresAt, lastSeenAt string
		uEmail, uRole                    string
		uMcp                             int
		uCreatedAt, uUpdatedAt           string
		uDisabledAt                      sql.NullString
	)
	err := row.Scan(
		&sess.ID, &sess.UserID, &createdAt, &expiresAt, &lastSeenAt, &ip, &ua,
		&uEmail, &uRole, &uMcp, &uCreatedAt, &uUpdatedAt, &uDisabledAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, users.User{}, ErrNotFound
		}
		return Session{}, users.User{}, fmt.Errorf("scan session: %w", err)
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	sess.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	sess.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeenAt)
	sess.LastSeenIP = ip.String
	sess.LastSeenUA = ua.String

	if time.Now().UTC().After(sess.ExpiresAt) {
		// Garbage-collect this row so it can't be re-tried. Best-effort:
		// failure to delete is fine, the next GC pass will catch it.
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID)
		return Session{}, users.User{}, ErrExpired
	}

	u := users.User{
		ID:                 sess.UserID,
		Email:              uEmail,
		Role:               uRole,
		MustChangePassword: uMcp == 1,
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, uCreatedAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, uUpdatedAt)
	if uDisabledAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, uDisabledAt.String)
		u.DisabledAt = &t
		return Session{}, users.User{}, ErrDisabled
	}
	return sess, u, nil
}

// Touch slides expires_at forward by SessionTTL and refreshes the last-seen
// metadata. Called by middleware on every authenticated request; the
// id argument is the public hash id (Session.ID), not the raw token.
func (s *Service) Touch(ctx context.Context, id, ip, ua string) error {
	now := time.Now().UTC()
	exp := now.Add(SessionTTL)
	_, err := s.DB.ExecContext(ctx,
		`UPDATE sessions
		    SET expires_at = ?, last_seen_at = ?, last_seen_ip = ?, last_seen_ua = ?
		  WHERE id = ?`,
		exp.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		nullableString(ip), nullableString(ua),
		id,
	)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes a single session (logout-from-this-device). The id is
// the public hash id.
func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteAllForUser wipes every session of a user.
func (s *Service) DeleteAllForUser(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// DeleteAllForUserExcept is like DeleteAllForUser but keeps one session
// alive (typically the one carrying the password-change request itself).
// keepID is the public hash id.
func (s *Service) DeleteAllForUserExcept(ctx context.Context, userID, keepID string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keepID)
	return err
}

// ListForUser returns active (non-expired) sessions for a user, newest
// first. Used by the Settings page to show "where am I logged in?".
// Session.ID is the public hash id; the raw tokens are never returned.
func (s *Service) ListForUser(ctx context.Context, userID string) ([]Session, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, user_id, created_at, expires_at, last_seen_at, last_seen_ip, last_seen_ua
		   FROM sessions
		  WHERE user_id = ? AND expires_at > ?
		  ORDER BY last_seen_at DESC`, userID, now)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var (
			s                                Session
			ip, ua                           sql.NullString
			createdAt, expiresAt, lastSeenAt string
		)
		if err := rows.Scan(&s.ID, &s.UserID, &createdAt, &expiresAt, &lastSeenAt, &ip, &ua); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		s.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
		s.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeenAt)
		s.LastSeenIP = ip.String
		s.LastSeenUA = ua.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// GC deletes all expired sessions. Safe to run periodically. Returns the
// number of rows removed.
func (s *Service) GC(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("gc sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// HashToken returns the public id (sha256-hex) for a raw session token.
// Exposed so tests can verify that the cookie value never appears in the
// DB column.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// newRawToken returns a fresh 256-bit base64url-encoded random token.
func newRawToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
