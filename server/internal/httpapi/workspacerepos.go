package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/workspacejobs"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

type workspaceRepoPayload struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	GitHubURL     string     `json:"github_url"`
	Branch        string     `json:"branch"`
	ProjectPath   string     `json:"project_path"`
	TokenID       *string    `json:"token_id"`
	AutoWebhook   bool       `json:"auto_webhook"`
	WebhookMode   string     `json:"webhook_mode"`
	Status        string     `json:"status"`
	LastSHA       *string    `json:"last_sha"`
	LastError     *string    `json:"last_error"`
	LastIndexedAt *time.Time `json:"last_indexed_at"`
	IsLinked      bool       `json:"is_linked"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func workspaceRepoToPayload(wr workspacerepos.WorkspaceRepo) workspaceRepoPayload {
	var tokenID *string
	if wr.TokenID != "" {
		v := wr.TokenID
		tokenID = &v
	}
	var lastSHA *string
	if wr.LastSHA != "" {
		v := wr.LastSHA
		lastSHA = &v
	}
	var lastErr *string
	if wr.LastError != "" {
		v := wr.LastError
		lastErr = &v
	}
	mode := wr.WebhookMode
	if mode == "" {
		mode = workspacerepos.WebhookModeManual
	}
	return workspaceRepoPayload{
		ID:            wr.ID,
		WorkspaceID:   wr.WorkspaceID,
		GitHubURL:     wr.GitHubURL,
		Branch:        wr.Branch,
		ProjectPath:   wr.ProjectPath,
		TokenID:       tokenID,
		AutoWebhook:   wr.AutoWebhook,
		WebhookMode:   mode,
		Status:        wr.Status,
		LastSHA:       lastSHA,
		LastError:     lastErr,
		LastIndexedAt: wr.LastIndexedAt,
		IsLinked:      wr.IsLinked,
		CreatedAt:     wr.CreatedAt,
		UpdatedAt:     wr.UpdatedAt,
	}
}

// workspaceReposUnavailable returns 503 when the feature flag is off OR
// any required service is nil.
func (s *Server) workspaceReposUnavailable(w http.ResponseWriter) bool {
	if !s.Deps.WorkspacesEnabled || s.Deps.WorkspaceRepos == nil || s.Deps.Workspaces == nil || s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		return true
	}
	return false
}

// requireWorkspace loads the parent workspace and returns 404 if missing.
// Used by every workspace-scoped endpoint to make "wrong workspace id"
// vs "wrong repo id" distinguishable in error responses.
func (s *Server) requireWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	_, err := s.Deps.Workspaces.GetByID(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, workspaces.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load workspace")
		}
		return false
	}
	return true
}

// ListWorkspaceRepos — GET /api/v1/workspaces/{id}/repos.
func (s *Server) ListWorkspaceRepos(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if !s.requireWorkspace(w, r, id) {
		return
	}
	list, err := s.Deps.WorkspaceRepos.ListByWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list repos")
		return
	}
	out := make([]workspaceRepoPayload, 0, len(list))
	for _, wr := range list {
		out = append(out, workspaceRepoToPayload(wr))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repos": out,
		"total": len(out),
	})
}

// AddWorkspaceRepo — POST /api/v1/workspaces/{id}/repos.
//
// Creates the workspace_repos row + enqueues the clone_repo job (which
// chains to index_repo on success). Response carries the freshly-minted
// webhook_secret + a constructed webhook_url so the operator can set up
// the GitHub webhook manually (or wait for PR3's auto-register flow).
func (s *Server) AddWorkspaceRepo(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if !s.requireWorkspace(w, r, id) {
		return
	}
	var body openapi.AddWorkspaceRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	req := workspacerepos.CreateRequest{
		WorkspaceID: id,
		GitHubURL:   body.GithubUrl,
		Branch:      body.Branch,
	}
	if body.TokenId != nil {
		req.TokenID = *body.TokenId
	}
	if body.AutoWebhook != nil {
		req.AutoWebhook = *body.AutoWebhook
	}
	if body.WebhookMode != nil {
		req.WebhookMode = string(*body.WebhookMode)
	}
	wr, err := s.Deps.WorkspaceRepos.Create(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, workspacerepos.ErrInvalidURL):
			writeError(w, http.StatusUnprocessableEntity, "github_url must be an https://github.com/owner/repo URL")
		case errors.Is(err, workspacerepos.ErrBranchEmpty):
			writeError(w, http.StatusUnprocessableEntity, "branch is required")
		case errors.Is(err, workspacerepos.ErrInvalidWebhookMode):
			writeError(w, http.StatusUnprocessableEntity, "webhook_mode must be one of manual, auto, disabled")
		case errors.Is(err, workspacerepos.ErrDuplicate):
			writeError(w, http.StatusConflict, "this repo+branch is already attached to the workspace")
		default:
			writeError(w, http.StatusInternalServerError, "could not attach repo")
		}
		return
	}

	if err := workspacejobs.EnqueueClone(r.Context(), s.Deps.Jobs, wr.ID); err != nil {
		// Row created, job not — surface the error but leave the row.
		// A manual reindex will retry the clone.
		writeError(w, http.StatusInternalServerError, "repo attached but clone could not be enqueued: "+err.Error())
		return
	}

	webhookURL := s.buildWebhookURL(wr.ID)
	autoRegistered := false
	autoNote := ""
	if wr.AutoWebhook {
		ok, note := s.tryAutoRegisterWebhook(r.Context(), wr, webhookURL)
		autoRegistered = ok
		autoNote = note
		if ok {
			// Reload so the response reflects the persisted webhook_id.
			if fresh, ferr := s.Deps.WorkspaceRepos.GetByID(r.Context(), wr.ID); ferr == nil {
				wr = fresh
			}
		}
	}

	resp := map[string]any{
		"repo":            workspaceRepoToPayload(wr),
		"webhook_url":     webhookURL,
		"webhook_secret":  wr.WebhookSecret,
		"auto_registered": autoRegistered,
	}
	if autoNote != "" {
		resp["auto_register_note"] = autoNote
	}
	writeJSON(w, http.StatusCreated, resp)
}

// tryAutoRegisterWebhook calls the GitHub API to register a push hook for
// the given repo. Best-effort — failure does NOT roll back the
// workspace_repos row; the operator can rerun manually via the
// webhook-info endpoint. Returns (success, human-readable note).
//
// Public URL is required — without it GitHub would deliver to a path
// that's not reachable. We refuse to attempt the call when PublicBaseURL
// is empty and surface that as the note.
func (s *Server) tryAutoRegisterWebhook(ctx context.Context, wr workspacerepos.WorkspaceRepo, deliveryURL string) (bool, string) {
	logger := s.Deps.Logger
	if !strings.HasPrefix(deliveryURL, "http") {
		return false, "CIX_PUBLIC_URL is not set — register the webhook manually"
	}
	if wr.TokenID == "" {
		return false, "auto_webhook=true requires a token_id with admin:repo_hook scope"
	}
	pat, err := s.Deps.GithubTokens.Reveal(ctx, wr.TokenID)
	if err != nil {
		if errors.Is(err, githubtokens.ErrNotFound) {
			return false, "token_id not found"
		}
		return false, "could not decrypt the GitHub token"
	}
	_ = s.Deps.GithubTokens.Touch(ctx, wr.TokenID)

	owner, repo, perr := githubapi.ParseOwnerRepo(wr.GitHubURL)
	if perr != nil {
		return false, "github_url is not a parseable owner/repo URL"
	}
	hr, herr := githubapi.New().CreateWebhook(ctx, githubapi.CreateWebhookOptions{
		Owner:  owner,
		Repo:   repo,
		PAT:    pat,
		URL:    deliveryURL,
		Secret: wr.WebhookSecret,
	})
	if herr != nil {
		if logger != nil {
			logger.Warn("workspaces: auto-register webhook failed",
				"repo_id", wr.ID, "owner", owner, "repo", repo, "err", herr)
		}
		if errors.Is(herr, githubapi.ErrUnauthorized) {
			return false, "GitHub rejected the token — add admin:repo_hook scope or register manually"
		}
		return false, "GitHub API rejected the call: " + herr.Error()
	}
	if uerr := s.Deps.WorkspaceRepos.SetWebhookID(ctx, wr.ID, hr.ID); uerr != nil && logger != nil {
		logger.Warn("workspaces: could not persist webhook id", "repo_id", wr.ID, "err", uerr)
	}
	return true, ""
}

// DeleteWorkspaceRepo — DELETE /api/v1/workspaces/{id}/repos/{repo_id}.
func (s *Server) DeleteWorkspaceRepo(w http.ResponseWriter, r *http.Request, id, repoID string) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if !s.requireWorkspace(w, r, id) {
		return
	}
	// Authorisation: a repo only belongs to its workspace, so we also
	// require the repo's workspace_id to match. Otherwise users could
	// detach repos across workspaces by guessing ids.
	existing, err := s.Deps.WorkspaceRepos.GetByID(r.Context(), repoID)
	if err != nil {
		if errors.Is(err, workspacerepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load repo")
		return
	}
	if existing.WorkspaceID != id {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	if err := s.Deps.WorkspaceRepos.Delete(r.Context(), repoID); err != nil {
		if errors.Is(err, workspacerepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete repo")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReindexWorkspaceRepo — POST /api/v1/workspaces/{id}/repos/{repo_id}/reindex.
func (s *Server) ReindexWorkspaceRepo(w http.ResponseWriter, r *http.Request, id, repoID string) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if !s.requireWorkspace(w, r, id) {
		return
	}
	wr, err := s.Deps.WorkspaceRepos.GetByID(r.Context(), repoID)
	if err != nil {
		if errors.Is(err, workspacerepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load repo")
		return
	}
	if wr.WorkspaceID != id {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}

	enqueued := true
	if _, eerr := s.Deps.Jobs.Enqueue(r.Context(), jobs.EnqueueRequest{
		Type:      workspacejobs.TypeCloneRepo,
		DedupeKey: "clone:" + wr.ID,
		Payload:   workspacejobs.ClonePayload{RepoID: wr.ID},
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
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": status,
		"repo":   workspaceRepoToPayload(wr),
	})
}

// buildWebhookURL constructs the publicly-reachable webhook delivery URL
// for a workspace_repo. When PublicBaseURL is empty (no operator-set
// origin), returns only the path so the dashboard can render it with a
// helper note.
func (s *Server) buildWebhookURL(repoID string) string {
	path := "/api/v1/webhooks/github/" + repoID
	base := strings.TrimRight(s.Deps.PublicBaseURL, "/")
	if base == "" {
		return path
	}
	return base + path
}

// LinkExistingProject — POST /api/v1/workspaces/{id}/repos/link.
//
// Attaches an already-indexed project to the workspace as a lightweight
// linked row. No clone, no index job, no webhook. The response mirrors
// AddWorkspaceRepo's shape so the dashboard can reuse the same refresh
// pattern; webhook_url + webhook_secret are empty because linked rows
// have no webhook to register.
func (s *Server) LinkExistingProject(w http.ResponseWriter, r *http.Request, id string) {
	if s.workspaceReposUnavailable(w) {
		return
	}
	if !s.requireWorkspace(w, r, id) {
		return
	}
	var body openapi.LinkExistingProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	hash := strings.TrimSpace(body.ProjectHash)
	if hash == "" {
		writeError(w, http.StatusUnprocessableEntity, "project_hash is required")
		return
	}

	// Resolve the project by hash so we can validate status + extract
	// host_path. The same lookup is used by /projects/{path} so the
	// behaviour is consistent — 404 for unknown hashes, 422 for known
	// but not-yet-indexed projects.
	proj, perr := projects.GetByHash(r.Context(), s.Deps.DB, hash)
	if perr != nil {
		if errors.Is(perr, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if proj.Status != "indexed" {
		writeError(w, http.StatusUnprocessableEntity,
			"project is not yet indexed (status="+proj.Status+") — wait for indexing to complete before linking")
		return
	}

	wr, err := s.Deps.WorkspaceRepos.CreateLink(r.Context(), id, proj.HostPath)
	if err != nil {
		switch {
		case errors.Is(err, workspacerepos.ErrInvalidURL):
			writeError(w, http.StatusUnprocessableEntity,
				"project host_path is not a github.com/owner/repo@branch — local-path projects cannot be linked")
		case errors.Is(err, workspacerepos.ErrBranchEmpty):
			writeError(w, http.StatusUnprocessableEntity, "project host_path has no branch suffix")
		case errors.Is(err, workspacerepos.ErrDuplicate):
			writeError(w, http.StatusConflict, "this repo+branch is already attached to the workspace")
		default:
			writeError(w, http.StatusInternalServerError, "could not link project")
		}
		return
	}

	// Mirror AddWorkspaceRepo's envelope so the dashboard can decode one
	// shape regardless of which create path it called. Linked rows have
	// no webhook, so URL/secret are empty.
	writeJSON(w, http.StatusCreated, map[string]any{
		"repo":            workspaceRepoToPayload(wr),
		"webhook_url":     "",
		"webhook_secret":  "",
		"auto_registered": false,
	})
}
