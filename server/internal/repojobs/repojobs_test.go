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
