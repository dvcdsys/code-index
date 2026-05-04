package apikeys

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
	u, err := usrSvc.Create(context.Background(), "a@b.com", "password1234", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return fixture{S: New(d), UserID: u.ID}
}

func TestGenerate_FormatAndAuthenticate(t *testing.T) {
	f := newFixture(t)
	full, ak, err := f.S.Generate(context.Background(), f.UserID, "my-cli")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(full, KeyPrefix) {
		t.Errorf("full key %q missing %s prefix", full, KeyPrefix)
	}
	// Body of the key past the prefix is base64url(32 bytes) = 43 chars
	// (256 bits of entropy — GitHub-class).
	if len(full)-len(KeyPrefix) != 43 {
		t.Errorf("body length = %d, want 43", len(full)-len(KeyPrefix))
	}
	if ak.Prefix != full[:PrefixDisplayLen] {
		t.Errorf("stored prefix %q does not match key head %q", ak.Prefix, full[:PrefixDisplayLen])
	}
	// Re-authenticate with the plaintext value: must round-trip back to
	// the same user + key.
	u, gotAk, err := f.S.Authenticate(context.Background(), full)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.ID != f.UserID {
		t.Errorf("authenticated as wrong user: %v", u.ID)
	}
	if gotAk.ID != ak.ID {
		t.Errorf("authenticated to wrong key: %v vs %v", gotAk.ID, ak.ID)
	}
}

func TestAuthenticate_BadKey(t *testing.T) {
	f := newFixture(t)
	cases := []string{
		"",
		"not-a-cix-key",
		KeyPrefix + "but-too-short",
		KeyPrefix + strings.Repeat("x", 32), // right shape, wrong content
	}
	for _, c := range cases {
		if _, _, err := f.S.Authenticate(context.Background(), c); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Authenticate(%q) err = %v, want ErrInvalidKey", c, err)
		}
	}
}

func TestRevoke_Blocks(t *testing.T) {
	f := newFixture(t)
	full, ak, _ := f.S.Generate(context.Background(), f.UserID, "soon-revoked")
	if err := f.S.Revoke(context.Background(), ak.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := f.S.Authenticate(context.Background(), full); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Authenticate after Revoke err = %v, want ErrInvalidKey", err)
	}
	// Re-revoke is idempotent.
	if err := f.S.Revoke(context.Background(), ak.ID); !errors.Is(err, ErrAlreadyRevoked) {
		t.Errorf("re-Revoke err = %v, want ErrAlreadyRevoked", err)
	}
}

func TestTouch(t *testing.T) {
	f := newFixture(t)
	_, ak, _ := f.S.Generate(context.Background(), f.UserID, "touched")
	if ak.LastUsedAt != nil {
		t.Fatalf("freshly-issued key should not have LastUsedAt set, got %v", ak.LastUsedAt)
	}
	time.Sleep(5 * time.Millisecond)
	if err := f.S.Touch(context.Background(), ak.ID, "10.0.0.1", "UA/1"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, err := f.S.GetByID(context.Background(), ak.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.LastUsedAt == nil || got.LastUsedIP != "10.0.0.1" || got.LastUsedUA != "UA/1" {
		t.Errorf("Touch did not persist metadata: %+v", got)
	}
}

func TestImportLegacy(t *testing.T) {
	f := newFixture(t)
	const legacy = "my-old-cix-api-key-from-env"
	ak, err := f.S.ImportLegacy(context.Background(), f.UserID, "env-bootstrap", legacy)
	if err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	// The legacy value doesn't have the cix_ prefix; Authenticate's
	// prefix gate should reject it.
	if _, _, err := f.S.Authenticate(context.Background(), legacy); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("legacy key without cix_ prefix should be rejected by Authenticate, got %v", err)
	}
	if ak.Name != "env-bootstrap" {
		t.Errorf("Name = %q", ak.Name)
	}
}

func TestImportLegacy_RoundTripWhenPrefixed(t *testing.T) {
	f := newFixture(t)
	// A legacy key that already has the cix_ prefix DOES authenticate —
	// covers the upgrade path where a user happened to set CIX_API_KEY=cix_...
	const legacy = KeyPrefix + "exact-32-char-body-1234567890ab"
	if _, err := f.S.ImportLegacy(context.Background(), f.UserID, "env-bootstrap", legacy); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if _, _, err := f.S.Authenticate(context.Background(), legacy); err != nil {
		t.Errorf("Authenticate of prefixed legacy key: %v", err)
	}
}

func TestListForOwner(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		_, _, _ = f.S.Generate(context.Background(), f.UserID, "key-"+string(rune('a'+i)))
	}
	list, err := f.S.ListForOwner(context.Background(), f.UserID)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("list length = %d, want 3", len(list))
	}
}

func TestAuthenticate_DisabledUser(t *testing.T) {
	f := newFixture(t)
	full, _, _ := f.S.Generate(context.Background(), f.UserID, "soon-disabled")

	// Need a second admin so disabling the first one doesn't trip
	// users.guardLastAdmin (apikeys tests don't use the users service
	// for the disable, but the fixture user is the only admin, so we
	// raw-update disabled_at to bypass that).
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.S.DB.Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, now, f.UserID); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, _, err := f.S.Authenticate(context.Background(), full); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("Authenticate of disabled-user key err = %v, want ErrUserDisabled", err)
	}
}
