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
