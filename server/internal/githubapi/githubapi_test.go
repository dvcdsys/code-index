package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer returns a Client pointing at an httptest.Server that
// captures requests for later assertion.
type recordedReq struct {
	Path   string
	Method string
	Auth   string
	Body   map[string]any
}

func fakeServer(t *testing.T, handler http.Handler) (*Client, *[]recordedReq) {
	t.Helper()
	var recs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		recs = append(recs, recordedReq{
			Path:   r.URL.Path,
			Method: r.Method,
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		})
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c, &recs
}

func TestCreateWebhookSendsExpectedRequest(t *testing.T) {
	c, recs := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 42, "url": "https://api.github.com/repos/o/r/hooks/42", "active": true}`))
	}))
	hr, err := c.CreateWebhook(context.Background(), CreateWebhookOptions{
		Owner:  "o",
		Repo:   "r",
		PAT:    "ghp_xxx",
		URL:    "https://cix.test/api/v1/webhooks/github/abc",
		Secret: "s3cr3t",
	})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if hr.ID != 42 {
		t.Fatalf("expected id=42, got %d", hr.ID)
	}
	if len(*recs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*recs))
	}
	r := (*recs)[0]
	if r.Path != "/repos/o/r/hooks" {
		t.Fatalf("path: %q", r.Path)
	}
	if r.Method != http.MethodPost {
		t.Fatalf("method: %q", r.Method)
	}
	if r.Auth != "token ghp_xxx" {
		t.Fatalf("auth: %q", r.Auth)
	}
	if cfg, _ := r.Body["config"].(map[string]any); cfg["secret"] != "s3cr3t" {
		t.Fatalf("secret not forwarded: %+v", r.Body)
	}
}

func TestCreateWebhookUnauthorized(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	}))
	_, err := c.CreateWebhook(context.Background(), CreateWebhookOptions{
		Owner: "o", Repo: "r", PAT: "x", URL: "https://x", Secret: "y",
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestCreateWebhookForbiddenIsUnauthorized(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "Resource not accessible by personal access token"}`))
	}))
	_, err := c.CreateWebhook(context.Background(), CreateWebhookOptions{
		Owner: "o", Repo: "r", PAT: "x", URL: "https://x", Secret: "y",
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("error should surface github message, got %v", err)
	}
}

func TestDeleteWebhookTreats404AsSuccess(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if err := c.DeleteWebhook(context.Background(), "o", "r", "x", 42); err != nil {
		t.Fatalf("404 should be success on DELETE, got %v", err)
	}
}

func TestValidateTokenReturnsScopesFromHeader(t *testing.T) {
	c, recs := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, admin:repo_hook, read:org")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login": "alice"}`))
	}))
	info, err := c.ValidateToken(context.Background(), "ghp_xxx")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if info.Login != "alice" {
		t.Fatalf("login: %q", info.Login)
	}
	want := []string{"repo", "admin:repo_hook", "read:org"}
	if len(info.Scopes) != len(want) {
		t.Fatalf("scopes: got %v, want %v", info.Scopes, want)
	}
	for i, s := range want {
		if info.Scopes[i] != s {
			t.Fatalf("scope[%d]=%q want %q", i, info.Scopes[i], s)
		}
	}
	if info.FineGrained {
		t.Fatalf("ghp_ prefix should not be fine-grained")
	}
	if len(*recs) != 1 || (*recs)[0].Path != "/user" {
		t.Fatalf("expected GET /user, got %+v", *recs)
	}
}

func TestValidateTokenFineGrainedHasEmptyScopes(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fine-grained PATs: GitHub omits X-OAuth-Scopes entirely.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login": "alice"}`))
	}))
	info, err := c.ValidateToken(context.Background(), "github_pat_yyy")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if !info.FineGrained {
		t.Fatalf("github_pat_ prefix should be fine-grained")
	}
	if len(info.Scopes) != 0 {
		t.Fatalf("expected empty scopes for fine-grained, got %v", info.Scopes)
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	}))
	_, err := c.ValidateToken(context.Background(), "bad")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("error should surface github message, got %v", err)
	}
}

func TestValidateTokenEmptyHeaderYieldsNilScopes(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "")
		_, _ = w.Write([]byte(`{"login": "alice"}`))
	}))
	info, err := c.ValidateToken(context.Background(), "ghp_x")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if len(info.Scopes) != 0 {
		t.Fatalf("empty header should yield empty scopes, got %v", info.Scopes)
	}
}

func TestListUserReposFollowsLinkHeader(t *testing.T) {
	// Two-page response: first page sends Link rel=next pointing at
	// page 2, which has no further Link header → terminator.
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", `<`+baseURL+`/user/repos?page=2>; rel="next", <`+baseURL+`/user/repos?page=2>; rel="last"`)
			_, _ = w.Write([]byte(`[{"full_name":"o/r1","default_branch":"main","private":false,"html_url":"https://github.com/o/r1"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"full_name":"o/r2","default_branch":"develop","private":true,"html_url":"https://github.com/o/r2"}]`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	baseURL = srv.URL
	c := New()
	c.BaseURL = srv.URL

	repos, err := c.ListUserRepos(context.Background(), "ghp_x", 5)
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos across pages, got %d", len(repos))
	}
	if repos[0].FullName != "o/r1" || repos[1].FullName != "o/r2" {
		t.Fatalf("unexpected repos: %+v", repos)
	}
	if !repos[1].Private {
		t.Fatalf("private flag should round-trip, got %+v", repos[1])
	}
}

func TestListUserReposHonoursMaxPages(t *testing.T) {
	// Server claims an infinite next-page chain; ListUserRepos must
	// stop after maxPages so we don't run forever on a misbehaving
	// upstream.
	var baseURL string
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page++
		w.Header().Set("Link", `<`+baseURL+`/user/repos?page=999>; rel="next"`)
		_, _ = w.Write([]byte(`[{"full_name":"o/r","default_branch":"main"}]`))
	}))
	t.Cleanup(srv.Close)
	baseURL = srv.URL
	c := New()
	c.BaseURL = srv.URL

	_, err := c.ListUserRepos(context.Background(), "ghp_x", 3)
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if page != 3 {
		t.Fatalf("expected exactly 3 page hits with maxPages=3, got %d", page)
	}
}

func TestListUserReposUnauthorized(t *testing.T) {
	c, _ := fakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	}))
	_, err := c.ListUserRepos(context.Background(), "bad", 1)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestParseNextLink(t *testing.T) {
	in := `<https://api.github.com/user/repos?page=2>; rel="next", <https://api.github.com/user/repos?page=10>; rel="last"`
	want := "https://api.github.com/user/repos?page=2"
	if got := parseNextLink(in); got != want {
		t.Fatalf("parseNextLink(%q) = %q, want %q", in, got, want)
	}
	if got := parseNextLink(""); got != "" {
		t.Fatalf("empty header should yield empty, got %q", got)
	}
	// rel=last only — there's no next, must terminate.
	if got := parseNextLink(`<https://x>; rel="last"`); got != "" {
		t.Fatalf("rel=last only should not advance, got %q", got)
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/spf13/cobra":      {"spf13", "cobra"},
		"https://github.com/spf13/cobra.git":  {"spf13", "cobra"},
		"https://github.com/spf13/cobra/":     {"spf13", "cobra"},
		"https://github.com/spf13/cobra.git/": {"spf13", "cobra"},
	}
	for in, want := range cases {
		o, r, err := ParseOwnerRepo(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if o != want[0] || r != want[1] {
			t.Errorf("%q → (%q,%q), want %v", in, o, r, want)
		}
	}
	bad := []string{
		"https://gitlab.com/x/y",
		"https://github.com/onlyowner",
		"not a url at all",
	}
	for _, b := range bad {
		if _, _, err := ParseOwnerRepo(b); err == nil {
			t.Errorf("%q: expected error", b)
		}
	}
}
