package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// githubTokenPayload mirrors openapi.GithubToken on the wire. Plaintext is
// never carried — only the metadata. The plaintext only ever surfaces on
// the very first POST response (see CreateGithubToken), and only because
// the caller already supplied it in the request.
type githubTokenPayload struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func githubTokenToPayload(t githubtokens.Token) githubTokenPayload {
	scopes := t.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return githubTokenPayload{
		ID:         t.ID,
		Name:       t.Name,
		Scopes:     scopes,
		CreatedAt:  t.CreatedAt,
		LastUsedAt: t.LastUsedAt,
	}
}

// githubTokensUnavailable returns 503 when the feature flag is off OR the
// service is nil (e.g. no encryption key configured at boot).
func (s *Server) githubTokensUnavailable(w http.ResponseWriter) bool {
	if !s.Deps.WorkspacesEnabled || s.Deps.GithubTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		return true
	}
	return false
}

// ListGithubTokens — GET /api/v1/github-tokens.
func (s *Server) ListGithubTokens(w http.ResponseWriter, r *http.Request) {
	if s.githubTokensUnavailable(w) {
		return
	}
	list, err := s.Deps.GithubTokens.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list github tokens")
		return
	}
	out := make([]githubTokenPayload, 0, len(list))
	for _, t := range list {
		out = append(out, githubTokenToPayload(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": out,
		"total":  len(out),
	})
}

// CreateGithubToken — POST /api/v1/github-tokens. The plaintext token
// arrives in the request body, gets encrypted, and is then dropped on the
// floor — only the metadata view comes back to the caller. We deliberately
// do NOT echo the plaintext in the response: the caller already has it,
// and re-serialising it would be a needless place for it to leak.
func (s *Server) CreateGithubToken(w http.ResponseWriter, r *http.Request) {
	if s.githubTokensUnavailable(w) {
		return
	}
	var body openapi.CreateGithubTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	scopes := []string{}
	if body.Scopes != nil {
		scopes = *body.Scopes
	}
	tok, err := s.Deps.GithubTokens.Create(r.Context(), body.Name, body.Token, scopes)
	if err != nil {
		switch {
		case errors.Is(err, githubtokens.ErrNameEmpty):
			writeError(w, http.StatusUnprocessableEntity, "name is required")
		case errors.Is(err, githubtokens.ErrEmpty):
			writeError(w, http.StatusUnprocessableEntity, "token value is required")
		case errors.Is(err, githubtokens.ErrNameTaken):
			writeError(w, http.StatusConflict, "token name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "could not store github token")
		}
		return
	}
	writeJSON(w, http.StatusCreated, githubTokenToPayload(tok))
}

// DeleteGithubToken — DELETE /api/v1/github-tokens/{id}.
func (s *Server) DeleteGithubToken(w http.ResponseWriter, r *http.Request, id string) {
	if s.githubTokensUnavailable(w) {
		return
	}
	if err := s.Deps.GithubTokens.Delete(r.Context(), id); err != nil {
		if errors.Is(err, githubtokens.ErrNotFound) {
			writeError(w, http.StatusNotFound, "github token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete github token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
