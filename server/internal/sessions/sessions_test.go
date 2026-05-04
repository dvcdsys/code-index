package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/users"
)

type fixture struct {
	S      *Service
	UserID string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	usrSvc := users.New(d)
	u, err := usrSvc.Create(context.Background(), "a@b.com", "password1234", users.RoleViewer, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return fixture{S: New(d), UserID: u.ID}
}

func TestCreateAndGet(t *testing.T) {
	f := newFixture(t)
	c, err := f.S.Create(context.Background(), f.UserID, "10.0.0.1", "tester/1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, u, err := f.S.Get(context.Background(), c.RawToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != c.Session.ID {
		t.Errorf("Get returned different id: %v vs %v", got.ID, c.Session.ID)
	}
	if u.ID != f.UserID {
		t.Errorf("Get returned different user: %v vs %v", u.ID, f.UserID)
	}
}

// TestCreate_StoresHashNotRawToken proves the headline security property:
// the value the browser sends in the cookie never appears in the sessions
// table. Only sha256(token) is persisted, so a leaked DB snapshot cannot
// be replayed to impersonate active sessions.
func TestCreate_StoresHashNotRawToken(t *testing.T) {
	f := newFixture(t)
	c, err := f.S.Create(context.Background(), f.UserID, "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.RawToken == "" || c.Session.ID == "" {
		t.Fatalf("Create returned empty token / id")
	}
	if c.RawToken == c.Session.ID {
		t.Fatalf("RawToken and Session.ID must differ (token=%q id=%q)", c.RawToken, c.Session.ID)
	}

	var dbID string
	if err := f.S.DB.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&dbID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if dbID != c.Session.ID {
		t.Errorf("DB id=%q, want hash %q", dbID, c.Session.ID)
	}
	if dbID == c.RawToken {
		t.Errorf("DB stored raw token %q — must store the hash instead", c.RawToken)
	}
	if strings.Contains(dbID, c.RawToken) {
		t.Errorf("DB id %q leaks the raw token %q", dbID, c.RawToken)
	}
	// Hash must be deterministic and match the public helper.
	if HashToken(c.RawToken) != c.Session.ID {
		t.Errorf("HashToken(raw) = %q, want Session.ID %q", HashToken(c.RawToken), c.Session.ID)
	}
}

// TestGet_RejectsRawHashLookup ensures an attacker who somehow knows the
// stored hash (e.g. from a leaked DB) cannot use it as the cookie value
// directly — Get hashes its argument before querying, so passing the
// hash hashes it again and finds nothing.
func TestGet_RejectsRawHashLookup(t *testing.T) {
	f := newFixture(t)
	c, _ := f.S.Create(context.Background(), f.UserID, "", "")
	// Lookup with the raw token works.
	if _, _, err := f.S.Get(context.Background(), c.RawToken); err != nil {
		t.Fatalf("lookup with raw token: %v", err)
	}
	// Lookup with the stored hash must fail.
	if _, _, err := f.S.Get(context.Background(), c.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(hash) err = %v, want ErrNotFound", err)
	}
}

func TestTouch_SlidesExpires(t *testing.T) {
	f := newFixture(t)
	c, _ := f.S.Create(context.Background(), f.UserID, "", "")
	originalExp := c.Session.ExpiresAt

	// Forward time by sleeping a hair so timestamps differ.
	time.Sleep(10 * time.Millisecond)
	if err := f.S.Touch(context.Background(), c.Session.ID, "127.0.0.1", "after"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _, _ := f.S.Get(context.Background(), c.RawToken)
	if !got.ExpiresAt.After(originalExp) {
		t.Errorf("ExpiresAt should slide forward; got %v vs %v", got.ExpiresAt, originalExp)
	}
	if got.LastSeenIP != "127.0.0.1" || got.LastSeenUA != "after" {
		t.Errorf("Touch did not update IP/UA: %v / %v", got.LastSeenIP, got.LastSeenUA)
	}
}

func TestGet_Expired(t *testing.T) {
	f := newFixture(t)
	c, _ := f.S.Create(context.Background(), f.UserID, "", "")
	// Force-expire by writing a past timestamp directly.
	if _, err := f.S.DB.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), c.Session.ID); err != nil {
		t.Fatalf("update expires_at: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), c.RawToken); !errors.Is(err, ErrExpired) {
		t.Errorf("Get on expired session err = %v, want ErrExpired", err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	c, _ := f.S.Create(context.Background(), f.UserID, "", "")
	if err := f.S.Delete(context.Background(), c.Session.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), c.RawToken); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteAllForUserExcept(t *testing.T) {
	f := newFixture(t)
	keep, _ := f.S.Create(context.Background(), f.UserID, "", "")
	gone1, _ := f.S.Create(context.Background(), f.UserID, "", "")
	gone2, _ := f.S.Create(context.Background(), f.UserID, "", "")

	if err := f.S.DeleteAllForUserExcept(context.Background(), f.UserID, keep.Session.ID); err != nil {
		t.Fatalf("DeleteAllForUserExcept: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), keep.RawToken); err != nil {
		t.Errorf("kept session lost: %v", err)
	}
	for _, g := range []Created{gone1, gone2} {
		if _, _, err := f.S.Get(context.Background(), g.RawToken); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %v should have been deleted, got err=%v", g.Session.ID, err)
		}
	}
}

func TestGC(t *testing.T) {
	f := newFixture(t)
	live, _ := f.S.Create(context.Background(), f.UserID, "", "")
	dead, _ := f.S.Create(context.Background(), f.UserID, "", "")
	_, _ = f.S.DB.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), dead.Session.ID)

	n, err := f.S.GC(context.Background())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if n != 1 {
		t.Errorf("GC removed = %d, want 1", n)
	}
	if _, _, err := f.S.Get(context.Background(), live.RawToken); err != nil {
		t.Errorf("live session lost after GC: %v", err)
	}
}

func TestGet_DisabledUser(t *testing.T) {
	f := newFixture(t)
	c, _ := f.S.Create(context.Background(), f.UserID, "", "")
	// Disable the user directly (bypass the LastAdminBlock guard since
	// the seeded user is a viewer, not an admin).
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = f.S.DB.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, now, f.UserID)
	if _, _, err := f.S.Get(context.Background(), c.RawToken); !errors.Is(err, ErrDisabled) {
		t.Errorf("Get on disabled-user session err = %v, want ErrDisabled", err)
	}
}
