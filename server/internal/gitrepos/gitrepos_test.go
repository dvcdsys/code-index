package gitrepos

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// seedProject inserts the minimum projects row the git_repos FK requires.
// We don't go through the projects service to keep the test focused on
// gitrepos and free of any indirect coupling.
func seedProject(t *testing.T, d *sql.DB, hostPath string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(`
		INSERT INTO projects (
			host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash
		) VALUES (?, ?, '[]', '{}', '{}', 'pending', ?, ?, ?)`,
		hostPath, hostPath, now, now, db.HashHostPath(hostPath),
	); err != nil {
		t.Fatalf("seed project %s: %v", hostPath, err)
	}
}

func mustOpen(t *testing.T) (*sql.DB, *Service) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, New(d)
}

func TestCreate_HappyPath(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/spf13/cobra@main")

	g, err := svc.Create(ctx, CreateRequest{
		GitHubURL: "https://github.com/spf13/cobra",
		Branch:    "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.ProjectPath != "github.com/spf13/cobra@main" {
		t.Errorf("ProjectPath = %q", g.ProjectPath)
	}
	if g.GitHubURL != "https://github.com/spf13/cobra" {
		t.Errorf("GitHubURL = %q", g.GitHubURL)
	}
	if g.WebhookMode != WebhookModeManual {
		t.Errorf("default WebhookMode = %q, want manual", g.WebhookMode)
	}
	if g.WebhookSecret == "" {
		t.Error("WebhookSecret was not generated")
	}
	if g.PathHash != db.HashHostPath(g.ProjectPath) {
		t.Errorf("PathHash mismatch: %q", g.PathHash)
	}
}

func TestCreate_RejectsBadURL(t *testing.T) {
	_, svc := mustOpen(t)
	cases := []struct {
		name string
		body CreateRequest
		want error
	}{
		{"empty url", CreateRequest{GitHubURL: "", Branch: "main"}, ErrInvalidURL},
		{"non-github host", CreateRequest{GitHubURL: "https://gitlab.com/x/y", Branch: "main"}, ErrInvalidURL},
		{"missing branch", CreateRequest{GitHubURL: "https://github.com/x/y", Branch: ""}, ErrBranchEmpty},
		{"missing repo", CreateRequest{GitHubURL: "https://github.com/x", Branch: "main"}, ErrInvalidURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(context.Background(), tc.body); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCreate_Duplicate guards the UNIQUE(github_url, branch) constraint
// — two git_repos rows for the same upstream + branch must not coexist
// even if the operator typed slightly different casings.
func TestCreate_Duplicate(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/foo/bar@main")

	if _, err := svc.Create(ctx, CreateRequest{
		GitHubURL: "https://github.com/foo/bar",
		Branch:    "main",
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, CreateRequest{
		GitHubURL: "https://github.com/foo/bar",
		Branch:    "main",
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second create: got %v, want ErrDuplicate", err)
	}
}

func TestGetByHash(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Create(ctx, CreateRequest{
		GitHubURL: "https://github.com/x/y",
		Branch:    "main",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	hash := db.HashHostPath("github.com/x/y@main")
	g, err := svc.GetByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if g.ProjectPath != "github.com/x/y@main" {
		t.Errorf("ProjectPath via hash mismatch: %q", g.ProjectPath)
	}
	if _, err := svc.GetByHash(ctx, "0000000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown hash: got %v, want ErrNotFound", err)
	}
}

func TestSetClone_UpdatesLastSHA(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Create(ctx, CreateRequest{GitHubURL: "https://github.com/x/y", Branch: "main"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetClone(ctx, "github.com/x/y@main", "deadbeef", ""); err != nil {
		t.Fatalf("SetClone: %v", err)
	}
	g, err := svc.GetByPath(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if g.LastSHA != "deadbeef" {
		t.Errorf("LastSHA = %q, want deadbeef", g.LastSHA)
	}
}

// TestSetIndexedSHA covers the round-trip for the column that drives
// the incremental reindex decision: tree.Diff(prev=indexed_sha, new=HEAD).
// Three behaviours that must hold:
//   - fresh row → IndexedSHA empty
//   - SetIndexedSHA(sha) → IndexedSHA == sha on next GetByPath
//   - SetIndexedSHA("") → IndexedSHA cleared back to empty (force-full path)
func TestSetIndexedSHA(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Create(ctx, CreateRequest{GitHubURL: "https://github.com/x/y", Branch: "main"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	g, err := svc.GetByPath(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("initial GetByPath: %v", err)
	}
	if g.IndexedSHA != "" {
		t.Errorf("fresh row IndexedSHA = %q, want empty", g.IndexedSHA)
	}

	if err := svc.SetIndexedSHA(ctx, "github.com/x/y@main", "abc123"); err != nil {
		t.Fatalf("SetIndexedSHA: %v", err)
	}
	g, err = svc.GetByPath(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("GetByPath after Set: %v", err)
	}
	if g.IndexedSHA != "abc123" {
		t.Errorf("IndexedSHA after Set = %q, want abc123", g.IndexedSHA)
	}

	// Force-full path: empty arg clears the column. NULL on disk again.
	if err := svc.SetIndexedSHA(ctx, "github.com/x/y@main", ""); err != nil {
		t.Fatalf("SetIndexedSHA clear: %v", err)
	}
	g, err = svc.GetByPath(ctx, "github.com/x/y@main")
	if err != nil {
		t.Fatalf("GetByPath after clear: %v", err)
	}
	if g.IndexedSHA != "" {
		t.Errorf("IndexedSHA after clear = %q, want empty", g.IndexedSHA)
	}
}

// TestSetIndexedSHA_UnknownProject covers the missing-row error path.
// ErrNotFound rather than a silent no-op makes the force-full handler
// fail loudly when the operator races a project deletion.
func TestSetIndexedSHA_UnknownProject(t *testing.T) {
	_, svc := mustOpen(t)
	err := svc.SetIndexedSHA(context.Background(), "github.com/never/existed@main", "abc")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetIndexedSHA(unknown) = %v, want ErrNotFound", err)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Create(ctx, CreateRequest{GitHubURL: "https://github.com/x/y", Branch: "main"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, "github.com/x/y@main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, "github.com/x/y@main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete: got %v, want ErrNotFound", err)
	}
}

// TestDeletingProject_CascadesToGitRepo guards the schema FK behaviour:
// removing a row from projects must drop the matching git_repos row via
// ON DELETE CASCADE so the project-level delete handler can stay simple.
func TestDeletingProject_CascadesToGitRepo(t *testing.T) {
	d, svc := mustOpen(t)
	ctx := context.Background()
	seedProject(t, d, "github.com/x/y@main")
	if _, err := svc.Create(ctx, CreateRequest{GitHubURL: "https://github.com/x/y", Branch: "main"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := d.ExecContext(ctx, `DELETE FROM projects WHERE host_path = ?`, "github.com/x/y@main"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := svc.GetByPath(ctx, "github.com/x/y@main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("git_repos row should have cascade-deleted, got %v", err)
	}
}
