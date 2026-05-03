// Package sessions implements the dashboard's cookie-backed login sessions.
// A session is an opaque random id stored both as an HttpOnly cookie on
// the browser and a row in the sessions table. The id never grants access
// on its own — it must JOIN to a non-disabled user.
//
// Rolling expiry: every Touch pushes expires_at out by SessionTTL. This
// matches typical browser-tab usage (you stay logged in as long as you
// keep using it) without long-lived bearer tokens.
package sessions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/dvcdsys/code-index/server/internal/users"
)

// SessionTTL is the rolling lifetime of a session: how long after the
// last request before the cookie expires. 14 days matches typical
// "stay signed in" behaviour for an internal admin tool.
const SessionTTL = 14 * 24 * time.Hour

// CookieName is the name of the HTTP cookie carrying the session id.
const CookieName = "cix_session"

var (
	ErrNotFound = errors.New("session not found")
	ErrExpired  = errors.New("session expired")
	ErrDisabled = errors.New("user account is disabled")
)

// Session is a single login session with its associated user attached.
// Get returns Session+User in one call so handlers don't have to do the
// JOIN themselves.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	LastSeenIP string
	LastSeenUA string
}

// Service wraps the sessions table.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create issues a new session for userID, returning the opaque session
// id (this is what goes in the cookie). The id is 256 bits of CSPRNG
// output base64url-encoded.
func (s *Service) Create(ctx context.Context, userID, ip, ua string) (Session, error) {
	id, err := newSessionID()
	if err != nil {
		return Session{}, fmt.Errorf("generate session id: %w", err)
	}
	now := time.Now().UTC()
	exp := now.Add(SessionTTL)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, last_seen_ip, last_seen_ua)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, userID,
		now.Format(time.RFC3339Nano),
		exp.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		nullableString(ip), nullableString(ua),
	)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return Session{
		ID:         id,
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  exp,
		LastSeenAt: now,
		LastSeenIP: ip,
		LastSeenUA: ua,
	}, nil
}

// Get looks up a session by id and returns it along with the owning user.
// Returns ErrNotFound for unknown ids, ErrExpired when the session has
// timed out (Get also deletes expired rows opportunistically), and
// ErrDisabled when the user has been disabled since the session was
// created.
func (s *Service) Get(ctx context.Context, id string) (Session, users.User, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.created_at, s.expires_at, s.last_seen_at, s.last_seen_ip, s.last_seen_ua,
		        u.email, u.role, u.must_change_password, u.created_at, u.updated_at, u.disabled_at
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		  WHERE s.id = ?`, id)

	var (
		sess                              Session
		ip, ua                            sql.NullString
		createdAt, expiresAt, lastSeenAt  string
		uEmail, uRole                     string
		uMcp                              int
		uCreatedAt, uUpdatedAt            string
		uDisabledAt                       sql.NullString
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
// metadata. Called by middleware on every authenticated request.
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

// Delete removes a single session (logout-from-this-device).
func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteAllForUser wipes every session of a user. Use after a password
// change to log them out everywhere except the current request (the
// caller can issue a fresh Create and set the new cookie before
// returning).
func (s *Service) DeleteAllForUser(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// DeleteAllForUserExcept is like DeleteAllForUser but keeps one session
// alive (typically the one carrying the password-change request itself).
func (s *Service) DeleteAllForUserExcept(ctx context.Context, userID, keepID string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keepID)
	return err
}

// ListForUser returns active (non-expired) sessions for a user, newest
// first. Used by the Settings page to show "where am I logged in?".
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
			s                                 Session
			ip, ua                            sql.NullString
			createdAt, expiresAt, lastSeenAt  string
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

// newSessionID returns a fresh 256-bit base64url-encoded random id.
func newSessionID() (string, error) {
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
