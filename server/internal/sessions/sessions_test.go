package sessions

import (
	"context"
	"errors"
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
	s, err := f.S.Create(context.Background(), f.UserID, "10.0.0.1", "tester/1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, u, err := f.S.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != s.ID {
		t.Errorf("Get returned different id: %v vs %v", got.ID, s.ID)
	}
	if u.ID != f.UserID {
		t.Errorf("Get returned different user: %v vs %v", u.ID, f.UserID)
	}
}

func TestTouch_SlidesExpires(t *testing.T) {
	f := newFixture(t)
	s, _ := f.S.Create(context.Background(), f.UserID, "", "")
	originalExp := s.ExpiresAt

	// Forward time by sleeping a hair so timestamps differ.
	time.Sleep(10 * time.Millisecond)
	if err := f.S.Touch(context.Background(), s.ID, "127.0.0.1", "after"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _, _ := f.S.Get(context.Background(), s.ID)
	if !got.ExpiresAt.After(originalExp) {
		t.Errorf("ExpiresAt should slide forward; got %v vs %v", got.ExpiresAt, originalExp)
	}
	if got.LastSeenIP != "127.0.0.1" || got.LastSeenUA != "after" {
		t.Errorf("Touch did not update IP/UA: %v / %v", got.LastSeenIP, got.LastSeenUA)
	}
}

func TestGet_Expired(t *testing.T) {
	f := newFixture(t)
	s, _ := f.S.Create(context.Background(), f.UserID, "", "")
	// Force-expire by writing a past timestamp directly.
	if _, err := f.S.DB.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), s.ID); err != nil {
		t.Fatalf("update expires_at: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), s.ID); !errors.Is(err, ErrExpired) {
		t.Errorf("Get on expired session err = %v, want ErrExpired", err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	s, _ := f.S.Create(context.Background(), f.UserID, "", "")
	if err := f.S.Delete(context.Background(), s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteAllForUserExcept(t *testing.T) {
	f := newFixture(t)
	keep, _ := f.S.Create(context.Background(), f.UserID, "", "")
	gone1, _ := f.S.Create(context.Background(), f.UserID, "", "")
	gone2, _ := f.S.Create(context.Background(), f.UserID, "", "")

	if err := f.S.DeleteAllForUserExcept(context.Background(), f.UserID, keep.ID); err != nil {
		t.Fatalf("DeleteAllForUserExcept: %v", err)
	}
	if _, _, err := f.S.Get(context.Background(), keep.ID); err != nil {
		t.Errorf("kept session lost: %v", err)
	}
	for _, g := range []Session{gone1, gone2} {
		if _, _, err := f.S.Get(context.Background(), g.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %v should have been deleted, got err=%v", g.ID, err)
		}
	}
}

func TestGC(t *testing.T) {
	f := newFixture(t)
	live, _ := f.S.Create(context.Background(), f.UserID, "", "")
	dead, _ := f.S.Create(context.Background(), f.UserID, "", "")
	_, _ = f.S.DB.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), dead.ID)

	n, err := f.S.GC(context.Background())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if n != 1 {
		t.Errorf("GC removed = %d, want 1", n)
	}
	if _, _, err := f.S.Get(context.Background(), live.ID); err != nil {
		t.Errorf("live session lost after GC: %v", err)
	}
}

func TestGet_DisabledUser(t *testing.T) {
	f := newFixture(t)
	s, _ := f.S.Create(context.Background(), f.UserID, "", "")
	// Disable the user directly (bypass the LastAdminBlock guard since
	// the seeded user is a viewer, not an admin).
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = f.S.DB.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, now, f.UserID)
	if _, _, err := f.S.Get(context.Background(), s.ID); !errors.Is(err, ErrDisabled) {
		t.Errorf("Get on disabled-user session err = %v, want ErrDisabled", err)
	}
}
