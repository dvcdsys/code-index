// Package repocloner is the workspaces feature's git boundary. It wraps
// go-git so the rest of the codebase doesn't need to know about plumbing
// objects, references, or storage layers.
//
// Why go-git (not `git` shell-out): the production CUDA image runs on
// distroless/cc-debian13 which has no shell and no git binary. Pulling
// go-git into the binary keeps the runtime image untouched.
//
// What this package does:
//   - Clone a branch (public OR PAT-authenticated)
//   - Fetch + reset to remote HEAD on subsequent runs
//   - Report the current HEAD SHA (for last_sha bookkeeping)
//   - Resolve a "github.com/owner/repo" + branch to a deterministic local
//     directory under DataDir/repos/{path_hash}/
//
// Errors are deliberately coarse — the worker pool surfaces them in the
// job row and the dashboard renders them verbatim. There's no point
// distinguishing "wrong PAT" from "branch missing" deep in the call chain.
package repocloner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ErrAlreadyUpToDate signals a fetch found no new commits. Callers can
// short-circuit reindex on this.
var ErrAlreadyUpToDate = errors.New("repo already up to date")

// CloneOptions parameterises a clone or fetch.
type CloneOptions struct {
	// GitHubURL is the canonical HTTPS URL — "https://github.com/owner/repo"
	// (with or without ".git" suffix; both work).
	GitHubURL string
	Branch    string
	// PAT, when non-empty, is sent as HTTP BasicAuth with username
	// "x-access-token" — works for fine-grained tokens, classic PATs, and
	// GitHub App installation tokens alike.
	PAT string
	// LocalDir is the absolute destination. Created if missing; reused
	// (fetch+reset) if it already contains a git repository for the same
	// remote URL.
	LocalDir string
}

// Result is what handlers care about post-clone.
type Result struct {
	HeadSHA string
}

// CloneOrFetch clones the repo when LocalDir is empty, otherwise fetches
// + resets the local checkout to origin/{branch}. Returns the HEAD SHA
// after the operation completes.
//
// The caller is responsible for choosing a LocalDir that won't collide
// across repos — typically `<DataDir>/repos/<path_hash>/` keyed by
// projects.path_hash (NOT the github URL, which can change with
// rename + redirect).
func CloneOrFetch(ctx context.Context, opts CloneOptions) (Result, error) {
	if strings.TrimSpace(opts.GitHubURL) == "" {
		return Result{}, fmt.Errorf("GitHubURL required")
	}
	if strings.TrimSpace(opts.Branch) == "" {
		return Result{}, fmt.Errorf("Branch required")
	}
	if strings.TrimSpace(opts.LocalDir) == "" {
		return Result{}, fmt.Errorf("LocalDir required")
	}
	url := normaliseURL(opts.GitHubURL)
	auth := authFor(opts.PAT)

	// First-time clone path: LocalDir is missing or empty.
	if needsClone(opts.LocalDir) {
		if err := os.MkdirAll(opts.LocalDir, 0o755); err != nil {
			return Result{}, fmt.Errorf("mkdir clone target: %w", err)
		}
		repo, err := git.PlainCloneContext(ctx, opts.LocalDir, false, &git.CloneOptions{
			URL:           url,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(opts.Branch),
			SingleBranch:  true,
			Depth:         1, // shallow — minimises bandwidth + disk
		})
		if err != nil {
			// Cleanup so the next retry isn't stuck with a half-clone.
			_ = os.RemoveAll(opts.LocalDir)
			return Result{}, fmt.Errorf("clone: %w", err)
		}
		head, err := repo.Head()
		if err != nil {
			return Result{}, fmt.Errorf("resolve HEAD: %w", err)
		}
		return Result{HeadSHA: head.Hash().String()}, nil
	}

	// Reuse path: open the existing repo, ensure the remote matches, fetch,
	// reset to origin/{branch}.
	repo, err := git.PlainOpen(opts.LocalDir)
	if err != nil {
		return Result{}, fmt.Errorf("open existing repo at %s: %w", opts.LocalDir, err)
	}
	if err := ensureRemote(repo, url); err != nil {
		return Result{}, err
	}

	err = repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     auth,
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", opts.Branch, opts.Branch))},
		Depth:    1,
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Result{}, fmt.Errorf("fetch: %w", err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", opts.Branch), true)
	if err != nil {
		return Result{}, fmt.Errorf("resolve remote ref: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return Result{}, fmt.Errorf("worktree: %w", err)
	}
	// Hard reset — discards any local mutation that crept in. Worker-managed
	// checkouts have no human edits we'd want to preserve.
	if err := wt.Reset(&git.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   git.HardReset,
	}); err != nil {
		return Result{}, fmt.Errorf("reset: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("resolve HEAD post-reset: %w", err)
	}
	return Result{HeadSHA: head.Hash().String()}, nil
}

// LocalDirFor returns the canonical path for a workspace_repo's checkout
// under dataDir. Centralised so the worker pool and the cleanup path agree.
// The id segment is treated as opaque (UUID/ULID), no validation here.
func LocalDirFor(dataDir, id string) string {
	return filepath.Join(dataDir, "repos", id)
}

// --- helpers ---

func authFor(pat string) *http.BasicAuth {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return nil
	}
	return &http.BasicAuth{
		// x-access-token is the username GitHub accepts for App / fine-grained
		// token auth. Classic PATs also accept it.
		Username: "x-access-token",
		Password: pat,
	}
}

func normaliseURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, "/")
	if !strings.HasSuffix(u, ".git") {
		u += ".git"
	}
	return u
}

func needsClone(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		// Either dir doesn't exist or has no .git/ — fresh clone path.
		return true
	}
	return false
}

func ensureRemote(repo *git.Repository, wantURL string) error {
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("no origin remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 || urls[0] != wantURL {
		// Repo on disk points at a different URL — likely the workspace
		// admin changed the github_url. Easiest fix: nuke + reclone, but
		// the caller can't see that from here. Surface as an error so the
		// operator at least sees the mismatch in the failed job.
		return fmt.Errorf("local repo remote %v does not match expected %s", urls, wantURL)
	}
	return nil
}
