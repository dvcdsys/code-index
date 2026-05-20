package pollscheduler

import (
	"context"
	"database/sql"
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

// addPollingRepo creates a webhook-disabled, polling-enabled git_repos row
// and forces its next_poll_at into the past so the scheduler picks it up.
func addPollingRepo(t *testing.T, d *sql.DB, gr *gitrepos.Service, url, branch string) string {
	t.Helper()
	owner, repo, err := gitrepos.ParseGitHubURL(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	projectPath := "github.com/" + owner + "/" + repo + "@" + branch
	seedProject(t, d, projectPath)
	if _, err := gr.Create(context.Background(), gitrepos.CreateRequest{
		GitHubURL:      url,
		Branch:         branch,
		WebhookMode:    gitrepos.WebhookModeDisabled,
		PollingEnabled: true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if err := gr.RescheduleNextPoll(context.Background(), projectPath, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	return projectPath
}

// TestTickOnce_EnqueuesAndReschedules: a due polling repo gets a clone_repo
// job and its next_poll_at advances into the future (provisional floor).
func TestTickOnce_EnqueuesAndReschedules(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	gr := gitrepos.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1}) // not started — no workers drain
	projectPath := addPollingRepo(t, d, gr, "https://github.com/a/a", "main")

	s := New(gr, jobsSvc, time.Minute, 300, 60, nil)
	s.tickOnce(ctx)

	// Exactly one clone_repo job should be queued.
	jobList, err := jobsSvc.List(ctx, "", "clone_repo", 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList) != 1 {
		t.Fatalf("clone_repo jobs = %d, want 1", len(jobList))
	}

	// next_poll_at must have advanced into the future so the next tick
	// won't re-enqueue the same repo while the job is in flight.
	g, err := gr.GetByPath(ctx, projectPath)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if g.NextPollAt == nil || !g.NextPollAt.After(time.Now()) {
		t.Fatalf("next_poll_at not advanced: %v", g.NextPollAt)
	}

	// A second tick must NOT enqueue a duplicate (repo no longer due).
	s.tickOnce(ctx)
	jobList, err = jobsSvc.List(ctx, "", "clone_repo", 100)
	if err != nil {
		t.Fatalf("list jobs 2: %v", err)
	}
	if len(jobList) != 1 {
		t.Fatalf("after 2nd tick clone_repo jobs = %d, want 1", len(jobList))
	}
}

// TestTickOnce_SkipsNonPolling: a webhook repo (polling off) is never enqueued.
func TestTickOnce_SkipsNonPolling(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	gr := gitrepos.New(d)
	jobsSvc := jobs.New(d, jobs.Options{Concurrency: 1})
	seedProject(t, d, "github.com/w/w@main")
	if _, err := gr.Create(ctx, gitrepos.CreateRequest{
		GitHubURL: "https://github.com/w/w", Branch: "main",
		WebhookMode: gitrepos.WebhookModeManual,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	New(gr, jobsSvc, time.Minute, 300, 60, nil).tickOnce(ctx)

	jobList, err := jobsSvc.List(ctx, "", "clone_repo", 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobList) != 0 {
		t.Fatalf("clone_repo jobs = %d, want 0 (polling off)", len(jobList))
	}
}
