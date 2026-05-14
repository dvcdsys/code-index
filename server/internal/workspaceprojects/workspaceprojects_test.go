package workspaceprojects

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

func mustOpen(t *testing.T) (*sql.DB, *Service) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, New(d)
}

// seedIndexedProject inserts the project row in status='indexed' so
// Link() satisfies its precondition. Avoids the full projects service
// import to keep the test focused on this package.
func seedIndexedProject(t *testing.T, d *sql.DB, hostPath string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(`
		INSERT INTO projects (
			host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash
		) VALUES (?, ?, '[]', '{}', '{}', 'indexed', ?, ?, ?)`,
		hostPath, hostPath, now, now, db.HashHostPath(hostPath),
	); err != nil {
		t.Fatalf("seed project %s: %v", hostPath, err)
	}
}

func seedWorkspace(t *testing.T, d *sql.DB, name string) string {
	t.Helper()
	ws, err := workspaces.New(d).Create(context.Background(), name, "")
	if err != nil {
		t.Fatalf("seed workspace %s: %v", name, err)
	}
	return ws.ID
}

func TestLink_HappyPath(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsID := seedWorkspace(t, d, "platform")
	seedIndexedProject(t, d, "github.com/x/y@main")

	m, err := svc.Link(ctx, wsID, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if m.WorkspaceID != wsID || m.ProjectPath != "github.com/x/y@main" {
		t.Fatalf("Link returned wrong row: %+v", m)
	}
	if m.AddedAt.IsZero() {
		t.Error("AddedAt was not populated")
	}
}

func TestLink_DuplicateRejected(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsID := seedWorkspace(t, d, "platform")
	seedIndexedProject(t, d, "github.com/x/y@main")

	if _, err := svc.Link(ctx, wsID, "github.com/x/y@main"); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	if _, err := svc.Link(ctx, wsID, "github.com/x/y@main"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Link: got %v, want ErrDuplicate", err)
	}
}

func TestLink_RejectsNonIndexed(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsID := seedWorkspace(t, d, "platform")
	// Seed without forcing status=indexed — default is 'pending'.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(`
		INSERT INTO projects (
			host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash
		) VALUES (?, ?, '[]', '{}', '{}', 'pending', ?, ?, ?)`,
		"github.com/x/y@main", "github.com/x/y@main", now, now,
		db.HashHostPath("github.com/x/y@main"),
	); err != nil {
		t.Fatalf("seed pending project: %v", err)
	}

	if _, err := svc.Link(ctx, wsID, "github.com/x/y@main"); !errors.Is(err, ErrProjectNotIndexed) {
		t.Fatalf("got %v, want ErrProjectNotIndexed", err)
	}
}

func TestLink_RejectsMissingTargets(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()

	// Unknown workspace.
	if _, err := svc.Link(ctx, "no-such-ws", "github.com/x/y@main"); !errors.Is(err, ErrWorkspaceMissing) {
		t.Fatalf("missing workspace: got %v, want ErrWorkspaceMissing", err)
	}

	wsID := seedWorkspace(t, d, "platform")
	// Workspace exists but project doesn't.
	if _, err := svc.Link(ctx, wsID, "github.com/missing/proj@main"); !errors.Is(err, ErrProjectMissing) {
		t.Fatalf("missing project: got %v, want ErrProjectMissing", err)
	}
}

func TestUnlink(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsID := seedWorkspace(t, d, "platform")
	seedIndexedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Link(ctx, wsID, "github.com/x/y@main"); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if err := svc.Unlink(ctx, wsID, "github.com/x/y@main"); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if err := svc.Unlink(ctx, wsID, "github.com/x/y@main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Unlink: got %v, want ErrNotFound", err)
	}
}

// TestListByWorkspace exercises the "newest first" ordering — the
// dashboard uses this to show the most recently linked project at the
// top of the workspace detail page.
func TestListByWorkspace(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsID := seedWorkspace(t, d, "platform")
	seedIndexedProject(t, d, "github.com/a/older@main")
	seedIndexedProject(t, d, "github.com/b/newer@main")

	// Link older first, sleep enough for distinct ISO seconds, link newer.
	if _, err := svc.Link(ctx, wsID, "github.com/a/older@main"); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := svc.Link(ctx, wsID, "github.com/b/newer@main"); err != nil {
		t.Fatalf("second Link: %v", err)
	}

	list, err := svc.ListByWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 memberships, got %d", len(list))
	}
	if list[0].ProjectPath != "github.com/b/newer@main" {
		t.Errorf("newest-first ordering broken: %+v", list)
	}
}

// TestListByProject covers the reverse lookup — which workspaces
// contain a given project. Used by the project detail page chips.
func TestListByProject(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsA := seedWorkspace(t, d, "alpha")
	wsB := seedWorkspace(t, d, "beta")
	seedIndexedProject(t, d, "github.com/x/y@main")

	for _, ws := range []string{wsA, wsB} {
		if _, err := svc.Link(ctx, ws, "github.com/x/y@main"); err != nil {
			t.Fatalf("Link %s: %v", ws, err)
		}
	}
	list, err := svc.ListByProject(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 memberships, got %d", len(list))
	}
}

// TestDeletingProject_CascadesMembership confirms the schema's FK
// ON DELETE CASCADE actually fires: removing the projects row drops
// every workspace_projects row referencing it.
func TestDeletingProject_CascadesMembership(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	wsA := seedWorkspace(t, d, "alpha")
	wsB := seedWorkspace(t, d, "beta")
	seedIndexedProject(t, d, "github.com/x/y@main")
	for _, ws := range []string{wsA, wsB} {
		if _, err := svc.Link(ctx, ws, "github.com/x/y@main"); err != nil {
			t.Fatalf("Link: %v", err)
		}
	}

	if _, err := d.ExecContext(ctx, `DELETE FROM projects WHERE host_path = ?`, "github.com/x/y@main"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	memberships, err := svc.ListByProject(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(memberships) != 0 {
		t.Fatalf("expected memberships cascaded to 0, got %d", len(memberships))
	}
}
