package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspacejobs"
)

// GetProjectWebhookInfo — GET /api/v1/projects/{hash}/webhook-info.
//
// Authenticated. Returns the publicly-reachable webhook URL + the HMAC
// secret. Operators copy these into GitHub's webhook config when
// webhook_mode is not 'auto'. 404 when the project is local (no
// git_repos row).
func (s *Server) GetProjectWebhookInfo(w http.ResponseWriter, r *http.Request, hash openapi.ProjectHash) {
	if s.gitReposUnavailable(w) {
		return
	}
	g, err := s.Deps.GitRepos.GetByHash(r.Context(), string(hash))
	if err != nil {
		if errors.Is(err, gitrepos.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no git_repos row for this project (local projects have no webhook)")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load git_repo: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook_url":     s.buildWebhookURL(g.PathHash),
		"webhook_secret":  g.WebhookSecret,
		"auto_registered": g.WebhookID != nil,
	})
}

// pushEvent is the minimal subset of GitHub's push webhook body we care
// about — the ref and the head SHA.
type pushEvent struct {
	Ref   string `json:"ref"`   // "refs/heads/main"
	After string `json:"after"` // post-push HEAD SHA
}

// ReceiveGithubWebhook — POST /api/v1/webhooks/github/{hash}.
//
// Public endpoint. Authenticated per-row by HMAC-SHA256 over the body
// keyed by git_repos.webhook_secret (looked up via projects.path_hash).
func (s *Server) ReceiveGithubWebhook(w http.ResponseWriter, r *http.Request, hash string, params openapi.ReceiveGithubWebhookParams) {
	if !s.Deps.WorkspacesEnabled || s.Deps.GitRepos == nil || s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	g, err := s.Deps.GitRepos.GetByHash(r.Context(), hash)
	if err != nil {
		// Don't leak existence — both unknown and bad-HMAC look like 404
		// to a probing attacker.
		writeError(w, http.StatusNotFound, "webhook target not found")
		return
	}

	sigHeader := ""
	if params.XHubSignature256 != nil {
		sigHeader = *params.XHubSignature256
	}
	if sigHeader == "" {
		sigHeader = r.Header.Get("X-Hub-Signature-256")
	}
	if !validHMAC(body, []byte(g.WebhookSecret), sigHeader) {
		s.Deps.Logger.Warn("workspaces webhook: HMAC mismatch", "path_hash", hash)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	event := ""
	if params.XGitHubEvent != nil {
		event = *params.XGitHubEvent
	}
	if event == "" {
		event = r.Header.Get("X-GitHub-Event")
	}

	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]any{"status": "ping"})
		return
	case "push":
		// fall through
	default:
		s.Deps.Logger.Info("workspaces webhook: ignored event",
			"path_hash", hash,
			"event", event)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}

	var p pushEvent
	if jerr := json.Unmarshal(body, &p); jerr != nil {
		writeError(w, http.StatusBadRequest, "invalid push payload")
		return
	}

	wantRef := "refs/heads/" + g.Branch
	if p.Ref != wantRef {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}
	if strings.Trim(p.After, "0") == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}

	// Sanity-check the projects row still exists so the job has
	// something to work against. We don't require any particular
	// status — re-clone is the normal recovery path.
	if _, perr := projects.Get(r.Context(), s.Deps.DB, g.ProjectPath); perr != nil {
		s.Deps.Logger.Error("webhook: project missing for git_repo",
			"path_hash", hash, "project", g.ProjectPath, "err", perr)
		writeError(w, http.StatusInternalServerError, "project row missing")
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
			s.Deps.Logger.Error("workspaces webhook: enqueue failed", "path_hash", hash, "err", eerr)
			writeError(w, http.StatusInternalServerError, "could not enqueue reindex")
			return
		}
	}
	status := "enqueued"
	if !enqueued {
		status = "already_running"
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "path_hash": g.PathHash})
}

// validHMAC returns true when the given header matches HMAC-SHA256(body, secret).
// Header format is "sha256=<hex>" per GitHub's spec.
//
// An empty secret is rejected before any computation: HMAC keyed with the
// empty string is a fixed, well-known value over any body (an attacker
// who never saw our secret can forge a matching header). This case
// should be unreachable — git_repos.webhook_secret is NOT NULL and the
// add-repo handler always populates it with a random 32-byte secret —
// but defending here makes the invariant explicit and removes the
// "what if a future caller passes nil" footgun.
func validHMAC(body, secret []byte, header string) bool {
	if len(secret) == 0 {
		return false
	}
	header = strings.TrimSpace(header)
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := mac.Sum(nil)
	return secrets.ConstantTimeEqual(got, want)
}
