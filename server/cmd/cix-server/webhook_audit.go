package main

import (
	"context"
	"database/sql"
	"log/slog"
)

// auditWebhookCleanBreak emits a one-line WARN on startup when git_repos
// rows still expose webhook URLs that operators must re-register in
// GitHub. The clean break happened when workspace_repos split into
// git_repos + workspace_projects: the URL went from
// /webhooks/github/{workspace_repos.id} (UUID) to
// /webhooks/github/{path_hash} (16-hex). GitHub will keep POSTing to
// the OLD URL until the operator updates the webhook config, and the
// server now returns 404 — deliveries silently fail.
//
// We can't detect "already re-registered" from the DB (manual webhooks
// have no acknowledgement signal), so the warning persists on every
// startup that still has manual + auto webhooks. That's accepted noise
// — the alternative is silent breakage. Operators who want to silence
// it can switch the rows to webhook_mode='disabled' once they've moved
// off webhook-driven indexing.
//
// Counts are surfaced as structured fields so log scrapers can alert
// on the threshold; the human-readable message points at
// /api/v1/projects/{hash}/webhook-info as the per-project URL source.
func auditWebhookCleanBreak(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	var manualCount, autoCount int
	if err := db.QueryRowContext(ctx,
		// COALESCE wraps SUM because SUM() over an empty table returns
		// NULL — which Scan into *int rejects. Empty table is the
		// fresh-install case and must scan to (0, 0), not fail.
		`SELECT
		     COALESCE(SUM(CASE WHEN webhook_mode = 'manual' THEN 1 ELSE 0 END), 0),
		     COALESCE(SUM(CASE WHEN webhook_mode = 'auto'   THEN 1 ELSE 0 END), 0)
		   FROM git_repos`,
	).Scan(&manualCount, &autoCount); err != nil {
		// git_repos missing only when called against a non-migrated
		// DB (pre-Open) — by the time main.go reaches us the schema
		// is applied, so any error here is a real surprise. Log and
		// move on; this is not load-bearing for boot.
		logger.Warn("webhooks: clean-break audit query failed; skipping", "err", err)
		return
	}
	if manualCount == 0 && autoCount == 0 {
		return
	}
	logger.Warn(
		"webhooks: git_repos rows still point at the OLD webhook URL (workspace_repos UUID). "+
			"The URL format changed when workspace_repos was split into git_repos + workspace_projects; "+
			"GitHub deliveries to the old URL will 404. Re-register in GitHub — per-project URLs are at "+
			"GET /api/v1/projects/{hash}/webhook-info or in the dashboard.",
		"manual_webhooks", manualCount,
		"auto_webhooks", autoCount,
	)
}
