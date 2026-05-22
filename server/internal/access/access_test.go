package access

import (
	"context"
	"slices"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

func TestProjectAndWorkspaceSharing(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	us := users.New(database)
	gs := groups.New(database)
	ws := workspaces.New(database)

	alice, _ := us.Create(ctx, "alice@x.com", "password1234", users.RoleUser, false)
	bob, _ := us.Create(ctx, "bob@x.com", "password1234", users.RoleUser, false)

	// Personal project owned by alice.
	if _, err := projects.Create(ctx, database, projects.CreateRequest{
		HostPath: "/alice/local", OwnerUserID: alice.ID,
	}); err != nil {
		t.Fatalf("create local project: %v", err)
	}

	// External project (ownerless) + its git_repos peer.
	if _, err := projects.Create(ctx, database, projects.CreateRequest{
		HostPath: "github.com/x/y@main",
	}); err != nil {
		t.Fatalf("create external project: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES ('github.com/x/y@main', 'https://github.com/x/y', 'main', 's', '2024-01-01', '2024-01-01')`,
	); err != nil {
		t.Fatalf("insert git_repos: %v", err)
	}

	// Group with bob as member.
	g, _ := gs.Create(ctx, "Product", "")
	if err := gs.AddMember(ctx, g.ID, bob.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// IsProjectExternal.
	if ext, _ := IsProjectExternal(ctx, database, "github.com/x/y@main"); !ext {
		t.Error("github.com/x/y@main should be external")
	}
	if ext, _ := IsProjectExternal(ctx, database, "/alice/local"); ext {
		t.Error("/alice/local should not be external")
	}

	// Before sharing: bob cannot access the external project.
	if shared, _ := ProjectSharedToUser(ctx, database, "github.com/x/y@main", bob.ID); shared {
		t.Error("bob should not have access before share")
	}

	// Share external project to the group.
	if err := ShareProjectToGroup(ctx, database, "github.com/x/y@main", g.ID); err != nil {
		t.Fatalf("share project: %v", err)
	}
	if shared, _ := ProjectSharedToUser(ctx, database, "github.com/x/y@main", bob.ID); !shared {
		t.Error("bob should have access after share")
	}
	// alice (not in group) still has no group-share access to it.
	if shared, _ := ProjectSharedToUser(ctx, database, "github.com/x/y@main", alice.ID); shared {
		t.Error("alice (non-member) should not have group-share access")
	}

	// AccessibleProjectHostPaths: alice sees her own local; bob sees the shared external.
	aliceHosts, _ := AccessibleProjectHostPaths(ctx, database, alice.ID)
	if !contains(aliceHosts, "/alice/local") || contains(aliceHosts, "github.com/x/y@main") {
		t.Errorf("alice accessible = %v", aliceHosts)
	}
	bobHosts, _ := AccessibleProjectHostPaths(ctx, database, bob.ID)
	if !contains(bobHosts, "github.com/x/y@main") || contains(bobHosts, "/alice/local") {
		t.Errorf("bob accessible = %v", bobHosts)
	}

	// Workspace owned by alice, shared to the group.
	w, _ := ws.Create(ctx, "team", "")
	if err := ShareWorkspaceToGroup(ctx, database, w.ID, g.ID); err != nil {
		t.Fatalf("share workspace: %v", err)
	}
	if seen, _ := WorkspaceSharedToUser(ctx, database, w.ID, bob.ID); !seen {
		t.Error("bob should see shared workspace")
	}
	visForBob, _ := VisibleWorkspaceIDs(ctx, database, bob.ID)
	if !contains(visForBob, w.ID) {
		t.Errorf("bob visible workspaces = %v", visForBob)
	}

	// Unshare removes access.
	if err := UnshareProjectFromGroup(ctx, database, "github.com/x/y@main", g.ID); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if shared, _ := ProjectSharedToUser(ctx, database, "github.com/x/y@main", bob.ID); shared {
		t.Error("bob should lose access after unshare")
	}
}

func contains(xs []string, want string) bool {
	return slices.Contains(xs, want)
}
