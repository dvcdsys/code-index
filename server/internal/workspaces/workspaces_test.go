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
