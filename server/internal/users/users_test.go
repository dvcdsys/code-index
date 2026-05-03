package users

import (
	"context"
	"errors"
	"strings"
	"testing"

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
