package groups

import (
	"context"
	"errors"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/users"
)

func newTestService(t *testing.T) (*Service, *users.Service) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(database), users.New(database)
}

func TestGroupCRUDAndMembership(t *testing.T) {
	ctx := context.Background()
	gs, us := newTestService(t)

	alice, err := us.Create(ctx, "alice@x.com", "password1234", users.RoleUser, false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := us.Create(ctx, "bob@x.com", "password1234", users.RoleUser, false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	g, err := gs.Create(ctx, "Product", "PM agents")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if g.Name != "Product" {
		t.Errorf("name = %q", g.Name)
	}

	// Duplicate name → ErrNameTaken.
	if _, err := gs.Create(ctx, "Product", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name err = %v, want ErrNameTaken", err)
	}

	// Add alice; bob stays out.
	if err := gs.AddMember(ctx, g.ID, alice.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	// Idempotent re-add.
	if err := gs.AddMember(ctx, g.ID, alice.ID); err != nil {
		t.Fatalf("re-add member: %v", err)
	}

	if ok, _ := gs.IsMember(ctx, alice.ID, g.ID); !ok {
		t.Error("alice should be a member")
	}
	if ok, _ := gs.IsMember(ctx, bob.ID, g.ID); ok {
		t.Error("bob should NOT be a member")
	}

	// AddMember for a non-existent user → ErrUserNotFound.
	if err := gs.AddMember(ctx, g.ID, "nope"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("add bad user err = %v, want ErrUserNotFound", err)
	}

	members, err := gs.ListMembers(ctx, g.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 || members[0].Email != "alice@x.com" {
		t.Errorf("members = %+v", members)
	}

	forAlice, err := gs.ListForUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("list for user: %v", err)
	}
	if len(forAlice) != 1 || forAlice[0].ID != g.ID {
		t.Errorf("groups for alice = %+v", forAlice)
	}
	if forBob, _ := gs.ListForUser(ctx, bob.ID); len(forBob) != 0 {
		t.Errorf("groups for bob = %+v, want none", forBob)
	}

	// Remove alice.
	if err := gs.RemoveMember(ctx, g.ID, alice.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if ok, _ := gs.IsMember(ctx, alice.ID, g.ID); ok {
		t.Error("alice should be removed")
	}

	// Delete group, then GetByID → ErrNotFound.
	if err := gs.Delete(ctx, g.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if _, err := gs.GetByID(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted err = %v, want ErrNotFound", err)
	}
}
