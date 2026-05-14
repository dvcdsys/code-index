package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/workspacejobs"
)

// gitReposUnavailable returns 503 when the workspaces feature flag is
// off OR any required service is nil. Single source for the message so
// the dashboard's "feature off" UI key is stable.
func (s *Server) gitReposUnavailable(w http.ResponseWriter) bool {
	if !s.Deps.WorkspacesEnabled || s.Deps.GitRepos == nil || s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		return true
	}
	return false
}

// gitRepoPayload mirrors the OpenAPI GitRepo schema.
type gitRepoPayload struct {
	ProjectPath string  `json:"project_path"`
	PathHash    string  `json:"path_hash"`
	GitHubURL   string  `json:"github_url"`
	Branch      string  `json:"branch"`
	TokenID     *string `json:"token_id"`
	AutoWebhook bool    `json:"auto_webhook"`
	WebhookMode string  `json:"webhook_mode"`
	LastSHA     *string `json:"last_sha"`
	LastError   *string `json:"last_error"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func gitRepoToPayload(g gitrepos.GitRepo) gitRepoPayload {
	var tokenID *string
	if g.TokenID != "" {
		v := g.TokenID
		tokenID = &v
	}
	var lastSHA *string
	if g.LastSHA != "" {
		v := g.LastSHA
		lastSHA = &v
	}
	var lastErr *string
	if g.LastError != "" {
		v := g.LastError
		lastErr = &v
	}
	return gitRepoPayload{
		ProjectPath: g.ProjectPath,
		PathHash:    g.PathHash,
		GitHubURL:   g.GitHubURL,
		Branch:      g.Branch,
		TokenID:     tokenID,
		AutoWebhook: g.AutoWebhook,
		WebhookMode: g.WebhookMode,
		LastSHA:     lastSHA,
		LastError:   lastErr,
		CreatedAt:   g.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:   g.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

// AddGitRepo — POST /api/v1/git-repos.
//
// Creates a projects row (status='pending'), the matching git_repos row,
// and enqueues a clone_repo job. The resulting project belongs to no
// workspace — the caller can attach it via POST /workspaces/{id}/projects.
func (s *Server) AddGitRepo(w http.ResponseWriter, r *http.Request) {
	if s.gitReposUnavailable(w) {
		return
	}
	var body openapi.AddGitRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}

	mode := ""
	if body.WebhookMode != nil {
		mode = string(*body.WebhookMode)
	}
	tokenID := ""
	if body.TokenId != nil {
		tokenID = *body.TokenId
	}

	// Parse the URL up front so we know the canonical project_path and
	// can stage the projects row before gitrepos.Create runs (the FK
	// from git_repos.project_path → projects.host_path needs it).
	owner, repo, perr := gitrepos.ParseGitHubURL(body.GithubUrl)
	if perr != nil {
		writeError(w, http.StatusUnprocessableEntity, "github_url must be an https://github.com/owner/repo URL")
		return
	}
	branch := strings.TrimSpace(body.Branch)
	if branch == "" {
		writeError(w, http.StatusUnprocessableEntity, "branch is required")
		return
	}
	projectPath := "github.com/" + owner + "/" + repo + "@" + branch

	// Pre-stage the projects row so the git_repos FK can attach.
	// ErrConflict on a re-add is fine — somebody else (or a previous
	// half-failed attempt) already wrote it; the gitrepos.Create
	// below will surface the real duplicate via UNIQUE on (github_url,
	// branch). ErrOverlap is a hard reject.
	//
	// Fix #5: track whether THIS request created the projects row so
	// we can compensate-delete it on gitrepos.Create failure. Without
	// the rollback a failed request leaves an operator-visible
	// 'pending' orphan with no git_repos and no workspace_projects.
	_, createErr := projects.Create(r.Context(), s.Deps.DB, projects.CreateRequest{HostPath: projectPath})
	projectCreatedHere := createErr == nil
	if createErr != nil && !errors.Is(createErr, projects.ErrConflict) {
		writeError(w, http.StatusUnprocessableEntity, createErr.Error())
		return
	}

	g, err := s.Deps.GitRepos.Create(r.Context(), gitrepos.CreateRequest{
		GitHubURL:   body.GithubUrl,
		Branch:      body.Branch,
		TokenID:     tokenID,
		WebhookMode: mode,
	})
	if err != nil {
		// Compensating delete (Fix #5): drop the project we staged so
		// the failed flow doesn't leave a 'pending' orphan visible in
		// /projects. Guarded by two checks:
		//   (a) projectCreatedHere — never touch a project that
		//       pre-existed; somebody else owns it.
		//   (b) no git_repos row currently FK-references this project
		//       — a concurrent winner may have inserted between our
		//       projects.Create and our gitrepos.Create. Deleting then
		//       would cascade away the winner's git_repo row.
		if projectCreatedHere {
			if _, gerr := s.Deps.GitRepos.GetByPath(r.Context(), projectPath); errors.Is(gerr, gitrepos.ErrNotFound) {
				if derr := projects.Delete(r.Context(), s.Deps.DB, projectPath); derr != nil && s.Deps.Logger != nil {
					s.Deps.Logger.Warn(
						"AddGitRepo: compensating projects.Delete after gitrepos.Create failure failed; an orphan 'pending' project may need manual cleanup",
						"project_path", projectPath,
						"original_err", err,
						"delete_err", derr,
					)
				}
			}
		}
		switch {
		case errors.Is(err, gitrepos.ErrInvalidURL):
			writeError(w, http.StatusUnprocessableEntity, "github_url must be an https://github.com/owner/repo URL")
		case errors.Is(err, gitrepos.ErrBranchEmpty):
			writeError(w, http.StatusUnprocessableEntity, "branch is required")
		case errors.Is(err, gitrepos.ErrInvalidWebhookMode):
			writeError(w, http.StatusUnprocessableEntity, "webhook_mode must be one of manual, auto, disabled")
		case errors.Is(err, gitrepos.ErrDuplicate):
			writeError(w, http.StatusConflict, "a project for this github_url + branch already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not register git repo: "+err.Error())
		}
		return
	}

	if err := workspacejobs.EnqueueClone(r.Context(), s.Deps.Jobs, g.ProjectPath); err != nil {
		writeError(w, http.StatusInternalServerError, "git repo registered but clone could not be enqueued: "+err.Error())
		return
	}

	webhookURL := s.buildWebhookURL(g.PathHash)
	autoRegistered := false
	autoNote := ""
	if g.WebhookMode == gitrepos.WebhookModeAuto {
		ok, note := s.tryAutoRegisterWebhook(r.Context(), g, webhookURL)
		autoRegistered = ok
		autoNote = note
		if ok {
			// Reload so the response reflects the persisted webhook_id.
			if fresh, ferr := s.Deps.GitRepos.GetByPath(r.Context(), g.ProjectPath); ferr == nil {
				g = fresh
			}
		}
	}

	proj, perr := projects.Get(r.Context(), s.Deps.DB, g.ProjectPath)
	if perr != nil {
		writeError(w, http.StatusInternalServerError, "could not reload project: "+perr.Error())
		return
	}
	resp := map[string]any{
		"project":         projectToOpenAPI(proj),
		"git_repo":        gitRepoToPayload(g),
		"webhook_url":     webhookURL,
		"webhook_secret":  g.WebhookSecret,
		"auto_registered": autoRegistered,
	}
	if autoNote != "" {
		resp["auto_register_note"] = autoNote
	}
	writeJSON(w, http.StatusCreated, resp)
}

// GetProjectGitRepo — GET /api/v1/projects/{hash}/git-repo.
func (s *Server) GetProjectGitRepo(w http.ResponseWriter, r *http.Request, hash openapi.ProjectHash) {
	if s.gitReposUnavailable(w) {
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), string(hash))
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no git_repos row for this project (likely a local project)")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, gitRepoToPayload(g))
}

// ReindexProject — POST /api/v1/projects/{hash}/reindex.
//
// Looks up the matching git_repos row and enqueues a clone_repo job
// (which chains into index_repo on success). 422 for local projects
// — they have no clone pipeline and must be reindexed via the CLI.
func (s *Server) ReindexProject(w http.ResponseWriter, r *http.Request, hash openapi.ProjectHash) {
	if s.gitReposUnavailable(w) {
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), string(hash))
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "this project has no git_repos row — reindex via `cix reindex <path>` for local projects")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}

	enqueued := true
	if _, eerr := s.Deps.Jobs.Enqueue(r.Context(), jobs.EnqueueRequest{
		Type:      workspacejobs.TypeCloneRepo,
		DedupeKey: "clone:" + g.PathHash,
		Payload:   workspacejobs.ClonePayload{ProjectPath: g.ProjectPath},
	}); eerr != nil {
		if errors.Is(eerr, jobs.ErrDuplicate) {
			enqueued = false
		} else {
			writeError(w, http.StatusInternalServerError, "could not enqueue reindex")
			return
		}
	}
	status := "enqueued"
	if !enqueued {
		status = "already_running"
	}
	// Flip the project status to "indexing" synchronously so the response
	// (and the dashboard's post-mutation refetch) reflects the reindex
	// without waiting for the worker to pick up the clone_repo job. The
	// worker's own status flip in handleClone is now idempotent for this
	// path but still needed for non-API triggers (e.g. webhook).
	if err := projects.SetStatus(r.Context(), s.Deps.DB, g.ProjectPath, "indexing"); err != nil {
		s.Deps.Logger.Warn("reindex: set status indexing failed", "project", g.ProjectPath, "err", err)
	}
	proj, _ := projects.Get(r.Context(), s.Deps.DB, g.ProjectPath)
	resp := map[string]any{"status": status}
	if proj != nil {
		resp["project"] = projectToOpenAPI(proj)
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// tryAutoRegisterWebhook calls the GitHub API to register a push hook
// for the given git_repo. Best-effort — failure does NOT roll back the
// git_repos row; the operator can rerun manually via webhook-info.
func (s *Server) tryAutoRegisterWebhook(ctx context.Context, g gitrepos.GitRepo, deliveryURL string) (bool, string) {
	logger := s.Deps.Logger
	if !strings.HasPrefix(deliveryURL, "http") {
		return false, "CIX_PUBLIC_URL is not set — register the webhook manually"
	}
	if g.TokenID == "" {
		return false, "auto webhook_mode requires a token_id with admin:repo_hook scope"
	}
	pat, err := s.Deps.GithubTokens.Reveal(ctx, g.TokenID)
	if err != nil {
		if errors.Is(err, githubtokens.ErrNotFound) {
			return false, "token_id not found"
		}
		return false, "could not decrypt the GitHub token"
	}
	_ = s.Deps.GithubTokens.Touch(ctx, g.TokenID)

	owner, repo, perr := githubapi.ParseOwnerRepo(g.GitHubURL)
	if perr != nil {
		return false, "github_url is not a parseable owner/repo URL"
	}
	hr, herr := githubapi.New().CreateWebhook(ctx, githubapi.CreateWebhookOptions{
		Owner:  owner,
		Repo:   repo,
		PAT:    pat,
		URL:    deliveryURL,
		Secret: g.WebhookSecret,
	})
	if herr != nil {
		if logger != nil {
			logger.Warn("workspaces: auto-register webhook failed",
				"project", g.ProjectPath, "owner", owner, "repo", repo, "err", herr)
		}
		if errors.Is(herr, githubapi.ErrUnauthorized) {
			return false, "GitHub rejected the token — add admin:repo_hook scope or register manually"
		}
		return false, "GitHub API rejected the call: " + herr.Error()
	}
	if uerr := s.Deps.GitRepos.SetWebhookID(ctx, g.ProjectPath, hr.ID); uerr != nil && logger != nil {
		logger.Warn("workspaces: could not persist webhook id", "project", g.ProjectPath, "err", uerr)
	}
	return true, ""
}

// buildWebhookURL constructs the publicly-reachable webhook delivery URL
// for a project's path_hash. When PublicBaseURL is empty, returns the
// path only so the dashboard can render with a helper note.
func (s *Server) buildWebhookURL(pathHash string) string {
	path := "/api/v1/webhooks/github/" + pathHash
	base := strings.TrimRight(s.Deps.PublicBaseURL, "/")
	if base == "" {
		return path
	}
	return base + path
}

