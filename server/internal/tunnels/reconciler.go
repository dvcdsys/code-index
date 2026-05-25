package tunnels

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
)

// WebhookPathPrefix is the path the public webhook delivery URL is built
// from. Mirrors the route registered for ReceiveGithubWebhook; kept here so
// the reconciler can construct the same URL the add-repo handler does.
const WebhookPathPrefix = "/api/v1/webhooks/github/"

// RepoStore is the gitrepos surface the reconciler needs. Satisfied by
// *gitrepos.Service; narrowed to an interface so tests can fake it.
type RepoStore interface {
	ListAll(ctx context.Context) ([]gitrepos.GitRepo, error)
	SetWebhookID(ctx context.Context, projectPath string, hookID int64) error
}

// TokenRevealer decrypts a stored GitHub PAT. Satisfied by
// *githubtokens.Service.
type TokenRevealer interface {
	Reveal(ctx context.Context, id string) (string, error)
	Touch(ctx context.Context, id string) error
}

// WebhookClient is the GitHub webhook API surface. Satisfied by
// *githubapi.Client.
type WebhookClient interface {
	CreateWebhook(ctx context.Context, opts githubapi.CreateWebhookOptions) (githubapi.HookResponse, error)
	UpdateWebhook(ctx context.Context, opts githubapi.UpdateWebhookOptions) (githubapi.HookResponse, error)
}

// Reconciler re-points every webhook_mode=auto repo at the current public
// base URL. It runs on boot and whenever the tunnel's public URL changes
// (quick tunnels get a fresh URL on every restart), so registered GitHub
// hooks always deliver to a live endpoint.
type Reconciler struct {
	repos  RepoStore
	tokens TokenRevealer
	gh     WebhookClient
	logger *slog.Logger

	// mu serializes Reconcile. On boot two reconciles can race (the
	// OnURLChange callback for a freshly-parsed quick-tunnel URL + the
	// explicit boot reconcile); without serialization both would take the
	// create branch for a repo that has no WebhookID yet and register
	// duplicate hooks on GitHub. Serializing means the second run re-reads
	// the now-persisted WebhookID and PATCHes instead.
	mu sync.Mutex
}

func NewReconciler(repos RepoStore, tokens TokenRevealer, gh WebhookClient, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{repos: repos, tokens: tokens, gh: gh, logger: logger}
}

// RepoOutcome records what happened for a single repo during reconcile.
type RepoOutcome struct {
	ProjectPath string `json:"project_path"`
	Action      string `json:"action"` // "updated" | "created" | "skipped" | "failed"
	Note        string `json:"note,omitempty"`
}

// ReconcileResult is the aggregate outcome surfaced to the dashboard.
type ReconcileResult struct {
	BaseURL  string        `json:"base_url"`
	Total    int           `json:"total"`   // auto repos considered
	Created  int           `json:"created"` // freshly registered
	Updated  int           `json:"updated"` // re-pointed
	Skipped  int           `json:"skipped"`
	Failed   int           `json:"failed"`
	Outcomes []RepoOutcome `json:"outcomes"`
}

// Reconcile ensures every auto repo's GitHub webhook points at baseURL. A
// repo with a known WebhookID is PATCHed (or re-created if GitHub no longer
// has the hook); one without is created. baseURL must be an absolute http(s)
// origin — when empty (no live tunnel) reconcile is a no-op.
func (r *Reconciler) Reconcile(ctx context.Context, baseURL string) (ReconcileResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res := ReconcileResult{BaseURL: baseURL}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(baseURL, "http") {
		r.logger.Info("webhook reconcile skipped: no public base URL")
		return res, nil
	}

	repos, err := r.repos.ListAll(ctx)
	if err != nil {
		return res, err
	}

	for _, g := range repos {
		if g.WebhookMode != gitrepos.WebhookModeAuto {
			continue
		}
		res.Total++
		outcome := r.reconcileOne(ctx, g, baseURL)
		res.Outcomes = append(res.Outcomes, outcome)
		switch outcome.Action {
		case "created":
			res.Created++
		case "updated":
			res.Updated++
		case "skipped":
			res.Skipped++
		case "failed":
			res.Failed++
		}
	}
	r.logger.Info("webhook reconcile complete",
		"base_url", baseURL, "total", res.Total,
		"created", res.Created, "updated", res.Updated,
		"skipped", res.Skipped, "failed", res.Failed)
	return res, nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, g gitrepos.GitRepo, baseURL string) RepoOutcome {
	out := RepoOutcome{ProjectPath: g.ProjectPath}
	if g.TokenID == "" {
		out.Action = "skipped"
		out.Note = "no token_id with admin:repo_hook scope"
		return out
	}
	pat, err := r.tokens.Reveal(ctx, g.TokenID)
	if err != nil {
		out.Action = "skipped"
		out.Note = "could not decrypt token"
		return out
	}
	_ = r.tokens.Touch(ctx, g.TokenID)

	owner, repo, perr := githubapi.ParseOwnerRepo(g.GitHubURL)
	if perr != nil {
		out.Action = "skipped"
		out.Note = "github_url not parseable"
		return out
	}
	delivery := baseURL + WebhookPathPrefix + g.PathHash

	// Existing hook → PATCH it. If GitHub no longer has it (404), fall
	// through to create a fresh one.
	if g.WebhookID != nil && *g.WebhookID != 0 {
		_, uerr := r.gh.UpdateWebhook(ctx, githubapi.UpdateWebhookOptions{
			Owner:  owner,
			Repo:   repo,
			PAT:    pat,
			HookID: *g.WebhookID,
			URL:    delivery,
			Secret: g.WebhookSecret,
		})
		if uerr == nil {
			out.Action = "updated"
			return out
		}
		if !errors.Is(uerr, githubapi.ErrNotFound) {
			out.Action = "failed"
			out.Note = uerr.Error()
			return out
		}
		r.logger.Warn("webhook gone on GitHub side, recreating", "project", g.ProjectPath)
	}

	hr, cerr := r.gh.CreateWebhook(ctx, githubapi.CreateWebhookOptions{
		Owner:  owner,
		Repo:   repo,
		PAT:    pat,
		URL:    delivery,
		Secret: g.WebhookSecret,
	})
	if cerr != nil {
		out.Action = "failed"
		out.Note = cerr.Error()
		return out
	}
	if serr := r.repos.SetWebhookID(ctx, g.ProjectPath, hr.ID); serr != nil {
		r.logger.Warn("could not persist webhook id", "project", g.ProjectPath, "err", serr)
	}
	out.Action = "created"
	return out
}
