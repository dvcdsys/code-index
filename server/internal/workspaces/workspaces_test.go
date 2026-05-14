package workspaces

import (
	"context"
	"errors"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func mustOpen(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(database)
}

func TestCreateAndGet(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	w, err := svc.Create(ctx, "platform", "microservices")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w.ID == "" || w.Name != "platform" || w.Description != "microservices" {
		t.Fatalf("unexpected workspace: %+v", w)
	}

	got, err := svc.GetByID(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "platform" {
		t.Fatalf("got name %q", got.Name)
	}
}

func TestCreateEmptyNameRejected(t *testing.T) {
	svc := mustOpen(t)
	if _, err := svc.Create(context.Background(), "  ", "x"); !errors.Is(err, ErrNameEmpty) {
		t.Fatalf("expected ErrNameEmpty, got %v", err)
	}
}

func TestCreateDuplicateName(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "alpha", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, "alpha", ""); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("expected ErrNameTaken, got %v", err)
	}
}

func TestList(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "alpha", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Create(ctx, "bravo", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(list))
	}
}

func TestUpdate(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	w, _ := svc.Create(ctx, "alpha", "old")
	newName := "alpha-renamed"
	newDesc := "new"
	updated, err := svc.Update(ctx, w.ID, &newName, &newDesc)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != newName || updated.Description != newDesc {
		t.Fatalf("update did not apply: %+v", updated)
	}

	// nil description = leave alone.
	finalName := "alpha-final"
	updated2, err := svc.Update(ctx, w.ID, &finalName, nil)
	if err != nil {
		t.Fatalf("Update again: %v", err)
	}
	if updated2.Description != newDesc {
		t.Fatalf("description should have been preserved, got %q", updated2.Description)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc := mustOpen(t)
	name := "x"
	if _, err := svc.Update(context.Background(), "no-such-id", &name, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()
	w, _ := svc.Create(ctx, "x", "")
	if err := svc.Delete(ctx, w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete should be ErrNotFound, got %v", err)
	}
}

// TestEnsureDefault_CreatesOnFirstCall verifies the bootstrap path:
// a fresh DB has no default workspace; EnsureDefault must create the
// singleton row and stamp it with IsDefault=true.
func TestEnsureDefault_CreatesOnFirstCall(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	// Sanity: GetDefault returns ErrNotFound before EnsureDefault runs.
	if _, err := svc.GetDefault(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound before EnsureDefault, got %v", err)
	}

	def, err := svc.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if !def.IsDefault {
		t.Fatalf("expected IsDefault=true, got %+v", def)
	}
	if def.Name != "Personal" {
		t.Fatalf("expected name=Personal, got %q", def.Name)
	}
}

// TestEnsureDefault_Idempotent verifies a second call returns the
// existing row without inserting a duplicate. The partial UNIQUE index
// on is_default would catch a regression here.
func TestEnsureDefault_Idempotent(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	first, err := svc.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("first EnsureDefault: %v", err)
	}
	second, err := svc.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("second EnsureDefault: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("EnsureDefault should be idempotent — got %q then %q", first.ID, second.ID)
	}

	// And only one row total should be flagged as default.
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var defaults int
	for _, w := range list {
		if w.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default workspace, got %d", defaults)
	}
}

// TestEnsureDefault_AvoidsNameCollision verifies the loop that bumps
// the name suffix when an operator-created workspace already occupies
// the natural "Personal" name. The default workspace lands at
// "Personal (2)" without stealing the existing row's identity.
func TestEnsureDefault_AvoidsNameCollision(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	existing, err := svc.Create(ctx, "Personal", "operator's own workspace")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	def, err := svc.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if def.ID == existing.ID {
		t.Fatalf("EnsureDefault stole the operator's workspace — got %+v", def)
	}
	if def.Name == existing.Name {
		t.Fatalf("default workspace should have a distinct name, got %q", def.Name)
	}
	if !def.IsDefault {
		t.Fatalf("default flag missing on the freshly-allocated row")
	}
	// The pre-existing row is unchanged.
	again, err := svc.GetByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if again.IsDefault {
		t.Fatalf("pre-existing row must not be flagged as default, got %+v", again)
	}
}

// TestDelete_DefaultProtected guards the bootstrap invariant: even if
// /git-repos is unused, the default workspace must survive operator
// deletes. The error type lets the HTTP layer map this to 409.
func TestDelete_DefaultProtected(t *testing.T) {
	svc := mustOpen(t)
	ctx := context.Background()

	def, err := svc.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if err := svc.Delete(ctx, def.ID); !errors.Is(err, ErrDefaultProtected) {
		t.Fatalf("expected ErrDefaultProtected, got %v", err)
	}
	// Sanity: regular workspaces remain deletable.
	other, err := svc.Create(ctx, "platform", "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Delete(ctx, other.ID); err != nil {
		t.Fatalf("Delete on non-default workspace failed: %v", err)
	}
}
