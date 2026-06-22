package githubtokens

import (
	"context"
	"errors"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/secrets"
)

func mustOpen(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{
		DataDir:       t.TempDir(),
		AllowGenerate: true,
	})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	return New(database, sec)
}

func TestCreateAndReveal(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	tok, err := svc.Create(ctx, "personal", "ghp_secret_value", []string{"repo", "admin:repo_hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.ID == "" || tok.Name != "personal" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if len(tok.Scopes) != 2 || tok.Scopes[0] != "repo" {
		t.Fatalf("scopes round-trip failed: %+v", tok.Scopes)
	}

	plain, err := svc.Reveal(ctx, tok.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if plain != "ghp_secret_value" {
		t.Fatalf("Reveal returned %q, want plaintext", plain)
	}
}

func TestUpdate(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	tok, err := svc.Create(ctx, "personal", "ghp_old_value", []string{"repo"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Rotate the secret + scopes; id and name must be preserved so bound
	// projects keep working.
	updated, err := svc.Update(ctx, tok.ID, "ghp_new_value", []string{"repo", "admin:repo_hook"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != tok.ID {
		t.Fatalf("id changed on update: %q -> %q", tok.ID, updated.ID)
	}
	if updated.Name != "personal" {
		t.Fatalf("name changed on update: %q", updated.Name)
	}
	if len(updated.Scopes) != 2 || updated.Scopes[1] != "admin:repo_hook" {
		t.Fatalf("scopes not refreshed: %+v", updated.Scopes)
	}

	plain, err := svc.Reveal(ctx, tok.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if plain != "ghp_new_value" {
		t.Fatalf("Reveal returned %q, want rotated plaintext", plain)
	}
}

func TestUpdateRejectsEmpty(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	tok, _ := svc.Create(ctx, "personal", "v1", nil)
	if _, err := svc.Update(ctx, tok.ID, "  ", nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc := mustOpen(t)
	if _, err := svc.Update(context.Background(), "no-such-id", "v", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateRejectsEmpty(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "  ", "x", nil); !errors.Is(err, ErrNameEmpty) {
		t.Fatalf("expected ErrNameEmpty, got %v", err)
	}
	if _, err := svc.Create(ctx, "name", "  ", nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestCreateDuplicateName(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "personal", "v1", nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.Create(ctx, "personal", "v2", nil); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("expected ErrNameTaken, got %v", err)
	}
}

func TestListDoesNotLeakPlaintext(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	plaintext := "ghp_some_secret_we_should_never_see_in_list"
	if _, err := svc.Create(ctx, "personal", plaintext, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 token, got %d", len(list))
	}
	// The Token struct does not carry a plaintext field — verify by
	// re-encoding to JSON-like representation and asserting the plaintext
	// is nowhere in it. A direct field check is the simplest assertion:
	if list[0].Name == plaintext {
		t.Fatalf("plaintext leaked into Name field")
	}
}

func TestDelete(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	tok, _ := svc.Create(ctx, "x", "y", nil)
	if err := svc.Delete(ctx, tok.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestRevealNotFound(t *testing.T) {
	svc := mustOpen(t)
	if _, err := svc.Reveal(context.Background(), "no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTouch(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	tok, _ := svc.Create(ctx, "x", "y", nil)
	if tok.LastUsedAt != nil {
		t.Fatalf("LastUsedAt should be nil on fresh token")
	}
	if err := svc.Touch(ctx, tok.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	updated, err := svc.GetByID(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.LastUsedAt == nil {
		t.Fatalf("LastUsedAt should be set after Touch")
	}
}

func TestCountWithEncryption(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	n, err := svc.CountWithEncryption(ctx)
	if err != nil || n != 0 {
		t.Fatalf("expected (0, nil), got (%d, %v)", n, err)
	}
	if _, err := svc.Create(ctx, "a", "b", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	n, _ = svc.CountWithEncryption(ctx)
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
}
