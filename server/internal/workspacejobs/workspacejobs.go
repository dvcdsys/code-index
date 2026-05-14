// Package workspacejobs wires the workspaces feature's job handlers
// into the generic internal/jobs queue. It owns nothing — just
// composes gitrepos, githubtokens, repocloner, and repoindexer behind
// a thin Register function called from main.
//
// Lifecycle for an external project:
//
//	1. POST /api/v1/git-repos
//	   - inserts a projects row (status='pending') and a git_repos row
//	   - enqueues clone_repo job (dedupe_key="clone:<path_hash>")
//
//	2. clone_repo handler
//	   - reveals PAT via githubtokens.Reveal (if token_id set)
//	   - calls repocloner.CloneOrFetch into DataDir/repos/<path_hash>/
//	   - flips projects.status → indexing
//	   - enqueues index_repo job (dedupe_key="index:<path_hash>")
//
//	3. index_repo handler
//	   - calls repoindexer.IndexDir with the project_path
//	   - flips projects.status → indexed (or 'error' on failure)
//	   - writes last_indexed_at on the projects row
//
// Workspace-level search is served from per-project chromem
// collections via a weighted fan-out (internal/httpapi/workspacesearch.go).
package workspacejobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
	"github.com/dvcdsys/code-index/server/internal/repoindexer"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// Job type constants.
const (
	TypeCloneRepo = "clone_repo"
	TypeIndexRepo = "index_repo"
)

// ClonePayload is the JSON shape stored on a clone_repo job. The
// project_path doubles as the lookup key for the matching git_repos
// row; path_hash is derived via db.HashHostPath when needed (e.g. as
// the on-disk clone directory name).
type ClonePayload struct {
	ProjectPath string `json:"project_path"`
}

// IndexPayload mirrors ClonePayload.
type IndexPayload struct {
	ProjectPath string `json:"project_path"`
}

// Deps bundles everything the handlers need.
type Deps struct {
	DB           *sql.DB
	Jobs         *jobs.Service
	GitRepos     *gitrepos.Service
	GithubTokens *githubtokens.Service
	Indexer      *indexer.Service
	VectorStore  *vectorstore.Store
	DataDir      string // root for cloned repos: <DataDir>/repos/<path_hash>/
	Logger       *slog.Logger
}

// Register hooks the workspaces job handlers into a jobs.Service.
func Register(d Deps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	d.Jobs.Register(TypeCloneRepo, func(ctx context.Context, job jobs.Job) error {
		return handleClone(ctx, d, job)
	})
	d.Jobs.Register(TypeIndexRepo, func(ctx context.Context, job jobs.Job) error {
		return handleIndex(ctx, d, job)
	})
}

// EnqueueClone inserts a clone_repo job. The index_repo job is chained
// on successful clone — callers don't enqueue it directly.
func EnqueueClone(ctx context.Context, j *jobs.Service, projectPath string) error {
	_, err := j.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeCloneRepo,
		DedupeKey: "clone:" + projects.HashPath(projectPath),
		Payload:   ClonePayload{ProjectPath: projectPath},
	})
	if errors.Is(err, jobs.ErrDuplicate) {
		// Already queued — soft no-op.
		return nil
	}
	return err
}

func handleClone(ctx context.Context, d Deps, job jobs.Job) error {
	var p ClonePayload
	if err := jobs.UnmarshalPayload(job, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.ProjectPath == "" {
		return errors.New("empty project_path")
	}
	g, err := d.GitRepos.GetByPath(ctx, p.ProjectPath)
	if err != nil {
		return fmt.Errorf("load git_repo for %s: %w", p.ProjectPath, err)
	}
	hash := projects.HashPath(g.ProjectPath)

	if err := setProjectStatus(ctx, d.DB, g.ProjectPath, "cloning"); err != nil {
		return fmt.Errorf("mark cloning: %w", err)
	}

	pat := ""
	if g.TokenID != "" {
		token, terr := d.GithubTokens.Reveal(ctx, g.TokenID)
		if terr != nil {
			d.recordFailure(ctx, g, fmt.Errorf("reveal token: %w", terr))
			return terr
		}
		pat = token
		// Best-effort intent signal: rebind `pat` to a zero-filled string
		// on function exit. Go strings are immutable, so this does NOT
		// wipe the underlying bytes — the original allocation from
		// Reveal/Decrypt may still be reachable from escape-analyzed
		// copies (e.g. inside repocloner's HTTP basic-auth header). The
		// gesture matters for readability + intent (PAT is sensitive,
		// don't hold it longer than needed), not as a security control.
		// Switching PAT to []byte with explicit wipe via crypto/subtle
		// would be overkill for this code path.
		defer func() { pat = strings.Repeat("\x00", len(pat)) }()
		_ = d.GithubTokens.Touch(ctx, g.TokenID)
	}

	result, err := repocloner.CloneOrFetch(ctx, repocloner.CloneOptions{
		GitHubURL: g.GitHubURL,
		Branch:    g.Branch,
		PAT:       pat,
		LocalDir:  repocloner.LocalDirFor(d.DataDir, hash),
	})
	if err != nil {
		d.recordFailure(ctx, g, fmt.Errorf("clone: %w", err))
		return err
	}

	if err := d.GitRepos.SetClone(ctx, g.ProjectPath, result.HeadSHA, ""); err != nil {
		d.Logger.Warn("workspacejobs: set last_sha failed", "project", g.ProjectPath, "err", err)
	}

	if err := setProjectStatus(ctx, d.DB, g.ProjectPath, "indexing"); err != nil {
		d.Logger.Warn("workspacejobs: set status indexing failed", "project", g.ProjectPath, "err", err)
	}

	if _, eerr := d.Jobs.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeIndexRepo,
		DedupeKey: "index:" + hash,
		Payload:   IndexPayload{ProjectPath: g.ProjectPath},
	}); eerr != nil && !errors.Is(eerr, jobs.ErrDuplicate) {
		d.recordFailure(ctx, g, fmt.Errorf("enqueue index: %w", eerr))
		return eerr
	}
	return nil
}

func handleIndex(ctx context.Context, d Deps, job jobs.Job) error {
	var p IndexPayload
	if err := jobs.UnmarshalPayload(job, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.ProjectPath == "" {
		return errors.New("empty project_path")
	}
	g, err := d.GitRepos.GetByPath(ctx, p.ProjectPath)
	if err != nil {
		return fmt.Errorf("load git_repo for %s: %w", p.ProjectPath, err)
	}
	cloneDir := repocloner.LocalDirFor(d.DataDir, projects.HashPath(g.ProjectPath))

	_, _, err = repoindexer.IndexDir(ctx, d.Indexer, g.ProjectPath, cloneDir, repoindexer.DefaultFilter(), d.Logger)
	if err != nil {
		d.recordFailure(ctx, g, fmt.Errorf("index: %w", err))
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE projects SET status = 'indexed', last_indexed_at = ?, updated_at = ? WHERE host_path = ?`,
		now, now, g.ProjectPath,
	); err != nil {
		return fmt.Errorf("mark indexed: %w", err)
	}
	return nil
}

func setProjectStatus(ctx context.Context, db *sql.DB, projectPath, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx,
		`UPDATE projects SET status = ?, updated_at = ? WHERE host_path = ?`,
		status, now, projectPath)
	return err
}

func (d Deps) recordFailure(ctx context.Context, g gitrepos.GitRepo, err error) {
	if err == nil {
		return
	}
	d.Logger.Error("workspacejobs: repo failed", "project", g.ProjectPath, "err", err)
	msg := err.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if uerr := d.GitRepos.SetClone(ctx, g.ProjectPath, "", msg); uerr != nil {
		d.Logger.Error("workspacejobs: could not write last_error", "project", g.ProjectPath, "err", uerr)
	}
	if uerr := setProjectStatus(ctx, d.DB, g.ProjectPath, "error"); uerr != nil {
		d.Logger.Error("workspacejobs: could not write status=error", "project", g.ProjectPath, "err", uerr)
	}
}

// Compile-time guard: payloads encode cleanly.
var _ = func() (any, any) {
	a, _ := json.Marshal(ClonePayload{})
	b, _ := json.Marshal(IndexPayload{})
	return a, b
}
