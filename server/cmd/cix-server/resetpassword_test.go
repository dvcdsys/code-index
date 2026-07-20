package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

func TestResetPassword_StdinPassword(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	usrSvc := users.New(d)
	sessSvc := sessions.New(d)
	ctx := context.Background()

	u, err := usrSvc.Create(ctx, "admin@example.com", "oldpassword", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := sessSvc.Create(ctx, u.ID, "127.0.0.1", "test-ua"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	var out strings.Builder
	if err := resetPassword(ctx, d, "admin@example.com", strings.NewReader("newpassword123\n"), &out); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	// New password works, old one doesn't.
	got, err := usrSvc.Authenticate(ctx, "admin@example.com", "newpassword123")
	if err != nil {
		t.Fatalf("authenticate with new password: %v", err)
	}
	if !got.MustChangePassword {
		t.Errorf("reset account must be flagged must_change_password")
	}
	if _, err := usrSvc.Authenticate(ctx, "admin@example.com", "oldpassword"); err == nil {
		t.Errorf("old password still authenticates after reset")
	}

	// Sessions revoked.
	sess, err := sessSvc.ListForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sess) != 0 {
		t.Errorf("len(sessions) = %d after reset, want 0", len(sess))
	}

	// A supplied (non-generated) password must never be echoed back.
	if strings.Contains(out.String(), "newpassword123") {
		t.Errorf("output echoes the stdin-supplied password: %q", out.String())
	}
}

func TestResetPassword_GeneratesWhenStdinEmpty(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	usrSvc := users.New(d)
	ctx := context.Background()

	if _, err := usrSvc.Create(ctx, "admin@example.com", "oldpassword", users.RoleAdmin, false); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var out strings.Builder
	if err := resetPassword(ctx, d, "admin@example.com", strings.NewReader(""), &out); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	// The generated password is printed and authenticates.
	lines := strings.Split(out.String(), "\n")
	var generated string
	for _, l := range lines {
		if pw, ok := strings.CutPrefix(l, "Temporary password: "); ok {
			generated = pw
		}
	}
	if generated == "" {
		t.Fatalf("output lacks a 'Temporary password:' line: %q", out.String())
	}
	if _, err := usrSvc.Authenticate(ctx, "admin@example.com", generated); err != nil {
		t.Fatalf("authenticate with generated password %q: %v", generated, err)
	}
}

func TestResetPassword_UnknownEmail_ListsUsers(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	usrSvc := users.New(d)
	ctx := context.Background()

	if _, err := usrSvc.Create(ctx, "real@example.com", "password1", users.RoleAdmin, false); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var out strings.Builder
	err = resetPassword(ctx, d, "typo@example.com", strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("expected error for unknown email")
	}
	if !strings.Contains(err.Error(), "real@example.com") {
		t.Errorf("error should list existing users to catch typos, got: %v", err)
	}
}

func TestResetPassword_ShortStdinPassword_Rejected(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	usrSvc := users.New(d)
	ctx := context.Background()

	if _, err := usrSvc.Create(ctx, "admin@example.com", "oldpassword", users.RoleAdmin, false); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	var out strings.Builder
	err = resetPassword(ctx, d, "admin@example.com", strings.NewReader("short\n"), &out)
	if err == nil {
		t.Fatal("expected error for too-short password")
	}
	// The old password must still work — nothing changed.
	if _, err := usrSvc.Authenticate(ctx, "admin@example.com", "oldpassword"); err != nil {
		t.Errorf("old password broken after failed reset: %v", err)
	}
}
