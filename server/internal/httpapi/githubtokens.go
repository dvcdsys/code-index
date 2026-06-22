package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// githubAPI returns a per-request GitHub client. The Deps override lets
// tests point at an httptest server; in production BaseURL stays at
// the canonical api.github.com via githubapi.New.
func (s *Server) githubAPI() *githubapi.Client {
	c := githubapi.New()
	if s.Deps.GithubAPIBaseURL != "" {
		c.BaseURL = s.Deps.GithubAPIBaseURL
	}
	return c
}

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

// githubTokensUnavailable returns 503 when the github_tokens service is
// nil (e.g. encryption-key boot failed and main left it unwired). Main
// always sets it on a successful boot; this is defensive for tests.
func (s *Server) githubTokensUnavailable(w http.ResponseWriter) bool {
	if s.Deps.GithubTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub tokens service is not configured on this server")
		return true
	}
	return false
}

// ListGithubTokens — GET /api/v1/github-tokens.
func (s *Server) ListGithubTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
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
//
// Scopes are NOT taken from the request body — GitHub is the only
// source of truth, so we call GET /user with the PAT, parse
// X-OAuth-Scopes from the response header, and store what GitHub
// actually advertises. The Scopes field on the request stays for
// backwards compatibility with older clients but is ignored.
func (s *Server) CreateGithubToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.githubTokensUnavailable(w) {
		return
	}
	var body openapi.CreateGithubTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.Token == "" {
		writeError(w, http.StatusUnprocessableEntity, "token value is required")
		return
	}

	info, verr := s.githubAPI().ValidateToken(r.Context(), body.Token)
	if verr != nil {
		if errors.Is(verr, githubapi.ErrUnauthorized) {
			writeError(w, http.StatusUnprocessableEntity,
				"GitHub rejected the token: "+verr.Error())
			return
		}
		writeError(w, http.StatusBadGateway,
			"could not validate token with GitHub: "+verr.Error())
		return
	}

	tok, err := s.Deps.GithubTokens.Create(r.Context(), body.Name, body.Token, info.Scopes)
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

// UpdateGithubToken — PUT /api/v1/github-tokens/{id}.
//
// Rotates the secret value of an existing token in place. The plaintext is
// re-validated against GitHub (GET /user) exactly like CreateGithubToken and
// the stored scopes are refreshed from X-OAuth-Scopes; the id and name are
// unchanged so every external project bound to this token keeps working with
// no re-binding. The new plaintext, like on create, is dropped after
// encryption — only metadata comes back.
func (s *Server) UpdateGithubToken(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.githubTokensUnavailable(w) {
		return
	}
	var body openapi.UpdateGithubTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.Token == "" {
		writeError(w, http.StatusUnprocessableEntity, "token value is required")
		return
	}

	info, verr := s.githubAPI().ValidateToken(r.Context(), body.Token)
	if verr != nil {
		if errors.Is(verr, githubapi.ErrUnauthorized) {
			writeError(w, http.StatusUnprocessableEntity,
				"GitHub rejected the token: "+verr.Error())
			return
		}
		writeError(w, http.StatusBadGateway,
			"could not validate token with GitHub: "+verr.Error())
		return
	}

	tok, err := s.Deps.GithubTokens.Update(r.Context(), id, body.Token, info.Scopes)
	if err != nil {
		switch {
		case errors.Is(err, githubtokens.ErrNotFound):
			writeError(w, http.StatusNotFound, "github token not found")
		case errors.Is(err, githubtokens.ErrEmpty):
			writeError(w, http.StatusUnprocessableEntity, "token value is required")
		default:
			writeError(w, http.StatusInternalServerError, "could not update github token")
		}
		return
	}
	writeJSON(w, http.StatusOK, githubTokenToPayload(tok))
}

// ListTokenAccounts — GET /api/v1/github-tokens/{id}/accounts.
//
// Returns the PAT owner plus every org the PAT can see (/user/orgs).
// The dashboard uses this as the first step of the add-repo flow so
// the operator can drill into a specific account before picking a
// repository — useful when /user/repos misses SAML-protected org
// repos that only surface under /orgs/{login}/repos.
func (s *Server) ListTokenAccounts(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.githubTokensUnavailable(w) {
		return
	}

	pat, err := s.Deps.GithubTokens.Reveal(r.Context(), id)
	if err != nil {
		if errors.Is(err, githubtokens.ErrNotFound) {
			writeError(w, http.StatusNotFound, "github token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load github token")
		return
	}

	accounts, lerr := s.githubAPI().ListAccounts(r.Context(), pat)
	if lerr != nil {
		if errors.Is(lerr, githubapi.ErrUnauthorized) {
			writeError(w, http.StatusUnprocessableEntity,
				"GitHub rejected the token: "+lerr.Error())
			return
		}
		writeError(w, http.StatusBadGateway,
			"could not list accounts via GitHub: "+lerr.Error())
		return
	}
	_ = s.Deps.GithubTokens.Touch(r.Context(), id)

	out := make([]openapi.GithubAccount, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, openapi.GithubAccount{
			Login:     a.Login,
			Type:      openapi.GithubAccountType(a.Type),
			AvatarUrl: ptrStr(a.AvatarURL),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": out,
		"total":    len(out),
	})
}

// ListTokenRepos — GET /api/v1/github-tokens/{id}/repos.
//
// Reveals the PAT server-side and returns the repos visible to it.
// When `account` is set, the server scopes the listing to that
// account's /users/{login}/repos or /orgs/{login}/repos endpoint;
// when not, it falls back to /user/repos (affiliations-aggregated).
//
// Up to 500 repos (5 pages × 100) so a worst-case org-member with
// lots of affiliations doesn't have to deal with infinite scroll. The
// optional ?q= substring filter is applied server-side so the
// dashboard fetch stays a single round-trip.
func (s *Server) ListTokenRepos(
	w http.ResponseWriter,
	r *http.Request,
	id string,
	params openapi.ListTokenReposParams,
) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.githubTokensUnavailable(w) {
		return
	}

	pat, err := s.Deps.GithubTokens.Reveal(r.Context(), id)
	if err != nil {
		if errors.Is(err, githubtokens.ErrNotFound) {
			writeError(w, http.StatusNotFound, "github token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load github token")
		return
	}

	const maxPages = 5

	var (
		repos []githubapi.Repo
		lerr  error
	)
	if params.Account != nil && *params.Account != "" {
		if params.AccountType == nil {
			writeError(w, http.StatusUnprocessableEntity,
				"account_type is required when account is set")
			return
		}
		accountType := githubapi.AccountType(*params.AccountType)
		repos, lerr = s.githubAPI().ListReposForAccount(
			r.Context(), pat, accountType, *params.Account, maxPages,
		)
	} else {
		repos, lerr = s.githubAPI().ListUserRepos(r.Context(), pat, maxPages)
	}
	if lerr != nil {
		if errors.Is(lerr, githubapi.ErrUnauthorized) {
			writeError(w, http.StatusUnprocessableEntity,
				"GitHub rejected the token: "+lerr.Error())
			return
		}
		if errors.Is(lerr, githubapi.ErrNotFound) {
			writeError(w, http.StatusNotFound,
				"account not found on GitHub: "+lerr.Error())
			return
		}
		writeError(w, http.StatusBadGateway,
			"could not list repos via GitHub: "+lerr.Error())
		return
	}
	_ = s.Deps.GithubTokens.Touch(r.Context(), id)

	// Optional client-supplied filter — applied here so the dashboard
	// fetch is a single round-trip even for larger result sets.
	if params.Q != nil && *params.Q != "" {
		needle := strings.ToLower(*params.Q)
		filtered := repos[:0]
		for _, rp := range repos {
			if strings.Contains(strings.ToLower(rp.FullName), needle) {
				filtered = append(filtered, rp)
			}
		}
		repos = filtered
	}

	out := make([]openapi.GithubRepo, 0, len(repos))
	for _, rp := range repos {
		desc := rp.Description
		out = append(out, openapi.GithubRepo{
			FullName:      rp.FullName,
			DefaultBranch: rp.DefaultBranch,
			Private:       rp.Private,
			HtmlUrl:       rp.HTMLURL,
			Description:   ptrStr(desc),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repos": out,
		"total": len(out),
	})
}

// ptrStr returns nil for the empty string and &s otherwise, matching
// the OpenAPI nullable + omitempty convention without leaking "" into
// the wire format.
func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DeleteGithubToken — DELETE /api/v1/github-tokens/{id}.
func (s *Server) DeleteGithubToken(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
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
