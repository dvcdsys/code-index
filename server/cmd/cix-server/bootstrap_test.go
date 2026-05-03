package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// silentLogger discards every log line — bootstrapAuth's warnings should
// not pollute test output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newServices(t *testing.T) (*users.Service, *apikeys.Service) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return users.New(d), apikeys.New(d)
}

func TestBootstrap_FreshDB_WithEnv_CreatesAdmin(t *testing.T) {
	usrSvc, akSvc := newServices(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "initialpass",
	}
	if err := bootstrapAuth(context.Background(), cfg, silentLogger(), usrSvc, akSvc); err != nil {
		t.Fatalf("bootstrapAuth: %v", err)
	}
	u, err := usrSvc.GetByEmail(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("admin not seeded: %v", err)
	}
	if u.Role != users.RoleAdmin {
		t.Errorf("seeded user has role %q, want admin", u.Role)
	}
	if !u.MustChangePassword {
		t.Errorf("seeded admin must be flagged must_change_password")
	}
}

func TestBootstrap_FreshDB_NoEnv_Fatal(t *testing.T) {
	usrSvc, akSvc := newServices(t)
	cfg := &config.Config{} // empty bootstrap fields
	err := bootstrapAuth(context.Background(), cfg, silentLogger(), usrSvc, akSvc)
	if err == nil {
		t.Fatal("expected fatal error on empty DB without bootstrap env")
	}
	if !strings.Contains(err.Error(), "no users in database") {
		t.Errorf("error message lacks expected text: %v", err)
	}
	// Sanity: the message must mention BOTH env var names, otherwise the
	// operator has to read the source to figure out what to set.
	for _, want := range []string{"CIX_BOOTSTRAP_ADMIN_EMAIL", "CIX_BOOTSTRAP_ADMIN_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %s, got: %v", want, err)
		}
	}
}

// TestBootstrap_FreshDB_PartialEnv_Fatal — half-set env vars are a real
// configuration mistake (typo, deleted line in compose, etc.). Refuse to
// start AND name the missing var so the operator doesn't have to guess.
func TestBootstrap_FreshDB_PartialEnv_Fatal(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		missing string
	}{
		{
			name:    "email only",
			cfg:     &config.Config{BootstrapAdminEmail: "admin@example.com"},
			missing: "CIX_BOOTSTRAP_ADMIN_PASSWORD",
		},
		{
			name:    "password only",
			cfg:     &config.Config{BootstrapAdminPassword: "initialpw"},
			missing: "CIX_BOOTSTRAP_ADMIN_EMAIL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usrSvc, akSvc := newServices(t)
			err := bootstrapAuth(context.Background(), tc.cfg, silentLogger(), usrSvc, akSvc)
			if err == nil {
				t.Fatal("expected error on half-set bootstrap env")
			}
			if !strings.Contains(err.Error(), "incomplete bootstrap configuration") {
				t.Errorf("error must say 'incomplete bootstrap configuration', got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Errorf("error must name the missing var %s, got: %v", tc.missing, err)
			}
		})
	}
}

func TestBootstrap_PopulatedDB_IgnoresEnv(t *testing.T) {
	usrSvc, akSvc := newServices(t)
	if _, err := usrSvc.Create(context.Background(), "preexisting@example.com", "preexisting1", users.RoleAdmin, false); err != nil {
		t.Fatalf("preseed: %v", err)
	}
	cfg := &config.Config{
		BootstrapAdminEmail:    "different@example.com",
		BootstrapAdminPassword: "differentpw",
	}
	if err := bootstrapAuth(context.Background(), cfg, silentLogger(), usrSvc, akSvc); err != nil {
		t.Fatalf("bootstrapAuth: %v", err)
	}
	// "different@example.com" should NOT have been created.
	if _, err := usrSvc.GetByEmail(context.Background(), "different@example.com"); err == nil {
		t.Errorf("env-supplied email was seeded despite DB having existing users")
	}
}

func TestBootstrap_LegacyAPIKey_Imported(t *testing.T) {
	usrSvc, akSvc := newServices(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "initialpw",
		APIKey:                 "legacy-cix-api-key-from-env",
	}
	if err := bootstrapAuth(context.Background(), cfg, silentLogger(), usrSvc, akSvc); err != nil {
		t.Fatalf("bootstrapAuth: %v", err)
	}
	u, _ := usrSvc.GetByEmail(context.Background(), "admin@example.com")
	keys, err := akSvc.ListForOwner(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1 (env-bootstrap)", len(keys))
	}
	if keys[0].Name != "env-bootstrap" {
		t.Errorf("key name = %q, want 'env-bootstrap'", keys[0].Name)
	}
}
