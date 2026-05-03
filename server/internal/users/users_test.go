package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(d)
}

func TestCreateAndAuthenticate(t *testing.T) {
	s := newTestService(t)
	u, err := s.Create(context.Background(), "Alice@Example.com", "supersecret", RoleAdmin, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email not normalised: %q", u.Email)
	}
	if !u.MustChangePassword {
		t.Errorf("MustChangePassword should be true after seeded creation")
	}

	got, err := s.Authenticate(context.Background(), "ALICE@example.com", "supersecret")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("Authenticate returned different user: %v vs %v", got.ID, u.ID)
	}
}

func TestAuthenticate_Wrong(t *testing.T) {
	s := newTestService(t)
	_, _ = s.Create(context.Background(), "a@b.com", "rightpassword", RoleViewer, false)
	_, err := s.Authenticate(context.Background(), "a@b.com", "wrong")
	if !errors.Is(err, ErrInvalidLogin) {
		t.Errorf("err = %v, want ErrInvalidLogin", err)
	}
	_, err = s.Authenticate(context.Background(), "ghost@b.com", "anything")
	if !errors.Is(err, ErrInvalidLogin) {
		t.Errorf("missing user err = %v, want ErrInvalidLogin (no enumeration)", err)
	}
}

func TestEmailUniqueness(t *testing.T) {
	s := newTestService(t)
	_, err := s.Create(context.Background(), "a@b.com", "password1", RoleViewer, false)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err = s.Create(context.Background(), "A@B.com", "password2", RoleViewer, false)
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("err = %v, want ErrEmailTaken (case-insensitive uniqueness)", err)
	}
}

func TestUpdatePassword_ClearsMustChange(t *testing.T) {
	s := newTestService(t)
	u, _ := s.Create(context.Background(), "a@b.com", "initial-password", RoleViewer, true)
	if err := s.UpdatePassword(context.Background(), u.ID, "newpassword123"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, err := s.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.MustChangePassword {
		t.Errorf("MustChangePassword should be cleared after UpdatePassword")
	}
	if _, err := s.Authenticate(context.Background(), "a@b.com", "newpassword123"); err != nil {
		t.Errorf("Authenticate with new password: %v", err)
	}
	if _, err := s.Authenticate(context.Background(), "a@b.com", "initial-password"); !errors.Is(err, ErrInvalidLogin) {
		t.Errorf("old password should no longer authenticate, got %v", err)
	}
}

func TestSetRole_LastAdminBlock(t *testing.T) {
	s := newTestService(t)
	a, _ := s.Create(context.Background(), "a@b.com", "password1", RoleAdmin, false)
	if err := s.SetRole(context.Background(), a.ID, RoleViewer); !errors.Is(err, ErrLastAdminBlock) {
		t.Errorf("demoting last admin err = %v, want ErrLastAdminBlock", err)
	}
	// Add a second admin — now demotion of the first must succeed.
	_, _ = s.Create(context.Background(), "b@b.com", "password2", RoleAdmin, false)
	if err := s.SetRole(context.Background(), a.ID, RoleViewer); err != nil {
		t.Errorf("demoting with another admin around: %v", err)
	}
}

func TestSetDisabled_LastAdminBlock(t *testing.T) {
	s := newTestService(t)
	a, _ := s.Create(context.Background(), "a@b.com", "password1", RoleAdmin, false)
	if err := s.SetDisabled(context.Background(), a.ID, true); !errors.Is(err, ErrLastAdminBlock) {
		t.Errorf("disabling last admin err = %v, want ErrLastAdminBlock", err)
	}
}

func TestDelete_LastAdminBlock(t *testing.T) {
	s := newTestService(t)
	a, _ := s.Create(context.Background(), "a@b.com", "password1", RoleAdmin, false)
	if err := s.Delete(context.Background(), a.ID); !errors.Is(err, ErrLastAdminBlock) {
		t.Errorf("deleting last admin err = %v, want ErrLastAdminBlock", err)
	}
}

func TestInvalidRole(t *testing.T) {
	s := newTestService(t)
	_, err := s.Create(context.Background(), "a@b.com", "password1", "superadmin", false)
	if !errors.Is(err, ErrInvalidRole) {
		t.Errorf("Create with bad role err = %v, want ErrInvalidRole", err)
	}
}

func TestList(t *testing.T) {
	s := newTestService(t)
	for _, em := range []string{"a@b.com", "b@b.com", "c@b.com"} {
		_, _ = s.Create(context.Background(), em, "password1234", RoleViewer, false)
	}
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("list length = %d, want 3", len(list))
	}
}

// Sanity: the dummy bcrypt-compare in Authenticate's no-row branch must
// not crash. Without this, a missing-user lookup would panic on a bad
// hash. The check is inside the function — we just need to exercise it.
func TestAuthenticate_NoUserDoesNotPanic(t *testing.T) {
	s := newTestService(t)
	_, err := s.Authenticate(context.Background(), "nobody@example.com", "anything")
	if err == nil || !strings.Contains(err.Error(), "invalid email or password") {
		t.Errorf("err = %v, want ErrInvalidLogin", err)
	}
}

// TestListWithStats verifies the joined aggregates: last_login_at (newest
// session), active_sessions_count (non-expired only), api_keys_count
// (non-revoked only). All three feed the dashboard's admin /users table.
func TestListWithStats(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	now := time.Now().UTC()

	// Three users: alice (active session + 2 keys, 1 revoked), bob (expired
	// session + 1 active key), carol (no sessions, no keys).
	alice, err := s.Create(ctx, "alice@b.com", "password1234", RoleAdmin, false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := s.Create(ctx, "bob@b.com", "password1234", RoleViewer, false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	carol, err := s.Create(ctx, "carol@b.com", "password1234", RoleViewer, false)
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}

	insertSession := func(userID string, created, expires time.Time) {
		_, err := s.DB.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at)
			 VALUES (?, ?, ?, ?, ?)`,
			"sess-"+userID+"-"+created.Format("150405.999999999"),
			userID,
			created.Format(time.RFC3339Nano),
			expires.Format(time.RFC3339Nano),
			created.Format(time.RFC3339Nano),
		)
		if err != nil {
			t.Fatalf("insert session for %s: %v", userID, err)
		}
	}
	insertKey := func(userID, name string, revoked bool) {
		var revokedAt sql.NullString
		if revoked {
			revokedAt = sql.NullString{Valid: true, String: now.Format(time.RFC3339Nano)}
		}
		_, err := s.DB.ExecContext(ctx,
			`INSERT INTO api_keys (id, owner_user_id, name, prefix, hash, created_at, revoked_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"ak-"+userID+"-"+name, userID, name,
			"cix_"+name[:4], "hash-"+userID+"-"+name,
			now.Format(time.RFC3339Nano), revokedAt,
		)
		if err != nil {
			t.Fatalf("insert key for %s: %v", userID, err)
		}
	}

	// Alice: 2 sessions (newer = "now", older = 1 hour ago), both active.
	insertSession(alice.ID, now.Add(-1*time.Hour), now.Add(13*24*time.Hour))
	aliceLatest := now.Add(-5 * time.Minute)
	insertSession(alice.ID, aliceLatest, now.Add(14*24*time.Hour))
	insertKey(alice.ID, "live1", false)
	insertKey(alice.ID, "live2", false)
	insertKey(alice.ID, "deadx", true)

	// Bob: 1 expired session, 1 active key. last_login_at still set
	// (expired session still counts as a past login event).
	bobOnly := now.Add(-30 * 24 * time.Hour)
	insertSession(bob.ID, bobOnly, now.Add(-16*24*time.Hour))
	insertKey(bob.ID, "bobok", false)

	// Carol: nothing.
	_ = carol

	got, err := s.ListWithStats(ctx)
	if err != nil {
		t.Fatalf("ListWithStats: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	by := map[string]UserWithStats{}
	for _, u := range got {
		by[u.Email] = u
	}

	if a := by["alice@b.com"]; a.ActiveSessionsCount != 2 || a.APIKeysCount != 2 ||
		a.LastLoginAt == nil || !a.LastLoginAt.Equal(aliceLatest.Truncate(time.Nanosecond)) {
		t.Errorf("alice stats wrong: sess=%d keys=%d last=%v (want sess=2 keys=2 last=%v)",
			a.ActiveSessionsCount, a.APIKeysCount, a.LastLoginAt, aliceLatest)
	}
	if b := by["bob@b.com"]; b.ActiveSessionsCount != 0 || b.APIKeysCount != 1 || b.LastLoginAt == nil {
		t.Errorf("bob stats wrong: sess=%d keys=%d last=%v (want sess=0 keys=1 last set)",
			b.ActiveSessionsCount, b.APIKeysCount, b.LastLoginAt)
	}
	if c := by["carol@b.com"]; c.ActiveSessionsCount != 0 || c.APIKeysCount != 0 || c.LastLoginAt != nil {
		t.Errorf("carol stats wrong: sess=%d keys=%d last=%v (want all zero/nil)",
			c.ActiveSessionsCount, c.APIKeysCount, c.LastLoginAt)
	}
}
