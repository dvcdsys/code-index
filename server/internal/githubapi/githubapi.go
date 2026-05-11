// Package githubapi is a tiny raw-HTTP client for the handful of GitHub
// REST calls the workspaces feature needs. We deliberately do NOT pull
// in google/go-github (which is ~10MB of generated code) for just the
// two operations we use — registering and deleting a repository webhook.
//
// Authentication: callers pass a Personal Access Token (PAT). The token
// is sent as `Authorization: token <PAT>` which matches what GitHub
// documents for both fine-grained tokens and classic PATs.
//
// Errors are surfaced verbatim from GitHub when the response carries a
// JSON `message` field. Callers usually display these in the dashboard
// so the operator can fix scope / permission issues without trawling
// server logs.
package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnauthorized is returned for 401/403 responses — usually the PAT
// is missing the admin:repo_hook scope. Handlers translate this into
// "you said auto-register but the token can't manage hooks; switch to
// manual or rotate the PAT".
var ErrUnauthorized = errors.New("github API rejected the token")

// TokenInfo carries the metadata we learn about a PAT by calling
// GET /user. The truth about scopes lives on GitHub, not in user input,
// so we always read X-OAuth-Scopes from the response.
//
// Fine-grained PATs (github_pat_*) do not advertise scopes via this
// header — for them Scopes is empty and FineGrained is true.
type TokenInfo struct {
	Login       string
	Scopes      []string
	FineGrained bool
}

// ErrNotFound is the 404 sentinel (e.g. repo missing or token can't see
// it).
var ErrNotFound = errors.New("github API: not found")

// Client is the per-call wrapper. Bare struct so handlers can construct
// per-request without coordinating lifecycle.
type Client struct {
	HTTPClient *http.Client
	// BaseURL defaults to https://api.github.com. Overridable for
	// GitHub Enterprise — and the test suite, which substitutes an
	// httptest server.
	BaseURL string
}

// New returns a Client with sane defaults: 30s timeout, the canonical
// api.github.com base.
func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		BaseURL:    "https://api.github.com",
	}
}

// Repo is the slice of GET /user/repos we care about for the dashboard
// add-repo flow. We deliberately keep this small — only the fields the
// repo-picker UI actually renders — so we don't bloat the JSON payload
// (a single user can have several hundred repos visible via a PAT).
type Repo struct {
	FullName      string `json:"full_name"`       // "owner/name"
	DefaultBranch string `json:"default_branch"`  // used to auto-fill the branch input
	Private       bool   `json:"private"`         // shown as a lock icon in the dropdown
	HTMLURL       string `json:"html_url"`        // canonical https://github.com/... form
	Description   string `json:"description,omitempty"`
}

// AccountType discriminates between a personal account (the PAT
// owner) and a GitHub organization. The dashboard uses this to pick
// the right repo-list endpoint when the user drills into an account.
type AccountType string

const (
	AccountTypeUser AccountType = "user"
	AccountTypeOrg  AccountType = "org"
)

// Account is the rendered shape of an entry in the account selector
// — either the PAT owner's personal account or one of the orgs they
// belong to. Reflects what GitHub returns from /user + /user/orgs.
type Account struct {
	Login     string      `json:"login"`
	Type      AccountType `json:"type"`
	AvatarURL string      `json:"avatar_url,omitempty"`
}

// ListAccounts returns the user that owns the PAT plus every org the
// PAT can see via /user/orgs. The two calls are sequential — they
// share a HTTP client but GitHub recommends one at a time to stay
// within rate-limit-friendly behaviour. Errors from /user/orgs are
// not fatal: an outdated PAT scope can return 200 with an empty list,
// or 403 if SSO is required; either way we keep the personal account
// in the response so the operator can still pick a personal repo.
func (c *Client) ListAccounts(ctx context.Context, pat string) ([]Account, error) {
	if pat == "" {
		return nil, fmt.Errorf("PAT required")
	}

	// Personal account — also doubles as a token-validity probe; if
	// /user 401s the whole flow short-circuits.
	user, err := c.fetchUser(ctx, pat)
	if err != nil {
		return nil, err
	}
	out := []Account{{
		Login:     user.Login,
		Type:      AccountTypeUser,
		AvatarURL: user.AvatarURL,
	}}

	// Orgs — paginated like /user/repos.
	pageURL := c.BaseURL + "/user/orgs?per_page=100"
	for pageURL != "" {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if rerr != nil {
			return nil, rerr
		}
		c.signRequest(req, pat)
		resp, derr := c.HTTPClient.Do(req)
		if derr != nil {
			return nil, fmt.Errorf("github API: %w", derr)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			var batch []struct {
				Login     string `json:"login"`
				AvatarURL string `json:"avatar_url"`
			}
			if err := json.Unmarshal(body, &batch); err != nil {
				return nil, fmt.Errorf("parse /user/orgs: %w", err)
			}
			for _, o := range batch {
				out = append(out, Account{
					Login:     o.Login,
					Type:      AccountTypeOrg,
					AvatarURL: o.AvatarURL,
				})
			}
		case http.StatusUnauthorized, http.StatusForbidden:
			// SSO-gated or insufficient-scope PATs can 403 here even
			// when /user succeeded. The personal account is enough to
			// continue, so swallow and return what we have.
			return out, nil
		default:
			return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(body))
		}
		pageURL = parseNextLink(resp.Header.Get("Link"))
	}
	return out, nil
}

// fetchUser is a small private helper for the two callsites (this
// package's own ValidateToken and the new ListAccounts). Returns the
// few fields we care about.
func (c *Client) fetchUser(ctx context.Context, pat string) (struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}, error) {
	type userResp struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	var u userResp
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/user", nil)
	if err != nil {
		return u, err
	}
	c.signRequest(req, pat)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return u, fmt.Errorf("github API: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(body, &u); err != nil {
			return u, fmt.Errorf("parse /user: %w", err)
		}
		return u, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return u, fmt.Errorf("%w: %s", ErrUnauthorized, githubMessage(body))
	default:
		return u, fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(body))
	}
}

// ListReposForAccount returns repos owned by a specific account. Use
// AccountTypeUser to hit /users/{login}/repos (which lists that user's
// public repos plus, when the caller IS that user, all repos they own
// regardless of visibility) and AccountTypeOrg to hit /orgs/{login}/repos
// (which respects the PAT's organization membership / SAML state).
//
// For the "all my repos" case, callers should fall back to
// ListUserRepos — /user/repos returns the affiliations-aggregated view
// in a single call, which is what we want when no account filter is set.
func (c *Client) ListReposForAccount(ctx context.Context, pat string, accountType AccountType, login string, maxPages int) ([]Repo, error) {
	if pat == "" {
		return nil, fmt.Errorf("PAT required")
	}
	if login == "" {
		return nil, fmt.Errorf("login required")
	}
	var base string
	switch accountType {
	case AccountTypeUser:
		base = c.BaseURL + "/users/" + url.PathEscape(login) + "/repos?per_page=100&sort=pushed&type=all"
	case AccountTypeOrg:
		base = c.BaseURL + "/orgs/" + url.PathEscape(login) + "/repos?per_page=100&sort=pushed&type=all"
	default:
		return nil, fmt.Errorf("unknown account type %q", accountType)
	}
	return c.fetchRepoPages(ctx, pat, base, maxPages)
}

// fetchRepoPages is the shared paginator for any /repos-shaped GitHub
// endpoint. Walks Link rel=next up to maxPages; 0 means no cap.
func (c *Client) fetchRepoPages(ctx context.Context, pat, firstURL string, maxPages int) ([]Repo, error) {
	out := []Repo{}
	page := 0
	pageURL := firstURL
	for pageURL != "" {
		page++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		c.signRequest(req, pat)
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github API: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			var batch []Repo
			if err := json.Unmarshal(body, &batch); err != nil {
				return nil, fmt.Errorf("parse repos page: %w", err)
			}
			out = append(out, batch...)
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, githubMessage(body))
		case http.StatusNotFound:
			return nil, fmt.Errorf("%w: %s", ErrNotFound, githubMessage(body))
		default:
			return nil, fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(body))
		}
		if maxPages > 0 && page >= maxPages {
			break
		}
		pageURL = parseNextLink(resp.Header.Get("Link"))
	}
	return out, nil
}

// ListUserRepos walks /user/repos pages, returning every repo the PAT
// can see as owner / collaborator / org member. The endpoint is
// inherently paginated (per_page=100 is the GitHub max) — we follow
// the Link rel=next header up to maxPages so an outlier user with a
// thousand affiliated repos still completes in bounded time.
//
// maxPages of 0 is interpreted as "no cap" (used in tests); production
// callers should pass a sensible ceiling (typical: 5 = up to 500 repos).
//
// Useful when the operator has not chosen a specific account in the
// dashboard yet — /user/repos is GitHub's affiliations-aggregated view
// and surfaces SAML-protected and collaborator repos that don't appear
// under /orgs/{login}/repos.
func (c *Client) ListUserRepos(ctx context.Context, pat string, maxPages int) ([]Repo, error) {
	if pat == "" {
		return nil, fmt.Errorf("PAT required")
	}
	first := c.BaseURL + "/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member"
	return c.fetchRepoPages(ctx, pat, first, maxPages)
}

// parseNextLink extracts the URL of rel=next from a GitHub Link header.
// Format per RFC 5988: `<https://...?page=2>; rel="next", <...>; rel="last"`.
// Empty string when no next page exists — that's the terminator for the
// pagination loop in ListUserRepos.
func parseNextLink(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		isNext := false
		for _, p := range segs[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				isNext = true
				break
			}
		}
		if !isNext {
			continue
		}
		u := strings.TrimSpace(segs[0])
		u = strings.TrimPrefix(u, "<")
		u = strings.TrimSuffix(u, ">")
		return u
	}
	return ""
}

// CreateWebhookOptions parameterises a hook registration. Events defaults
// to ["push"] when nil.
type CreateWebhookOptions struct {
	Owner    string
	Repo     string
	PAT      string
	URL      string // the cix-server delivery URL
	Secret   string // HMAC secret cix-server expects
	Events   []string
	Insecure bool // mostly for tests against http:// origins
}

// HookResponse is the slice of the GitHub response we care about.
type HookResponse struct {
	ID     int64  `json:"id"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// CreateWebhook calls POST /repos/{owner}/{repo}/hooks. Returns the
// hook id so callers can persist it for later DeleteWebhook.
func (c *Client) CreateWebhook(ctx context.Context, opts CreateWebhookOptions) (HookResponse, error) {
	if opts.Owner == "" || opts.Repo == "" {
		return HookResponse{}, fmt.Errorf("owner/repo required")
	}
	if opts.PAT == "" {
		return HookResponse{}, fmt.Errorf("PAT required")
	}
	events := opts.Events
	if len(events) == 0 {
		events = []string{"push"}
	}
	body := map[string]any{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]any{
			"url":          opts.URL,
			"content_type": "json",
			"secret":       opts.Secret,
			"insecure_ssl": insecureSSLValue(opts.Insecure),
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return HookResponse{}, err
	}
	endpoint := c.BaseURL + "/repos/" + url.PathEscape(opts.Owner) + "/" + url.PathEscape(opts.Repo) + "/hooks"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return HookResponse{}, err
	}
	c.signRequest(req, opts.PAT)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return HookResponse{}, fmt.Errorf("github API: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var hr HookResponse
		if err := json.Unmarshal(respBody, &hr); err != nil {
			return HookResponse{}, fmt.Errorf("parse hook response: %w", err)
		}
		return hr, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return HookResponse{}, fmt.Errorf("%w: %s", ErrUnauthorized, githubMessage(respBody))
	case http.StatusNotFound:
		return HookResponse{}, fmt.Errorf("%w: %s", ErrNotFound, githubMessage(respBody))
	default:
		return HookResponse{}, fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(respBody))
	}
}

// ValidateToken probes GET /user with the given PAT, returning the
// authenticated login plus the scopes GitHub advertises in the
// X-OAuth-Scopes response header. A 401/403 yields ErrUnauthorized so
// the caller can reject token creation with a precise message.
//
// We treat X-OAuth-Scopes as the only authoritative source of scope
// information: it is what GitHub will actually enforce, so storing
// anything else (e.g. user-typed strings) just invites drift.
func (c *Client) ValidateToken(ctx context.Context, pat string) (TokenInfo, error) {
	if pat == "" {
		return TokenInfo{}, fmt.Errorf("PAT required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/user", nil)
	if err != nil {
		return TokenInfo{}, err
	}
	c.signRequest(req, pat)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return TokenInfo{}, fmt.Errorf("github API: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var u struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(respBody, &u); err != nil {
			return TokenInfo{}, fmt.Errorf("parse /user response: %w", err)
		}
		info := TokenInfo{
			Login:       u.Login,
			Scopes:      parseScopeHeader(resp.Header.Get("X-OAuth-Scopes")),
			FineGrained: strings.HasPrefix(pat, "github_pat_"),
		}
		return info, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return TokenInfo{}, fmt.Errorf("%w: %s", ErrUnauthorized, githubMessage(respBody))
	default:
		return TokenInfo{}, fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(respBody))
	}
}

// parseScopeHeader splits the comma-separated X-OAuth-Scopes value
// GitHub returns on classic PATs. An empty header (typical for
// fine-grained PATs or a token with no scopes) yields a nil slice
// rather than [""]; callers that need a stable JSON shape replace nil
// with []string{} at the boundary.
func parseScopeHeader(h string) []string {
	h = strings.TrimSpace(h)
	if h == "" {
		return nil
	}
	parts := strings.Split(h, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DeleteWebhook calls DELETE /repos/{owner}/{repo}/hooks/{id}. Treats
// 404 as success — if the hook is already gone the post-condition is
// satisfied.
func (c *Client) DeleteWebhook(ctx context.Context, owner, repo, pat string, hookID int64) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/hooks/%d", c.BaseURL,
		url.PathEscape(owner), url.PathEscape(repo), hookID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	c.signRequest(req, pat)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github API: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrUnauthorized, githubMessage(respBody))
	default:
		return fmt.Errorf("github API %d: %s", resp.StatusCode, githubMessage(respBody))
	}
}

// --- helpers ---

func (c *Client) signRequest(req *http.Request, pat string) {
	req.Header.Set("Authorization", "token "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cix-server")
}

func insecureSSLValue(insecure bool) string {
	if insecure {
		return "1"
	}
	return "0"
}

func githubMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "no body"
	}
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Message != "" {
		return env.Message
	}
	// Fall back to the raw body, truncated to keep error strings sane.
	const maxLen = 200
	s := strings.TrimSpace(string(body))
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// ParseOwnerRepo extracts {owner, repo} from an https://github.com/owner/repo URL.
// Mirrors the same logic as workspacerepos.parseGitHubURL but kept private
// to that package — we re-implement here to avoid an import cycle.
func ParseOwnerRepo(githubURL string) (owner, repo string, err error) {
	u, perr := url.Parse(strings.TrimSpace(githubURL))
	if perr != nil {
		return "", "", fmt.Errorf("invalid URL: %w", perr)
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return "", "", fmt.Errorf("not a github.com URL")
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected /owner/repo path, got %q", u.Path)
	}
	return parts[0], parts[1], nil
}
