// Package gitrepos is the service layer for the git_repos table —
// clone + webhook metadata for projects that come from a git remote
// (currently GitHub-only). A row exists exactly 1:1 with the projects
// row whose host_path matches project_path; local projects (CLI-indexed
// filesystem paths) have no git_repos row, which is how the rest of the
// system tells them apart from cloneable repos.
//
// Workspace membership lives in a separate junction table —
// workspace_projects — owned by the workspaceprojects package. This
// package knows nothing about workspaces.
package gitrepos

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// Webhook modes. Stored verbatim in the webhook_mode column so the
// dashboard renders the operator's stated intent.
const (
	WebhookModeManual   = "manual"
	WebhookModeAuto     = "auto"
	WebhookModeDisabled = "disabled"
)

// Errors.
var (
	ErrNotFound           = errors.New("git repo not found")
	ErrDuplicate          = errors.New("a git repo with this (github_url, branch) already exists")
	ErrInvalidURL         = errors.New("github_url must be an https://github.com/owner/repo URL")
	ErrBranchEmpty        = errors.New("branch is required")
	ErrInvalidWebhookMode = errors.New("webhook_mode must be one of manual, auto, disabled")
	// ErrPollingRequiresWebhookDisabled is returned when polling is enabled
	// on a repo whose webhook_mode is not 'disabled'. A repo syncs via
	// webhook OR polling, never both.
	ErrPollingRequiresWebhookDisabled = errors.New("polling requires webhook_mode='disabled'")
)

// GitRepo is the wire view. The webhook_secret is in the response of
// Create only — subsequent reads must call WebhookInfo to fetch it
// (kept out of bulk lists so secrets don't fan out unnecessarily).
type GitRepo struct {
	ProjectPath   string
	PathHash      string
	GitHubURL     string
	Branch        string
	TokenID       string
	WebhookSecret string
	WebhookID     *int64
	WebhookMode   string
	AutoWebhook   bool
	LastSHA       string
	// IndexedSHA is the SHA of the commit whose tree is currently
	// reflected in the cix index for this repo. Empty when the repo
	// has never been indexed with the incremental pipeline (legacy
	// rows pre-migration 7; freshly-cloned-but-not-yet-indexed rows
	// during the indexing window). Drives the incremental reindex
	// path: tree.Diff between IndexedSHA and the post-fetch HEAD
	// gives the exact change set, no file hashing needed.
	IndexedSHA string
	LastError  string
	// PollingEnabled gates this repo into the shared poll scheduler. Only
	// valid when WebhookMode == 'disabled' (webhook XOR polling).
	PollingEnabled bool
	// PollIntervalSeconds is the per-repo poll cadence. 0 → use the server
	// default (CIX_DEFAULT_POLL_INTERVAL). Always read through
	// EffectivePollInterval, which applies the default and the floor.
	PollIntervalSeconds int
	// NextPollAt is the absolute time this repo is next due for a poll.
	// nil → not scheduled (polling off). Set to now+interval at the END of
	// each clone/index cycle so cadence is measured from the last index run.
	NextPollAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Service wraps the git_repos table.
type Service struct {
	DB *sql.DB
}

func New(db *sql.DB) *Service { return &Service{DB: db} }

// CreateRequest is what handlers pass in. ProjectPath is computed from
// GitHubURL + Branch; callers don't supply it directly.
type CreateRequest struct {
	GitHubURL   string
	Branch      string
	TokenID     string // optional
	WebhookMode string // empty → manual
	// PollingEnabled opts the repo into polling sync. Requires the
	// effective WebhookMode to be 'disabled' (webhook XOR polling),
	// otherwise Create returns ErrPollingRequiresWebhookDisabled.
	PollingEnabled bool
	// PollIntervalSeconds is the optional per-repo cadence (0 → default).
	PollIntervalSeconds int
}

// Create inserts a git_repos row. The caller is responsible for
// ensuring the matching projects row exists (FK target). The resulting
// ProjectPath is "github.com/owner/repo@branch".
func (s *Service) Create(ctx context.Context, req CreateRequest) (GitRepo, error) {
	owner, repo, err := parseGitHubURL(req.GitHubURL)
	if err != nil {
		return GitRepo{}, err
	}
	if strings.TrimSpace(req.Branch) == "" {
		return GitRepo{}, ErrBranchEmpty
	}
	mode, merr := NormaliseWebhookMode(req.WebhookMode)
	if merr != nil {
		return GitRepo{}, merr
	}
	if req.PollingEnabled && mode != WebhookModeDisabled {
		return GitRepo{}, ErrPollingRequiresWebhookDisabled
	}

	projectPath := fmt.Sprintf("github.com/%s/%s@%s", owner, repo, req.Branch)
	githubURL := canonicaliseURL(req.GitHubURL)
	secret, err := generateWebhookSecret()
	if err != nil {
		return GitRepo{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	auto := 0
	if mode == WebhookModeAuto {
		auto = 1
	}
	tokenID := nullableString(req.TokenID)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pollEnabled := 0
	var pollSecs any
	var nextPollAt any
	if req.PollingEnabled {
		pollEnabled = 1
		if req.PollIntervalSeconds > 0 {
			pollSecs = req.PollIntervalSeconds
		}
		// Schedule the first poll promptly; the scheduler picks it up on
		// its next tick and the completion handler then measures cadence
		// from the end of the first index run.
		nextPollAt = now
	}

	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO git_repos (
			project_path, github_url, branch,
			token_id, webhook_secret,
			webhook_mode, auto_webhook,
			polling_enabled, poll_interval_seconds, next_poll_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectPath, githubURL, req.Branch,
		tokenID, secret, mode, auto,
		pollEnabled, pollSecs, nextPollAt,
		now, now,
	); err != nil {
		if isUniqueConstraintViolation(err) {
			return GitRepo{}, ErrDuplicate
		}
		return GitRepo{}, fmt.Errorf("insert git_repo: %w", err)
	}
	return s.GetByPath(ctx, projectPath)
}

// GetByPath returns the git_repos row for the given project_path
// (= projects.host_path).
func (s *Service) GetByPath(ctx context.Context, projectPath string) (GitRepo, error) {
	row := s.DB.QueryRowContext(ctx, selectColumns+` WHERE project_path = ?`, projectPath)
	return scanRow(row)
}

// GetByHash resolves a git_repos row by the 16-char SHA1 prefix of
// project_path (= projects.path_hash). The webhook endpoint uses this
// — it's stable across the system and doubles as the on-disk clone dir
// identifier.
func (s *Service) GetByHash(ctx context.Context, pathHash string) (GitRepo, error) {
	var path string
	if err := s.DB.QueryRowContext(ctx,
		`SELECT host_path FROM projects WHERE path_hash = ?`, pathHash,
	).Scan(&path); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GitRepo{}, ErrNotFound
		}
		return GitRepo{}, fmt.Errorf("lookup by path_hash: %w", err)
	}
	return s.GetByPath(ctx, path)
}

// ListAll returns every git_repos row, newest first. Local projects do
// not appear here — they have no git_repos representation.
func (s *Service) ListAll(ctx context.Context) ([]GitRepo, error) {
	rows, err := s.DB.QueryContext(ctx, selectColumns+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list git_repos: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// SetWebhookID persists the GitHub-side hook id after the auto-register
// flow registers the webhook. ErrNotFound when the row is gone.
func (s *Service) SetWebhookID(ctx context.Context, projectPath string, hookID int64) error {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos SET webhook_id = ?, updated_at = ?
		WHERE project_path = ?`,
		hookID, time.Now().UTC().Format(time.RFC3339Nano), projectPath)
	if err != nil {
		return fmt.Errorf("set webhook_id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetClone updates last_sha / last_error after a clone job completes.
// Pass empty strings to leave the corresponding field unchanged (NULL
// to clear last_error explicitly is not supported — callers should
// pass "" for "no error" which CASE-clears it).
func (s *Service) SetClone(ctx context.Context, projectPath, lastSHA, lastError string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos
		   SET last_sha   = COALESCE(NULLIF(?, ''), last_sha),
		       last_error = CASE WHEN ? = '' THEN NULL ELSE ? END,
		       updated_at = ?
		 WHERE project_path = ?`,
		lastSHA, lastError, lastError, now, projectPath)
	if err != nil {
		return fmt.Errorf("set clone: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetIndexedSHA records the SHA whose tree is now reflected in the
// index for this repo. Called by the index_repo job handler after a
// successful FinishIndexing. Empty sha clears the column — used by
// the force-full path before re-enqueueing a clone job.
func (s *Service) SetIndexedSHA(ctx context.Context, projectPath, sha string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var arg any
	if sha == "" {
		arg = nil
	} else {
		arg = sha
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos
		   SET indexed_sha = ?,
		       updated_at  = ?
		 WHERE project_path = ?`,
		arg, now, projectPath)
	if err != nil {
		return fmt.Errorf("set indexed_sha: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDue returns polling-enabled repos whose next_poll_at has elapsed,
// oldest-due first. The shared poll scheduler calls this every tick.
func (s *Service) ListDue(ctx context.Context, now time.Time) ([]GitRepo, error) {
	rows, err := s.DB.QueryContext(ctx,
		selectColumns+` WHERE polling_enabled = 1
		                  AND next_poll_at IS NOT NULL
		                  AND next_poll_at <= ?
		                ORDER BY next_poll_at`,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("list due polls: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// SetSync atomically rewrites a repo's sync configuration: webhook_mode,
// the auto_webhook mirror bool, polling on/off, and the poll interval.
// Enforces the webhook-XOR-polling rule (polling requires
// webhookMode='disabled', else ErrPollingRequiresWebhookDisabled).
// When polling is enabled it schedules the first poll immediately
// (next_poll_at=now); otherwise next_poll_at is cleared. intervalSeconds<=0
// stores NULL (server default). It does NOT touch webhook_id — hook
// registration / de-registration is the caller's concern (see the
// UpdateProjectGitRepoSync handler). ErrNotFound when the row is gone.
func (s *Service) SetSync(ctx context.Context, projectPath, webhookMode string, pollingEnabled bool, intervalSeconds int) error {
	mode, err := NormaliseWebhookMode(webhookMode)
	if err != nil {
		return err
	}
	if pollingEnabled && mode != WebhookModeDisabled {
		return ErrPollingRequiresWebhookDisabled
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auto := 0
	if mode == WebhookModeAuto {
		auto = 1
	}
	pe := 0
	var pollSecs any
	var nextPoll any
	if pollingEnabled {
		pe = 1
		if intervalSeconds > 0 {
			pollSecs = intervalSeconds
		}
		nextPoll = now
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos
		   SET webhook_mode          = ?,
		       auto_webhook          = ?,
		       polling_enabled       = ?,
		       poll_interval_seconds = ?,
		       next_poll_at          = ?,
		       updated_at            = ?
		 WHERE project_path = ?`,
		mode, auto, pe, pollSecs, nextPoll, now, projectPath)
	if err != nil {
		return fmt.Errorf("set sync: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

// ClearWebhookID nulls the stored GitHub hook id — called after the hook is
// de-registered (or assumed gone) when a repo switches away from webhook
// sync, so the dashboard doesn't show a stale hook. ErrNotFound when gone.
func (s *Service) ClearWebhookID(ctx context.Context, projectPath string) error {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos SET webhook_id = NULL, updated_at = ?
		 WHERE project_path = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), projectPath)
	if err != nil {
		return fmt.Errorf("clear webhook_id: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

// EnablePollingFallback atomically switches a repo from a failed
// webhook-auto-register into polling sync: webhook_mode='disabled',
// auto_webhook=0, polling_enabled=1, and next_poll_at=now (poll promptly).
// Used by AddGitRepo when the user requested an auto webhook but lacks
// admin rights on the repo. intervalSeconds <= 0 → NULL (server default).
// ErrNotFound when the row is gone.
func (s *Service) EnablePollingFallback(ctx context.Context, projectPath string, intervalSeconds int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pollSecs any
	if intervalSeconds > 0 {
		pollSecs = intervalSeconds
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos
		   SET webhook_mode          = ?,
		       auto_webhook          = 0,
		       polling_enabled       = 1,
		       poll_interval_seconds = ?,
		       next_poll_at          = ?,
		       updated_at            = ?
		 WHERE project_path = ?`,
		WebhookModeDisabled, pollSecs, now, now, projectPath)
	if err != nil {
		return fmt.Errorf("enable polling fallback: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

// RescheduleNextPoll sets next_poll_at to an absolute time. Used by the
// scheduler (provisional floor at enqueue) and the clone/index completion
// handlers (authoritative "interval from end of indexing"). No-op-safe on
// non-polling repos — callers gate on PollingEnabled. ErrNotFound when gone.
func (s *Service) RescheduleNextPoll(ctx context.Context, projectPath string, at time.Time) error {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE git_repos
		   SET next_poll_at = ?,
		       updated_at   = ?
		 WHERE project_path = ?`,
		at.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		projectPath)
	if err != nil {
		return fmt.Errorf("reschedule next poll: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

// Delete removes a git_repos row. Idempotent — re-deleting returns
// ErrNotFound. The matching projects row + on-disk clone are NOT
// cleaned up here; that's the project-delete handler's job.
func (s *Service) Delete(ctx context.Context, projectPath string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM git_repos WHERE project_path = ?`, projectPath)
	if err != nil {
		return fmt.Errorf("delete git_repo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- helpers ---

const selectColumns = `
	SELECT project_path, github_url, branch,
	       token_id, webhook_secret, webhook_id,
	       webhook_mode, auto_webhook,
	       last_sha, indexed_sha, last_error,
	       polling_enabled, poll_interval_seconds, next_poll_at,
	       created_at, updated_at
	  FROM git_repos`

func scanRow(r interface{ Scan(dest ...any) error }) (GitRepo, error) {
	var (
		g           GitRepo
		tokenID     sql.NullString
		webhookID   sql.NullInt64
		webhookMode string
		autoWebhook int
		lastSHA     sql.NullString
		indexedSHA  sql.NullString
		lastError   sql.NullString
		pollEnabled int
		pollSecs    sql.NullInt64
		nextPollAt  sql.NullString
		createdAt   string
		updatedAt   string
	)
	err := r.Scan(&g.ProjectPath, &g.GitHubURL, &g.Branch,
		&tokenID, &g.WebhookSecret, &webhookID,
		&webhookMode, &autoWebhook,
		&lastSHA, &indexedSHA, &lastError,
		&pollEnabled, &pollSecs, &nextPollAt,
		&createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GitRepo{}, ErrNotFound
		}
		return GitRepo{}, fmt.Errorf("scan git_repo: %w", err)
	}
	g.PathHash = HashHostPath(g.ProjectPath)
	g.TokenID = tokenID.String
	if webhookID.Valid {
		v := webhookID.Int64
		g.WebhookID = &v
	}
	g.WebhookMode = webhookMode
	if g.WebhookMode == "" {
		g.WebhookMode = WebhookModeManual
	}
	g.AutoWebhook = autoWebhook == 1
	g.LastSHA = lastSHA.String
	g.IndexedSHA = indexedSHA.String
	g.LastError = lastError.String
	g.PollingEnabled = pollEnabled == 1
	if pollSecs.Valid {
		g.PollIntervalSeconds = int(pollSecs.Int64)
	}
	if nextPollAt.Valid && nextPollAt.String != "" {
		if t, perr := time.Parse(time.RFC3339Nano, nextPollAt.String); perr == nil {
			g.NextPollAt = &t
		}
	}
	g.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	g.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return g, nil
}

func scanRows(rows *sql.Rows) ([]GitRepo, error) {
	out := []GitRepo{}
	for rows.Next() {
		g, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EffectivePollInterval resolves the cadence (in seconds) for a repo:
// the per-repo value when set, else defaultSecs, clamped up to minSecs.
func EffectivePollInterval(repo GitRepo, defaultSecs, minSecs int) int {
	secs := repo.PollIntervalSeconds
	if secs <= 0 {
		secs = defaultSecs
	}
	if minSecs > 0 && secs < minSecs {
		secs = minSecs
	}
	return secs
}

// rowsAffectedOrNotFound maps a zero-row UPDATE/DELETE to ErrNotFound.
func rowsAffectedOrNotFound(res sql.Result) error {
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// NormaliseWebhookMode rejects unknown values up front so the DB only
// ever stores one of the three documented states. Empty input maps to
// the default 'manual'.
func NormaliseWebhookMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return WebhookModeManual, nil
	case WebhookModeManual:
		return WebhookModeManual, nil
	case WebhookModeAuto:
		return WebhookModeAuto, nil
	case WebhookModeDisabled:
		return WebhookModeDisabled, nil
	default:
		return "", ErrInvalidWebhookMode
	}
}

// ParseGitHubURL extracts owner + repo from an HTTPS GitHub URL.
// Accepts trailing slash and ".git" suffix; rejects anything not on
// github.com. Exported so the HTTP handler can resolve the canonical
// project_path before staging the projects row.
func ParseGitHubURL(s string) (owner, repo string, err error) {
	return parseGitHubURL(s)
}

// parseGitHubURL extracts owner + repo from an HTTPS GitHub URL. Accepts
// trailing slash and ".git" suffix. Rejects anything not on github.com.
func parseGitHubURL(s string) (owner, repo string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", ErrInvalidURL
	}
	u, perr := url.Parse(s)
	if perr != nil {
		return "", "", ErrInvalidURL
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return "", "", ErrInvalidURL
	}
	p := strings.Trim(u.Path, "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidURL
	}
	return parts[0], parts[1], nil
}

func canonicaliseURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

func generateWebhookSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// HashHostPath is a thin re-export of db.HashHostPath so callers within
// the gitrepos package don't need a separate import.
func HashHostPath(path string) string { return db.HashHostPath(path) }
