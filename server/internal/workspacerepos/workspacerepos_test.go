package workspacerepos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// withWorkspace creates a workspaces row and returns its id. Tests need a
// real FK target since workspace_repos.workspace_id has ON DELETE CASCADE.
func withWorkspace(t *testing.T) (*Service, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ws, err := workspaces.New(d).Create(context.Background(), "ws", "")
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return New(d), ws.ID
}

func TestCreateAndGet(t *testing.T) {
	svc, wsID := withWorkspace(t)
	ctx := context.Background()
	wr, err := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID,
		GitHubURL:   "https://github.com/spf13/cobra",
		Branch:      "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wr.ProjectPath != "github.com/spf13/cobra@main" {
		t.Fatalf("unexpected project_path %q", wr.ProjectPath)
	}
	if wr.WebhookSecret == "" {
		t.Fatalf("webhook secret should be auto-generated")
	}
	if wr.Status != StatusPending {
		t.Fatalf("expected pending status, got %q", wr.Status)
	}

	got, err := svc.GetByID(ctx, wr.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ProjectPath != wr.ProjectPath {
		t.Fatalf("get/create mismatch")
	}
}

func TestURLNormalisation(t *testing.T) {
	svc, wsID := withWorkspace(t)
	ctx := context.Background()
	// trailing slash + .git suffix should be collapsed.
	wr, err := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID,
		GitHubURL:   "https://github.com/spf13/cobra.git/",
		Branch:      "main",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wr.GitHubURL != "https://github.com/spf13/cobra" {
		t.Fatalf("URL not canonicalised, got %q", wr.GitHubURL)
	}
	if wr.ProjectPath != "github.com/spf13/cobra@main" {
		t.Fatalf("project_path wrong: %q", wr.ProjectPath)
	}
}

func TestDuplicateRejected(t *testing.T) {
	svc, wsID := withWorkspace(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID, GitHubURL: "https://github.com/x/y", Branch: "main",
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID, GitHubURL: "https://github.com/x/y", Branch: "main",
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	// Different branch should succeed.
	if _, err := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID, GitHubURL: "https://github.com/x/y", Branch: "develop",
	}); err != nil {
		t.Fatalf("different branch should succeed: %v", err)
	}
}

func TestInvalidURL(t *testing.T) {
	svc, wsID := withWorkspace(t)
	cases := []string{
		"",
		"not a url",
		"https://gitlab.com/x/y",
		"https://github.com",
		"https://github.com/onlyowner",
	}
	for _, c := range cases {
		_, err := svc.Create(context.Background(), CreateRequest{
			WorkspaceID: wsID, GitHubURL: c, Branch: "main",
		})
		if !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("URL %q: expected ErrInvalidURL, got %v", c, err)
		}
	}
}

func TestSetStatus(t *testing.T) {
	svc, wsID := withWorkspace(t)
	ctx := context.Background()
	wr, _ := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID, GitHubURL: "https://github.com/x/y", Branch: "main",
	})
	now := time.Now().UTC()
	if err := svc.SetStatus(ctx, wr.ID, StatusIndexed, "abc123", "", &now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := svc.GetByID(ctx, wr.ID)
	if got.Status != StatusIndexed || got.LastSHA != "abc123" {
		t.Fatalf("status/sha not persisted: %+v", got)
	}
	if got.LastIndexedAt == nil {
		t.Fatalf("LastIndexedAt should be set")
	}
}

func TestDeleteCascade(t *testing.T) {
	svc, wsID := withWorkspace(t)
	ctx := context.Background()
	wr, _ := svc.Create(ctx, CreateRequest{
		WorkspaceID: wsID, GitHubURL: "https://github.com/a/b", Branch: "main",
	})
	if err := svc.Delete(ctx, wr.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, wr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
