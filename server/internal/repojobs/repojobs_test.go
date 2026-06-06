package repojobs

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/jobs"
)

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

// TestReconcileStuckProjects covers the startup recovery for external
// projects a killed process left in 'indexing' with no job to drive them:
// they flip to 'error' (so the dashboard is honest + the user can Sync to
// resume), while a project with a still-queued job, and local projects
// (no git_repos), are left untouched.
func TestReconcileStuckProjects(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	gr := gitrepos.New(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	setStatus := func(host, status string) {
		if _, err := d.Exec(`UPDATE projects SET status=? WHERE host_path=?`, status, host); err != nil {
			t.Fatalf("set status %s: %v", host, err)
		}
	}
	getStatus := func(host string) string {
		var s string
		if err := d.QueryRow(`SELECT status FROM projects WHERE host_path=?`, host).Scan(&s); err != nil {
			t.Fatalf("get status %s: %v", host, err)
		}
		return s
	}

	// (1) External project stuck 'indexing' with NO job → should become 'error'.
	seedProject(t, d, "github.com/a/a@main")
	if _, err := gr.Create(ctx, gitrepos.CreateRequest{GitHubURL: "https://github.com/a/a", Branch: "main"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	setStatus("github.com/a/a@main", "indexing")

	// (2) External project stuck 'indexing' WITH a pending index job → left alone.
	seedProject(t, d, "github.com/b/b@main")
	if _, err := gr.Create(ctx, gitrepos.CreateRequest{GitHubURL: "https://github.com/b/b", Branch: "main"}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	setStatus("github.com/b/b@main", "indexing")
	js := jobs.New(d, jobs.Options{Logger: logger})
	if _, err := js.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeIndexRepo,
		DedupeKey: "index:" + db.HashHostPath("github.com/b/b@main"),
		Payload:   IndexPayload{ProjectPath: "github.com/b/b@main"},
	}); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	// (3) Local project (no git_repos) stuck 'indexing' → must NOT be touched.
	seedProject(t, d, "/local/proj")
	setStatus("/local/proj", "indexing")

	ReconcileStuckProjects(ctx, d, logger)

	if got := getStatus("github.com/a/a@main"); got != "error" {
		t.Errorf("project a: status=%q, want 'error' (stuck, no job)", got)
	}
	if got := getStatus("github.com/b/b@main"); got != "indexing" {
		t.Errorf("project b: status=%q, want 'indexing' (has a pending job)", got)
	}
	if got := getStatus("/local/proj"); got != "indexing" {
		t.Errorf("local project: status=%q, want 'indexing' (no git_repos — out of scope)", got)
	}
}

// TestReschedulePoll covers the "interval from end of indexing" write that
// the clone/index completion handlers make: a polling repo's next_poll_at is
// pushed to ~now+interval; a non-polling repo is left untouched.
func TestReschedulePoll(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	gr := gitrepos.New(d)
	deps := Deps{
		DB:                         d,
		GitRepos:                   gr,
		Logger:                     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DefaultPollIntervalSeconds: 300,
		MinPollIntervalSeconds:     60,
	}

	// Polling repo with a 120s interval.
	seedProject(t, d, "github.com/a/a@main")
	if _, err := gr.Create(ctx, gitrepos.CreateRequest{
		GitHubURL: "https://github.com/a/a", Branch: "main",
		WebhookMode:    gitrepos.WebhookModeDisabled,
		PollingEnabled: true, PollIntervalSeconds: 120,
	}); err != nil {
		t.Fatalf("create polling repo: %v", err)
	}
	g, err := gr.GetByPath(ctx, "github.com/a/a@main")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}

	before := time.Now()
	deps.reschedulePoll(ctx, g)
	after, err := gr.GetByPath(ctx, "github.com/a/a@main")
	if err != nil {
		t.Fatalf("GetByPath after: %v", err)
	}
	if after.NextPollAt == nil {
		t.Fatal("next_poll_at is nil after reschedule")
	}
	gotDelta := after.NextPollAt.Sub(before)
	if gotDelta < 110*time.Second || gotDelta > 130*time.Second {
		t.Errorf("next_poll_at delta = %v, want ~120s", gotDelta)
	}

	// Non-polling repo: reschedulePoll is a no-op.
	seedProject(t, d, "github.com/b/b@main")
	if _, err := gr.Create(ctx, gitrepos.CreateRequest{
		GitHubURL: "https://github.com/b/b", Branch: "main",
	}); err != nil {
		t.Fatalf("create webhook repo: %v", err)
	}
	gb, _ := gr.GetByPath(ctx, "github.com/b/b@main")
	deps.reschedulePoll(ctx, gb)
	gb2, _ := gr.GetByPath(ctx, "github.com/b/b@main")
	if gb2.NextPollAt != nil {
		t.Errorf("non-polling repo got next_poll_at = %v, want nil", gb2.NextPollAt)
	}
}
