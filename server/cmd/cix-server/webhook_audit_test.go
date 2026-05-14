package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// capturingLogger returns a slog.Logger that writes text-formatted
// records into the supplied buffer so the test can inspect them with
// strings.Contains.
func capturingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// seedGitRepo inserts a project + git_repo pair so the audit query has
// real data to count. project_path is the FK target — git_repos.project_path
// references projects.host_path.
func seedGitRepo(t *testing.T, d *sql.DB, hostPath, mode string) {
	t.Helper()
	if _, err := d.Exec(`
		INSERT INTO projects (host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'indexed', '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z', ?)`,
		hostPath, hostPath, db.HashHostPath(hostPath),
	); err != nil {
		t.Fatalf("seed project %s: %v", hostPath, err)
	}
	if _, err := d.Exec(`
		INSERT INTO git_repos (project_path, github_url, branch, webhook_secret,
			webhook_mode, created_at, updated_at)
		VALUES (?, ?, 'main', 'secret-' || ?, ?, '2026-05-14T00:00:00Z', '2026-05-14T00:00:00Z')`,
		hostPath, "https://"+hostPath, mode, mode,
	); err != nil {
		t.Fatalf("seed git_repo %s/%s: %v", hostPath, mode, err)
	}
}

// TestAuditWebhookCleanBreak_NoRows — fresh DB with no git_repos rows:
// the audit must stay silent. A WARN on an empty DB would spam every
// test run plus every dev boot.
func TestAuditWebhookCleanBreak_NoRows(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	buf := &bytes.Buffer{}
	auditWebhookCleanBreak(context.Background(), d, capturingLogger(buf))

	if buf.Len() != 0 {
		t.Errorf("expected silent audit on empty DB, got:\n%s", buf.String())
	}
}

// TestAuditWebhookCleanBreak_ManualAndAuto — both modes exist + one
// 'disabled' row that must NOT be counted. Single WARN line, two
// structured fields with the right counts.
func TestAuditWebhookCleanBreak_ManualAndAuto(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	seedGitRepo(t, d, "github.com/x/manual1@main", "manual")
	seedGitRepo(t, d, "github.com/x/manual2@main", "manual")
	seedGitRepo(t, d, "github.com/x/manual3@main", "manual")
	seedGitRepo(t, d, "github.com/x/auto1@main", "auto")
	seedGitRepo(t, d, "github.com/x/auto2@main", "auto")
	seedGitRepo(t, d, "github.com/x/disabled1@main", "disabled")

	buf := &bytes.Buffer{}
	auditWebhookCleanBreak(context.Background(), d, capturingLogger(buf))

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level, got:\n%s", out)
	}
	if !strings.Contains(out, "manual_webhooks=3") {
		t.Errorf("expected manual_webhooks=3, got:\n%s", out)
	}
	if !strings.Contains(out, "auto_webhooks=2") {
		t.Errorf("expected auto_webhooks=2, got:\n%s", out)
	}
	// Disabled rows must not be counted as either; presence of any
	// "disabled" mention in the log is fine, but the counts must
	// match the manual+auto totals exactly.
	if strings.Count(out, "webhook_mode=") > 0 {
		t.Errorf("audit log should not surface webhook_mode= field, got:\n%s", out)
	}
	if !strings.Contains(out, "/api/v1/projects/{hash}/webhook-info") {
		t.Errorf("expected pointer to per-project webhook-info endpoint, got:\n%s", out)
	}
}

// TestAuditWebhookCleanBreak_OnlyDisabled — webhook_mode='disabled' is
// the operator opting out of webhook delivery entirely. No
// re-registration is needed and the audit must stay silent.
func TestAuditWebhookCleanBreak_OnlyDisabled(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	seedGitRepo(t, d, "github.com/x/d1@main", "disabled")
	seedGitRepo(t, d, "github.com/x/d2@main", "disabled")

	buf := &bytes.Buffer{}
	auditWebhookCleanBreak(context.Background(), d, capturingLogger(buf))

	if buf.Len() != 0 {
		t.Errorf("expected silent audit on disabled-only rows, got:\n%s", buf.String())
	}
}
