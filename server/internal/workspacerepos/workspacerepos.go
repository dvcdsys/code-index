// Package workspacerepos is the service layer for the workspace_repos
// table — one row per (workspace, github_url, branch). Each row maps 1:1
// to an indexed project (host_path = "github.com/owner/repo@branch").
//
// Lifecycle (PR2):
//
//	create row (status=pending) → enqueue clone_repo job → worker clones
//	→ enqueue index_repo job → worker indexes → status=indexed
//
// PR3 adds webhook delivery → enqueue fetch_repo on push; PR4+ feeds
// call-graph + community recompute. This package stays small — handlers
// own service composition; we just persist rows.
package workspacerepos

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

	"github.com/google/uuid"
)

// Status values. Kept as bare strings since they map straight to the DB
// column and the JSON wire format.
const (
	StatusPending  = "pending"  // row created, work not yet scheduled
	StatusCloning  = "cloning"  // clone_repo job running
	StatusIndexing = "indexing" // index_repo job running
	StatusIndexed  = "indexed"  // happy path
	StatusFailed   = "failed"   // last attempt errored (see LastError)
)

// Webhook modes. The legacy AutoWebhook bool stays in the struct for
// backwards compatibility with old API consumers, but new code should
// consult WebhookMode — it carries the operator's stated intent (auto
// vs manual-pending vs deliberately disabled).
const (
	WebhookModeManual   = "manual"
	WebhookModeAuto     = "auto"
	WebhookModeDisabled = "disabled"
)

// NormaliseWebhookMode rejects unknown values up front so the database
// only ever stores one of the three documented states. Empty input maps
// to the default ('manual'), so old API clients that omit the field
// keep working unchanged.
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

// Errors.
var (
	ErrNotFound           = errors.New("workspace repo not found")
	ErrDuplicate          = errors.New("repo is already in this workspace on that branch")
	ErrInvalidURL         = errors.New("github_url must be an https://github.com/owner/repo URL")
	ErrBranchEmpty        = errors.New("branch is required")
	ErrInvalidWebhookMode = errors.New("webhook_mode must be one of manual, auto, disabled")
)

// WorkspaceRepo is the wire view. Tokens themselves are referenced by
// id — Reveal happens server-side via internal/githubtokens.
type WorkspaceRepo struct {
	ID             string
	WorkspaceID    string
	GitHubURL      string
	Branch         string
	ProjectPath    string
	TokenID        string // empty when no PAT is associated (public repo)
	WebhookSecret  string
	WebhookID      *int64 // GitHub hook id (set by PR3 auto-register)
	AutoWebhook    bool
	WebhookMode    string // 'manual' | 'auto' | 'disabled'
	Status         string
	LastSHA        string
	LastError      string
	LastIndexedAt  *time.Time
	IsLinked       bool // true for lightweight references to projects owned by another workspace_repo
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Service wraps the workspace_repos table.
type Service struct {
	DB *sql.DB
}

// New returns a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// CreateRequest is what handlers pass in.
type CreateRequest struct {
	WorkspaceID string
	GitHubURL   string
	Branch      string
	TokenID     string // optional
	AutoWebhook bool   // legacy: kept for old clients; new code uses WebhookMode
	WebhookMode string // 'manual' | 'auto' | 'disabled'; empty = manual
}

// Create inserts a workspace_repo and generates a webhook secret. The
// resulting ProjectPath is "github.com/owner/repo@branch" — the canonical
// id for downstream tables (projects.host_path).
func (s *Service) Create(ctx context.Context, req CreateRequest) (WorkspaceRepo, error) {
	owner, repo, err := parseGitHubURL(req.GitHubURL)
	if err != nil {
		return WorkspaceRepo{}, err
	}
	if strings.TrimSpace(req.Branch) == "" {
		return WorkspaceRepo{}, ErrBranchEmpty
	}
	projectPath := fmt.Sprintf("github.com/%s/%s@%s", owner, repo, req.Branch)

	secret, err := generateWebhookSecret()
	if err != nil {
		return WorkspaceRepo{}, fmt.Errorf("generate webhook secret: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	githubURL := canonicaliseURL(req.GitHubURL)

	// WebhookMode is the source of truth in the DB; AutoWebhook stays
	// derived so the legacy SELECT path keeps working until removed.
	mode, merr := NormaliseWebhookMode(req.WebhookMode)
	if merr != nil {
		return WorkspaceRepo{}, merr
	}
	// If the caller used the legacy bool but left WebhookMode empty,
	// honour the bool — otherwise mode wins.
	if req.WebhookMode == "" && req.AutoWebhook {
		mode = WebhookModeAuto
	}
	auto := 0
	if mode == WebhookModeAuto {
		auto = 1
	}
	tokenID := nullableString(req.TokenID)

	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO workspace_repos (
		   id, workspace_id, github_url, branch, project_path,
		   token_id, webhook_secret, auto_webhook, webhook_mode, status,
		   created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.WorkspaceID, githubURL, req.Branch, projectPath,
		tokenID, secret, auto, mode, StatusPending,
		now, now,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return WorkspaceRepo{}, ErrDuplicate
		}
		return WorkspaceRepo{}, fmt.Errorf("insert workspace_repo: %w", err)
	}
	return s.GetByID(ctx, id)
}

// CreateLink inserts a workspace_repo with is_linked=1: a lightweight
// pointer to an already-indexed project. Unlike Create, there is no
// clone job, no webhook, no PAT — the row exists only so the project
// participates in workspace-level features (search, communities,
// the repo list UI). The canonical project must already exist in the
// projects table; the caller (HTTP handler) is responsible for that
// check + the status='indexed' precondition before calling here.
//
// projectPath must be the same canonical form Create produces, i.e.
// "github.com/owner/repo@branch" — we round-trip through parseProjectPath
// so the resulting (workspace_id, github_url, branch) triple matches
// what an owned row would produce. This is what makes the
// UNIQUE(workspace_id, github_url, branch) constraint catch an attempt
// to link the same project that's already attached as owned.
//
// webhook_secret is generated but never used — the column is NOT NULL.
// webhook_mode is set to 'disabled' so the dashboard hides the webhook
// UI for linked rows.
func (s *Service) CreateLink(ctx context.Context, workspaceID, projectPath string) (WorkspaceRepo, error) {
	githubURL, branch, err := parseProjectPath(projectPath)
	if err != nil {
		return WorkspaceRepo{}, err
	}

	secret, err := generateWebhookSecret()
	if err != nil {
		return WorkspaceRepo{}, fmt.Errorf("generate webhook secret: %w", err)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO workspace_repos (
		   id, workspace_id, github_url, branch, project_path,
		   token_id, webhook_secret, auto_webhook, webhook_mode, status,
		   is_linked, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, NULL, ?, 0, ?, ?, 1, ?, ?)`,
		id, workspaceID, githubURL, branch, projectPath,
		secret, WebhookModeDisabled, StatusIndexed,
		now, now,
	)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return WorkspaceRepo{}, ErrDuplicate
		}
		return WorkspaceRepo{}, fmt.Errorf("insert linked workspace_repo: %w", err)
	}
	return s.GetByID(ctx, id)
}

// GetByID returns one row.
func (s *Service) GetByID(ctx context.Context, id string) (WorkspaceRepo, error) {
	row := s.DB.QueryRowContext(ctx, selectColumns+` WHERE id = ?`, id)
	return scanRow(row)
}

// ListByWorkspace returns every repo in a workspace, newest first.
func (s *Service) ListByWorkspace(ctx context.Context, workspaceID string) ([]WorkspaceRepo, error) {
	rows, err := s.DB.QueryContext(ctx,
		selectColumns+` WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// SetStatus is the workhorse called from job handlers. lastSHA / lastError
// / lastIndexedAt are optional — pass empty / nil / nil to leave them
// unchanged.
func (s *Service) SetStatus(ctx context.Context, id, status string, lastSHA, lastError string, indexed *time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// We use a single UPDATE with COALESCE to keep optional fields atomic.
	var indexedStr any
	if indexed != nil {
		indexedStr = indexed.UTC().Format(time.RFC3339Nano)
	} else {
		indexedStr = nil
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE workspace_repos
		   SET status         = ?,
		       last_sha       = COALESCE(NULLIF(?, ''), last_sha),
		       last_error     = CASE WHEN ? = '' THEN NULL ELSE ? END,
		       last_indexed_at = COALESCE(?, last_indexed_at),
		       updated_at     = ?
		 WHERE id = ?`,
		status, lastSHA, lastError, lastError, indexedStr, now, id,
	)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWebhookID persists the GitHub-side hook id returned by the
// auto-register flow. ErrNotFound when the row is gone (race with
// concurrent delete — caller can ignore).
func (s *Service) SetWebhookID(ctx context.Context, id string, hookID int64) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE workspace_repos SET webhook_id = ?, updated_at = ? WHERE id = ?`,
		hookID, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set webhook_id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a workspace_repo. The on-disk clone, indexed project, and
// associated rows are NOT cleaned up here — handlers should enqueue a
// cleanup job (PR3+) or accept the orphan for now.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM workspace_repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace_repo: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- helpers ---

const selectColumns = `
	SELECT id, workspace_id, github_url, branch, project_path,
	       token_id, webhook_secret, webhook_id, auto_webhook,
	       webhook_mode, status, last_sha, last_error, last_indexed_at,
	       is_linked, created_at, updated_at
	  FROM workspace_repos`

func scanRow(r interface{ Scan(dest ...any) error }) (WorkspaceRepo, error) {
	var (
		wr            WorkspaceRepo
		tokenID       sql.NullString
		webhookID     sql.NullInt64
		autoWebhook   int
		webhookMode   string
		lastSHA       sql.NullString
		lastError     sql.NullString
		lastIndexed   sql.NullString
		isLinked      int
		createdAt     string
		updatedAt     string
	)
	err := r.Scan(&wr.ID, &wr.WorkspaceID, &wr.GitHubURL, &wr.Branch, &wr.ProjectPath,
		&tokenID, &wr.WebhookSecret, &webhookID, &autoWebhook,
		&webhookMode, &wr.Status, &lastSHA, &lastError, &lastIndexed,
		&isLinked, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceRepo{}, ErrNotFound
		}
		return WorkspaceRepo{}, fmt.Errorf("scan workspace_repo: %w", err)
	}
	wr.TokenID = tokenID.String
	if webhookID.Valid {
		v := webhookID.Int64
		wr.WebhookID = &v
	}
	wr.AutoWebhook = autoWebhook == 1
	wr.WebhookMode = webhookMode
	if wr.WebhookMode == "" {
		wr.WebhookMode = WebhookModeManual
	}
	wr.LastSHA = lastSHA.String
	wr.LastError = lastError.String
	if lastIndexed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastIndexed.String)
		wr.LastIndexedAt = &t
	}
	wr.IsLinked = isLinked == 1
	wr.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	wr.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return wr, nil
}

func scanRows(rows *sql.Rows) ([]WorkspaceRepo, error) {
	out := []WorkspaceRepo{}
	for rows.Next() {
		wr, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wr)
	}
	return out, rows.Err()
}

// parseGitHubURL extracts owner + repo from an HTTPS GitHub URL. Accepts
// trailing slash and ".git" suffix. Rejects anything not on github.com so
// we don't accidentally try to clone arbitrary forge URLs (each forge has
// its own quirks — supporting them is out of scope).
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
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidURL
	}
	return parts[0], parts[1], nil
}

// parseProjectPath splits a canonical project_path of the form
// "github.com/owner/repo@branch" back into (github_url, branch) so we
// can reuse the per-workspace uniqueness key when creating a linked
// row from a project hash. Inverse of the Sprintf at Create().
//
// Errors:
//   - empty input or missing "@" → ErrInvalidURL
//   - prefix not "github.com/" → ErrInvalidURL (linked rows only make
//     sense for GitHub-derived projects; local paths can't map to a
//     workspace_repo since the schema requires github_url + branch)
//   - branch portion empty → ErrBranchEmpty
func parseProjectPath(projectPath string) (githubURL, branch string, err error) {
	s := strings.TrimSpace(projectPath)
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return "", "", ErrInvalidURL
	}
	left, right := s[:at], s[at+1:]
	if right == "" {
		return "", "", ErrBranchEmpty
	}
	const prefix = "github.com/"
	if !strings.HasPrefix(left, prefix) {
		return "", "", ErrInvalidURL
	}
	ownerRepo := strings.Trim(left[len(prefix):], "/")
	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidURL
	}
	return "https://github.com/" + parts[0] + "/" + parts[1], right, nil
}

// canonicaliseURL strips trailing slash + ".git" so two forms of the same
// URL aren't treated as distinct repos.
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
