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
	"github.com/dvcdsys/code-index/server/internal/repojobs"
)

// gitReposUnavailable returns 503 when a required GitHub-repo service
// is nil. Main always wires both services; this defensive check exists
// so test harnesses that build a partial Deps still get a clean 503
// rather than a nil-pointer panic.
func (s *Server) gitReposUnavailable(w http.ResponseWriter) bool {
	if s.Deps.GitRepos == nil || s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub repo support is not configured on this server")
		return true
	}
	return false
}

// gitRepoPayload mirrors the OpenAPI GitRepo schema.
type gitRepoPayload struct {
	ProjectPath         string  `json:"project_path"`
	PathHash            string  `json:"path_hash"`
	GitHubURL           string  `json:"github_url"`
	Branch              string  `json:"branch"`
	TokenID             *string `json:"token_id"`
	AutoWebhook         bool    `json:"auto_webhook"`
	WebhookMode         string  `json:"webhook_mode"`
	LastSHA             *string `json:"last_sha"`
	LastError           *string `json:"last_error"`
	PollingEnabled      bool    `json:"polling_enabled"`
	PollIntervalSeconds *int    `json:"poll_interval_seconds"`
	NextPollAt          *string `json:"next_poll_at"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
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
	var pollSecs *int
	if g.PollIntervalSeconds > 0 {
		v := g.PollIntervalSeconds
		pollSecs = &v
	}
	var nextPollAt *string
	if g.NextPollAt != nil {
		v := g.NextPollAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		nextPollAt = &v
	}
	return gitRepoPayload{
		ProjectPath:         g.ProjectPath,
		PathHash:            g.PathHash,
		GitHubURL:           g.GitHubURL,
		Branch:              g.Branch,
		TokenID:             tokenID,
		AutoWebhook:         g.AutoWebhook,
		WebhookMode:         g.WebhookMode,
		LastSHA:             lastSHA,
		LastError:           lastErr,
		PollingEnabled:      g.PollingEnabled,
		PollIntervalSeconds: pollSecs,
		NextPollAt:          nextPollAt,
		CreatedAt:           g.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:           g.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

// AddGitRepo — POST /api/v1/git-repos.
//
// Creates a projects row (status='pending'), the matching git_repos row,
// and enqueues a clone_repo job. The resulting project belongs to no
// workspace — the caller can attach it via POST /workspaces/{id}/projects.
func (s *Server) AddGitRepo(w http.ResponseWriter, r *http.Request) {
	// External projects are admin-administered and ownerless; only admins may
	// add one (it clones a repo, may register a webhook, and uses a stored PAT).
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
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
	pollingEnabled := body.PollingEnabled != nil && *body.PollingEnabled
	pollIntervalSecs := 0
	if body.PollIntervalSeconds != nil {
		pollIntervalSecs = *body.PollIntervalSeconds
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
		GitHubURL:           body.GithubUrl,
		Branch:              body.Branch,
		TokenID:             tokenID,
		WebhookMode:         mode,
		PollingEnabled:      pollingEnabled,
		PollIntervalSeconds: pollIntervalSecs,
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
		case errors.Is(err, gitrepos.ErrPollingRequiresWebhookDisabled):
			writeError(w, http.StatusUnprocessableEntity, "polling requires webhook_mode='disabled'")
		case errors.Is(err, gitrepos.ErrDuplicate):
			writeError(w, http.StatusConflict, "a project for this github_url + branch already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not register git repo: "+err.Error())
		}
		return
	}

	if err := repojobs.EnqueueClone(r.Context(), s.Deps.Jobs, g.ProjectPath); err != nil {
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
		} else {
			// Auto-register failed (typically: the user isn't a repo admin,
			// so the hook can't be installed). Fall back to polling so the
			// repo still auto-syncs. Flips webhook off + polling on.
			if ferr := s.Deps.GitRepos.EnablePollingFallback(r.Context(), g.ProjectPath, pollIntervalSecs); ferr != nil {
				if s.Deps.Logger != nil {
					s.Deps.Logger.Warn("AddGitRepo: polling fallback failed", "project_path", g.ProjectPath, "err", ferr)
				}
			} else {
				autoNote = strings.TrimSpace(note + " Enabled polling sync as a fallback.")
				if fresh, ferr := s.Deps.GitRepos.GetByPath(r.Context(), g.ProjectPath); ferr == nil {
					g = fresh
				}
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
	// Exposes sync config for an external project — admin-only, like the PATCH.
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
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

// UpdateProjectGitRepoSync — PATCH /api/v1/projects/{hash}/git-repo.
//
// Reconfigures how an external project is kept in sync (webhook | polling |
// manual) directly from the project page. 422 on an unknown sync_method,
// 404 for local projects.
func (s *Server) UpdateProjectGitRepoSync(w http.ResponseWriter, r *http.Request, hash string) {
	// Admin-only: this handler decrypts the stored PAT and registers/deletes
	// webhooks on GitHub (via tryAutoRegisterWebhook / deregisterWebhookIfAny),
	// matching the privilege level of ReconcileWebhooks. The dashboard already
	// gates the card behind isAdmin; this closes the direct-API hole where a
	// viewer could PATCH the endpoint and drive those privileged operations.
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.gitReposUnavailable(w) {
		return
	}
	var body openapi.UpdateGitRepoSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no git_repos row for this project (likely a local project)")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}
	interval := 0
	if body.PollIntervalSeconds != nil {
		interval = *body.PollIntervalSeconds
	}

	note := ""
	switch body.SyncMethod {
	case openapi.Polling:
		s.deregisterWebhookIfAny(r.Context(), g)
		if err := s.Deps.GitRepos.SetSync(r.Context(), g.ProjectPath, gitrepos.WebhookModeDisabled, true, interval); err != nil {
			s.writeSyncError(w, err)
			return
		}
	case openapi.Manual:
		s.deregisterWebhookIfAny(r.Context(), g)
		if err := s.Deps.GitRepos.SetSync(r.Context(), g.ProjectPath, gitrepos.WebhookModeDisabled, false, 0); err != nil {
			s.writeSyncError(w, err)
			return
		}
	case openapi.Webhook:
		// Polling off either way. Pick webhook_mode by whether the server can
		// install the hook itself:
		//   - already have a hook → keep mode=auto, don't re-register.
		//   - auto-register succeeds → mode=auto.
		//   - auto-register fails (no admin token / no public URL) → mode=manual:
		//     a perfectly valid webhook the operator finishes by pasting the
		//     URL + secret into GitHub. We do NOT fall back to polling here —
		//     the operator explicitly asked for webhook.
		mode := gitrepos.WebhookModeAuto
		switch {
		case g.WebhookID != nil:
			note = "Webhook already registered."
		case g.WebhookMode == gitrepos.WebhookModeManual:
			// The repo is already a manually-configured webhook: the operator
			// installed the hook in GitHub themselves and we never stored its
			// id (WebhookID == nil). The dashboard collapses webhook_mode
			// auto+manual into a single "Webhook" choice, so re-saving here
			// would otherwise flip the mode to auto and call
			// tryAutoRegisterWebhook — creating a SECOND hook beside the
			// operator's. Preserve manual instead of duplicating.
			mode = gitrepos.WebhookModeManual
			note = "Webhook is configured manually — left as-is. Delete the manual hook in GitHub first if you want the server to manage it."
		default:
			ok, regNote := s.tryAutoRegisterWebhook(r.Context(), g, s.buildWebhookURL(g.PathHash))
			if !ok {
				mode = gitrepos.WebhookModeManual
				note = "Couldn't auto-install the webhook (" + regNote + "). Add it manually: copy the URL and secret below into GitHub → Settings → Webhooks (content type application/json, event: push)."
			}
		}
		if err := s.Deps.GitRepos.SetSync(r.Context(), g.ProjectPath, mode, false, 0); err != nil {
			s.writeSyncError(w, err)
			return
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, "sync_method must be one of webhook, polling, manual")
		return
	}

	fresh, err := s.Deps.GitRepos.GetByPath(r.Context(), g.ProjectPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sync updated but reload failed: "+err.Error())
		return
	}
	resp := map[string]any{"git_repo": gitRepoToPayload(fresh)}
	if note != "" {
		resp["note"] = note
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeSyncError maps gitrepos errors from a sync update to HTTP statuses.
func (s *Server) writeSyncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitrepos.ErrPollingRequiresWebhookDisabled):
		writeError(w, http.StatusUnprocessableEntity, "polling requires webhook_mode='disabled'")
	case errors.Is(err, gitrepos.ErrInvalidWebhookMode):
		writeError(w, http.StatusUnprocessableEntity, "invalid webhook_mode")
	case errors.Is(err, gitrepos.ErrNotFound):
		writeError(w, http.StatusNotFound, "no git_repos row for this project")
	default:
		writeError(w, http.StatusInternalServerError, "could not update sync config: "+err.Error())
	}
}

// deregisterWebhookIfAny best-effort removes a GitHub hook the server
// previously created, when a repo switches away from webhook sync. Failure
// is non-fatal (the receiver ignores pushes for webhook_mode='disabled'
// repos anyway) — but we still clear webhook_id so the dashboard reflects
// reality. No-op when there is no hook recorded.
func (s *Server) deregisterWebhookIfAny(ctx context.Context, g gitrepos.GitRepo) {
	if g.WebhookID == nil {
		return
	}
	if g.TokenID != "" {
		if pat, perr := s.Deps.GithubTokens.Reveal(ctx, g.TokenID); perr == nil {
			if owner, repo, oerr := githubapi.ParseOwnerRepo(g.GitHubURL); oerr == nil {
				if derr := githubapi.New().DeleteWebhook(ctx, owner, repo, pat, *g.WebhookID); derr != nil && s.Deps.Logger != nil {
					s.Deps.Logger.Warn("UpdateProjectGitRepoSync: de-register webhook failed (continuing)",
						"project", g.ProjectPath, "err", derr)
				}
			}
		}
	}
	if cerr := s.Deps.GitRepos.ClearWebhookID(ctx, g.ProjectPath); cerr != nil && s.Deps.Logger != nil {
		s.Deps.Logger.Warn("UpdateProjectGitRepoSync: clear webhook_id failed", "project", g.ProjectPath, "err", cerr)
	}
}

// ReindexProject — POST /api/v1/projects/{hash}/reindex.
//
// Looks up the matching git_repos row and enqueues a clone_repo job
// (which chains into index_repo on success). 422 for local projects
// — they have no clone pipeline and must be reindexed via the CLI.
//
// Query parameter `full=true` clears git_repos.indexed_sha before
// enqueueing the job, which forces the clone-job's mode-determination
// to land on "full" (first-index branch). Use this to recover from
// suspected index drift — manual rebuild without losing tokens or
// workspace memberships.
func (s *Server) ReindexProject(w http.ResponseWriter, r *http.Request, hash string, params openapi.ReindexProjectParams) {
	// Reindex of an external repo is an admin operation (clone + index).
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.gitReposUnavailable(w) {
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "this project has no git_repos row — reindex via `cix reindex <path>` for local projects")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}

	// Force-full path: drop indexed_sha BEFORE enqueueing so the
	// clone_repo handler's mode-determination sees IndexedSHA="" and
	// routes through the full-reindex branch. Doing the clear here
	// (rather than inside the job) means a dashboard refetch
	// immediately reflects the "uncommitted" state.
	forceFull := params.Full != nil && *params.Full
	if forceFull && g.IndexedSHA != "" {
		if err := s.Deps.GitRepos.SetIndexedSHA(r.Context(), g.ProjectPath, ""); err != nil {
			writeError(w, http.StatusInternalServerError, "could not clear indexed_sha for force-full: "+err.Error())
			return
		}
	}

	enqueued := true
	if _, eerr := s.Deps.Jobs.Enqueue(r.Context(), jobs.EnqueueRequest{
		Type:      repojobs.TypeCloneRepo,
		DedupeKey: "clone:" + g.PathHash,
		Payload:   repojobs.ClonePayload{ProjectPath: g.ProjectPath},
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
	resp := map[string]any{"status": status, "mode": "incremental"}
	if forceFull {
		resp["mode"] = "full"
	}
	if proj != nil {
		resp["project"] = projectToOpenAPI(proj)
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// ForceStopIndex — POST /api/v1/projects/{hash}/force-stop.
//
// External projects only. Hard-aborts the clone + index pipeline:
//
//  1. Delete pending/running clone_repo + index_repo jobs so the queue
//     can't retry or resume. Done first so a not-yet-started index job
//     can't sneak in between the cancel and the cleanup.
//  2. Cancel the active in-process index session, which unblocks the
//     next index/begin; the in-flight IndexDir then sees ErrNoSession
//     and stops cleanly (handled in repojobs.handleIndex).
//  3. Settle the project on a terminal status (see below).
//
// 404 when no project resolves to the hash; 422 when the project exists
// but is local (no git_repos row — local projects index via the CLI and
// have no server-side pipeline to stop).
//
// Best-effort by design: chunks already committed are not rolled back,
// and a force-stop landing in the brief clone/fetch window may not catch
// an index job the clone goroutine enqueues a moment later — re-run if so.
func (s *Server) ForceStopIndex(w http.ResponseWriter, r *http.Request, hash string) {
	// Force-stopping an external repo's index run is an admin operation.
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.gitReposUnavailable(w) {
		return
	}
	// Distinguish "no such project" (404) from "local project" (422):
	// GitRepos.GetByHash returns ErrNotFound for both, which would mislead.
	if _, perr := projects.GetByHash(r.Context(), s.Deps.DB, hash); perr != nil {
		if errors.Is(perr, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no project for this hash")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+perr.Error())
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "this is a local project — force-stop only applies to external (git-cloned) projects; cancel a local index via the CLI")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}

	cleared, derr := s.Deps.Jobs.DeleteByDedupeKeys(r.Context(),
		"clone:"+g.PathHash, "index:"+g.PathHash)
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "could not clear pipeline jobs: "+derr.Error())
		return
	}

	cancelled := false
	if s.Deps.Indexer != nil {
		cancelled, err = s.Deps.Indexer.CancelIndexing(r.Context(), g.ProjectPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not cancel index session: "+err.Error())
			return
		}
	}

	// Settle on a terminal status, regardless of whether a live session
	// was found. CancelIndexing flips to 'indexed' unconditionally when a
	// session existed — wrong for a project that was never fully indexed
	// (partial/zero chunks). Best-effort cancel semantics: a repo with no
	// recorded indexed_sha returns to 'created'; otherwise 'indexed'. This
	// also covers the !cancelled case (stopped mid-clone, or nothing ran),
	// where the project could be stuck at 'cloning'/'indexing'.
	status := "created"
	if g.IndexedSHA != "" {
		status = "indexed"
	}
	if serr := projects.SetStatus(r.Context(), s.Deps.DB, g.ProjectPath, status); serr != nil {
		s.Deps.Logger.Warn("force-stop: set terminal status failed", "project", g.ProjectPath, "err", serr)
	}

	resp := map[string]any{"cancelled": cancelled, "jobs_cleared": cleared}
	if proj, _ := projects.Get(r.Context(), s.Deps.DB, g.ProjectPath); proj != nil {
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
	// EnsureWebhook is idempotent: if the repo already has a hook pointing at
	// this delivery URL it reuses (and PATCHes) it instead of POSTing a
	// duplicate, and prunes any accumulated duplicates (issue #68).
	hr, _, herr := githubapi.New().EnsureWebhook(ctx, githubapi.CreateWebhookOptions{
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
// for a project's path_hash. When no public base is known, returns the
// path only so the dashboard can render with a helper note.
func (s *Server) buildWebhookURL(pathHash string) string {
	path := "/api/v1/webhooks/github/" + pathHash
	base := strings.TrimRight(s.publicBaseURL(), "/")
	if base == "" {
		return path
	}
	return base + path
}

// publicBaseURL returns the origin webhook URLs should be built against.
// A live managed tunnel takes precedence over the operator-set
// CIX_PUBLIC_URL, since the tunnel is the path that actually reaches a
// NAT'd server.
func (s *Server) publicBaseURL() string {
	if s.Deps.Tunnel != nil {
		if u := s.Deps.Tunnel.URL(); u != "" {
			return u
		}
	}
	return s.Deps.PublicBaseURL
}
