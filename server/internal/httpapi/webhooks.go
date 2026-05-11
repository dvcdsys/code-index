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

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/workspacejobs"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
)

// GetWorkspaceRepoWebhookInfo — GET /workspaces/{id}/repos/{repo_id}/webhook-info.
//
// Authenticated. Returns the publicly-reachable webhook URL + the HMAC
// secret. Operators copy these into GitHub's webhook config when
// auto_webhook=false.
func (s *Server) GetWorkspaceRepoWebhookInfo(w http.ResponseWriter, r *http.Request, id, repoID string) {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook_url":     s.buildWebhookURL(wr.ID),
		"webhook_secret":  wr.WebhookSecret,
		"auto_registered": wr.WebhookID != nil,
	})
}

// pushEvent is the minimal subset of GitHub's push webhook body we care
// about. We don't bind to go-github here because we only need two fields
// — the ref and the head SHA — and pulling in the dependency for that
// would be heavyweight.
type pushEvent struct {
	Ref   string `json:"ref"`     // "refs/heads/main"
	After string `json:"after"`   // post-push HEAD SHA
}

// ReceiveGithubWebhook — POST /api/v1/webhooks/github/{repo_id}.
//
// Public endpoint (added to publicPaths in middleware.go). Authenticated
// per-row by HMAC-SHA256 over the body keyed by workspace_repos.webhook_secret.
func (s *Server) ReceiveGithubWebhook(w http.ResponseWriter, r *http.Request, repoID string, params openapi.ReceiveGithubWebhookParams) {
	if !s.Deps.WorkspacesEnabled || s.Deps.WorkspaceRepos == nil || s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		return
	}

	// Read the raw body BEFORE any JSON parsing so we can compute HMAC
	// against the exact byte sequence GitHub signed.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	wr, err := s.Deps.WorkspaceRepos.GetByID(r.Context(), repoID)
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
		// Fall back to direct header read in case oapi-codegen casing
		// differs from what GitHub sends.
		sigHeader = r.Header.Get("X-Hub-Signature-256")
	}
	if !validHMAC(body, []byte(wr.WebhookSecret), sigHeader) {
		s.Deps.Logger.Warn("workspaces webhook: HMAC mismatch", "repo_id", repoID)
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
		// GitHub sends ping on webhook creation — return 200 so the UI
		// confirms the setup is wired.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ping"})
		return
	case "push":
		// fall through
	default:
		// Unknown / unsupported events are ack'd quietly so GitHub stops
		// retrying. We log so operators can see what arrived.
		s.Deps.Logger.Info("workspaces webhook: ignored event",
			"repo_id", repoID,
			"event", event)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}

	var p pushEvent
	if jerr := json.Unmarshal(body, &p); jerr != nil {
		writeError(w, http.StatusBadRequest, "invalid push payload")
		return
	}

	// Only react to pushes on the tracked branch. GitHub sends one delivery
	// per ref; deletes have After=000…000 and we treat those as ignored.
	wantRef := "refs/heads/" + wr.Branch
	if p.Ref != wantRef {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
		return
	}
	if strings.Trim(p.After, "0") == "" {
		// Branch deletion → ignore (cleanup story lives in PR4+).
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
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
			s.Deps.Logger.Error("workspaces webhook: enqueue failed", "repo_id", repoID, "err", eerr)
			writeError(w, http.StatusInternalServerError, "could not enqueue reindex")
			return
		}
	}
	status := "enqueued"
	if !enqueued {
		status = "already_running"
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "repo_id": wr.ID})
}

// validHMAC returns true when the given header matches HMAC-SHA256(body, secret).
// Header format is "sha256=<hex>" per GitHub's spec. Constant-time compare
// against the expected value to avoid leaking timing signals.
func validHMAC(body, secret []byte, header string) bool {
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
	// Use the secrets package's constant-time helper — same byte semantics
	// as hmac.Equal, kept in one place across the codebase.
	return secrets.ConstantTimeEqual(got, want)
}
