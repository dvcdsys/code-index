// Package githubapi is a tiny raw-HTTP client for the handful of GitHub
// REST calls the server-side GitHub-repo lifecycle needs. We
// deliberately do NOT pull in google/go-github (which is ~10MB of
// generated code) for just the two operations we use — registering
// and deleting a repository webhook.
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
	FullName      string    `json:"full_name"`      // "owner/name"
	DefaultBranch string    `json:"default_branch"` // used to auto-fill the branch input
	Private       bool      `json:"private"`        // shown as a lock icon in the dropdown
	HTMLURL       string    `json:"html_url"`       // canonical https://github.com/... form
	Description   string    `json:"description,omitempty"`
	Owner         RepoOwner `json:"owner"`
}

// RepoOwner mirrors the `owner` slice of GitHub's repo payload. GitHub
// uses the strings "User" / "Organization" (capitalized) so we
// preserve them verbatim and translate at the application boundary.
type RepoOwner struct {
	Login     string `json:"login"`
	Type      string `json:"type"` // "User" | "Organization"
	AvatarURL string `json:"avatar_url"`
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

// ListAccounts returns the user that owns the PAT plus every distinct
// org owner found in /user/repos.
//
// Why not /user/orgs or /user/memberships/orgs? Both require explicit
// scopes (classic: `read:org`; fine-grained: "Members → Read"). PATs
// that lack those still see and clone repos through /user/repos — so
// scoping the dashboard's account picker to "everything the PAT
// already shows me" is both more permissive and more honest. The
// listed accounts always correspond to repos the token can actually
// access. The trade-off is that orgs with zero visible repos won't
// surface; that's a feature, not a bug — there'd be nothing for the
// operator to pick anyway.
//
// /user is still hit first as a token-validity probe and to pick up
// the operator's own login + avatar even when /user/repos is empty.
func (c *Client) ListAccounts(ctx context.Context, pat string) ([]Account, error) {
	if pat == "" {
		return nil, fmt.Errorf("PAT required")
	}

	user, err := c.fetchUser(ctx, pat)
	if err != nil {
		return nil, err
	}

	const maxPages = 5
	repos, rerr := c.ListUserRepos(ctx, pat, maxPages)
	if rerr != nil {
		// Don't fail the whole flow on a /user/repos hiccup — return
		// at least the personal account so the dialog renders.
		return []Account{{
			Login:     user.Login,
			Type:      AccountTypeUser,
			AvatarURL: user.AvatarURL,
		}}, nil
	}

	// The user's own account always comes first so the dashboard can
	// default-select it. Then every distinct owner — Users vs Orgs
	// distinguished by the owner.type field from /user/repos. Login
	// comparison is case-insensitive (GitHub treats `Foo` and `foo`
	// as the same account).
	out := []Account{{
		Login:     user.Login,
		Type:      AccountTypeUser,
		AvatarURL: user.AvatarURL,
	}}
	seen := map[string]struct{}{
		strings.ToLower(user.Login): {},
	}
	for _, rp := range repos {
		key := strings.ToLower(rp.Owner.Login)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Account{
			Login:     rp.Owner.Login,
			Type:      ownerTypeToAccountType(rp.Owner.Type),
			AvatarURL: rp.Owner.AvatarURL,
		})
	}
	return out, nil
}

// ownerTypeToAccountType normalises GitHub's "User"/"Organization"
// strings to our user/org enum. Unknown values default to user, which
// is the conservative choice — repos will be fetched from
// /users/{login}/repos and just return what's accessible.
func ownerTypeToAccountType(s string) AccountType {
	switch strings.ToLower(s) {
	case "organization":
		return AccountTypeOrg
	default:
		return AccountTypeUser
	}
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

// HookConfig mirrors the `config` slice of a GitHub hook. We only read
// `url` — the delivery target — which is what makes registration
// idempotent: a hook whose config.url already equals our delivery URL is
// the cix hook, so we reuse it instead of POSTing a duplicate.
type HookConfig struct {
	URL string `json:"url"`
}

// HookResponse is the slice of the GitHub response we care about. The
// top-level URL is the hook's own API URL (…/hooks/{id}); Config.URL is the
// delivery target the hook POSTs to.
type HookResponse struct {
	ID     int64      `json:"id"`
	URL    string     `json:"url"`
	Active bool       `json:"active"`
	Config HookConfig `json:"config"`
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

// ListWebhooks calls GET /repos/{owner}/{repo}/hooks and returns the repo's
// configured hooks (with their delivery config.url). Used to make
// registration idempotent — callers match on config.url to find an existing
// cix hook before creating a new one. Walks Link rel=next up to maxPages
// (0 = no cap); a repo with >100 hooks is pathological, so the default
// caller passes a small ceiling.
func (c *Client) ListWebhooks(ctx context.Context, owner, repo, pat string, maxPages int) ([]HookResponse, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo required")
	}
	if pat == "" {
		return nil, fmt.Errorf("PAT required")
	}
	out := []HookResponse{}
	page := 0
	pageURL := c.BaseURL + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/hooks?per_page=100"
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
			var batch []HookResponse
			if err := json.Unmarshal(body, &batch); err != nil {
				return nil, fmt.Errorf("parse hooks page: %w", err)
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

// EnsureWebhook is the idempotent registration entry point. It lists the
// repo's existing hooks and, if one already targets opts.URL, PATCHes it in
// place (refreshing the secret/events/active flag) and returns its id;
// otherwise it POSTs a fresh hook. Any *additional* hooks pointing at the
// same delivery URL are deleted, so a repo that already accumulated
// duplicates (issue #68) converges to exactly one cix hook.
//
// The returned bool reports whether a new hook was created (false = an
// existing one was reused). Callers persist the returned id either way.
func (c *Client) EnsureWebhook(ctx context.Context, opts CreateWebhookOptions) (HookResponse, bool, error) {
	hooks, err := c.ListWebhooks(ctx, opts.Owner, opts.Repo, opts.PAT, 10)
	if err != nil {
		return HookResponse{}, false, err
	}
	var matches []HookResponse
	for _, h := range hooks {
		if sameDeliveryURL(h.Config.URL, opts.URL) {
			matches = append(matches, h)
		}
	}
	if len(matches) == 0 {
		hr, cerr := c.CreateWebhook(ctx, opts)
		if cerr != nil {
			return HookResponse{}, false, cerr
		}
		return hr, true, nil
	}

	// Reuse the first match; PATCH to refresh delivery config. If GitHub
	// reports it gone (raced delete between list and patch), create instead.
	keep := matches[0]
	hr, uerr := c.UpdateWebhook(ctx, UpdateWebhookOptions{
		Owner:    opts.Owner,
		Repo:     opts.Repo,
		PAT:      opts.PAT,
		HookID:   keep.ID,
		URL:      opts.URL,
		Secret:   opts.Secret,
		Events:   opts.Events,
		Insecure: opts.Insecure,
	})
	if uerr != nil {
		if errors.Is(uerr, ErrNotFound) {
			hr2, cerr := c.CreateWebhook(ctx, opts)
			if cerr != nil {
				return HookResponse{}, false, cerr
			}
			return hr2, true, nil
		}
		return HookResponse{}, false, uerr
	}

	// Prune any leftover duplicates so the repo ends up with one cix hook.
	for _, dup := range matches[1:] {
		_ = c.DeleteWebhook(ctx, opts.Owner, opts.Repo, opts.PAT, dup.ID)
	}
	return hr, false, nil
}

// sameDeliveryURL compares two hook delivery URLs for idempotency. Exact
// match after trimming a trailing slash — cix builds delivery URLs
// deterministically, so anything subtler would risk treating a genuinely
// different endpoint as the same hook.
func sameDeliveryURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/") && a != ""
}

// UpdateWebhookOptions parameterises a hook update. HookID identifies the
// existing hook; URL/Secret are the new delivery config. Events defaults
// to ["push"] when nil.
type UpdateWebhookOptions struct {
	Owner    string
	Repo     string
	PAT      string
	HookID   int64
	URL      string // the new cix-server delivery URL
	Secret   string // HMAC secret cix-server expects
	Events   []string
	Insecure bool
}

// UpdateWebhook calls PATCH /repos/{owner}/{repo}/hooks/{id}. It re-points
// an existing hook at a new delivery URL (and refreshes the secret) — the
// path taken when the managed tunnel's public URL changes. Returns the
// hook id so callers keep the same persisted value.
func (c *Client) UpdateWebhook(ctx context.Context, opts UpdateWebhookOptions) (HookResponse, error) {
	if opts.Owner == "" || opts.Repo == "" {
		return HookResponse{}, fmt.Errorf("owner/repo required")
	}
	if opts.PAT == "" {
		return HookResponse{}, fmt.Errorf("PAT required")
	}
	if opts.HookID == 0 {
		return HookResponse{}, fmt.Errorf("hook id required")
	}
	events := opts.Events
	if len(events) == 0 {
		events = []string{"push"}
	}
	body := map[string]any{
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
	endpoint := fmt.Sprintf("%s/repos/%s/%s/hooks/%d", c.BaseURL,
		url.PathEscape(opts.Owner), url.PathEscape(opts.Repo), opts.HookID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(raw))
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
	case http.StatusOK:
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
// Mirrors the same logic as gitrepos.parseGitHubURL but kept private
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
