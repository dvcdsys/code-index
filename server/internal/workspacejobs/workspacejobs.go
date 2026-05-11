// Package workspacejobs wires the workspaces feature's job handlers into
// the generic internal/jobs queue. It owns nothing — just composes the
// other workspaces packages (workspacerepos, githubtokens, repocloner,
// repoindexer) behind a thin Register function called from main.
//
// Lifecycle for a repo:
//
//	1. POST /api/v1/workspaces/{id}/repos
//	   - inserts a workspace_repos row (status=pending)
//	   - enqueues clone_repo  job  (dedupe_key="clone:<repo_id>")
//
//	2. clone_repo handler
//	   - reveals PAT via githubtokens.Reveal (if token_id set)
//	   - calls repocloner.CloneOrFetch into DataDir/repos/<repo_id>/
//	   - registers projects row (host_path = workspace_repos.project_path)
//	   - flips status → indexing
//	   - enqueues index_repo job (dedupe_key="index:<repo_id>")
//
//	3. index_repo handler
//	   - calls repoindexer.IndexDir with the workspace_repo.project_path
//	   - flips status → indexed (or failed on error)
//
// PR3 will add fetch_repo (incremental) + the webhook receiver that
// enqueues it; PR4+ chains build_call_graph + compute_workspace_communities.
package workspacejobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dvcdsys/code-index/server/internal/callgraph"
	"github.com/dvcdsys/code-index/server/internal/communities"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
	"github.com/dvcdsys/code-index/server/internal/repoindexer"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/workspacerepos"
)

// Job type constants. Kept here so handlers and enqueue-sites share one
// string — typos in job types are a notoriously easy source of "why isn't
// this running?" bugs.
const (
	TypeCloneRepo    = "clone_repo"
	TypeIndexRepo    = "index_repo"
	TypeComputeWorkspaceCommunities = "compute_workspace_communities"
)

// CommunitiesDebounce is the delay applied when chaining a community
// recompute after an index_repo. A burst of repos finishing within the
// window collapses to one rebuild via the dedupe_key — keeps Louvain
// churn off the worker during catch-up after a long downtime.
const CommunitiesDebounce = 30 * time.Second

// ClonePayload is the JSON shape stored on a clone_repo job.
type ClonePayload struct {
	RepoID string `json:"repo_id"`
}

// IndexPayload is the JSON shape stored on an index_repo job.
type IndexPayload struct {
	RepoID string `json:"repo_id"`
}

// CommunitiesPayload is the JSON shape stored on a
// compute_workspace_communities job. WorkspaceID identifies the
// workspace whose Louvain + centroids will be rebuilt.
type CommunitiesPayload struct {
	WorkspaceID string `json:"workspace_id"`
}

// Deps bundles everything the handlers need. Keeping it explicit makes
// wiring obvious in main and means tests can swap any single piece for a
// fake.
type Deps struct {
	DB             *sql.DB
	Jobs           *jobs.Service
	WorkspaceRepos *workspacerepos.Service
	GithubTokens   *githubtokens.Service
	Indexer        *indexer.Service
	VectorStore    *vectorstore.Store
	DataDir        string // root for cloned repos: <DataDir>/repos/<id>/
	Logger         *slog.Logger
}

// Register hooks the workspaces job handlers into a jobs.Service. Call
// once at startup, BEFORE jobs.Start.
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
	d.Jobs.Register(TypeComputeWorkspaceCommunities, func(ctx context.Context, job jobs.Job) error {
		return handleComputeCommunities(ctx, d, job)
	})
}

// EnqueueComputeCommunities schedules a Louvain + centroid rebuild for a
// workspace. Dedupe key + 30s delay collapses the common "many repos
// finished indexing at once" scenario into one rebuild.
func EnqueueComputeCommunities(ctx context.Context, j *jobs.Service, workspaceID string) error {
	_, err := j.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeComputeWorkspaceCommunities,
		DedupeKey: "communities:" + workspaceID,
		Payload:   CommunitiesPayload{WorkspaceID: workspaceID},
		Delay:     CommunitiesDebounce,
	})
	if errors.Is(err, jobs.ErrDuplicate) {
		return nil
	}
	return err
}

func handleComputeCommunities(ctx context.Context, d Deps, job jobs.Job) error {
	var p CommunitiesPayload
	if err := jobs.UnmarshalPayload(job, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.WorkspaceID == "" {
		return errors.New("empty workspace_id")
	}
	res, err := communities.Build(ctx, d.DB, d.VectorStore, p.WorkspaceID, d.Logger)
	if err != nil {
		return fmt.Errorf("communities build: %w", err)
	}
	d.Logger.Info("workspacejobs: communities rebuilt",
		"workspace_id", p.WorkspaceID,
		"nodes", res.Nodes,
		"edges", res.Edges,
		"communities", res.CommunityCount,
		"centroids_stored", res.CentroidsStored,
		"modularity", res.Modularity)
	return nil
}

// EnqueueCloneAndIndex inserts a clone_repo job. The index_repo job is
// chained on successful clone — callers don't enqueue it directly.
func EnqueueClone(ctx context.Context, j *jobs.Service, repoID string) error {
	_, err := j.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeCloneRepo,
		DedupeKey: "clone:" + repoID,
		Payload:   ClonePayload{RepoID: repoID},
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
	if p.RepoID == "" {
		return errors.New("empty repo_id")
	}
	wr, err := d.WorkspaceRepos.GetByID(ctx, p.RepoID)
	if err != nil {
		return fmt.Errorf("load workspace_repo: %w", err)
	}

	if err := d.WorkspaceRepos.SetStatus(ctx, wr.ID, workspacerepos.StatusCloning, "", "", nil); err != nil {
		return fmt.Errorf("mark cloning: %w", err)
	}

	pat := ""
	if wr.TokenID != "" {
		token, terr := d.GithubTokens.Reveal(ctx, wr.TokenID)
		if terr != nil {
			d.recordFailure(ctx, wr.ID, fmt.Errorf("reveal token: %w", terr))
			return terr
		}
		pat = token
		// Best-effort last_used bookkeeping; ignore errors.
		_ = d.GithubTokens.Touch(ctx, wr.TokenID)
	}

	result, err := repocloner.CloneOrFetch(ctx, repocloner.CloneOptions{
		GitHubURL: wr.GitHubURL,
		Branch:    wr.Branch,
		PAT:       pat,
		LocalDir:  repocloner.LocalDirFor(d.DataDir, wr.ID),
	})
	if err != nil {
		d.recordFailure(ctx, wr.ID, fmt.Errorf("clone: %w", err))
		return err
	}

	// Register the project row (idempotent — Get-or-Create pattern). Two
	// branches:
	//   a) project already exists → leave it alone (incremental updates
	//      happen via subsequent index runs)
	//   b) project missing → create it with the project_path as host_path
	if _, gerr := projects.Get(ctx, d.DB, wr.ProjectPath); gerr != nil {
		if _, cerr := projects.Create(ctx, d.DB, projects.CreateRequest{
			HostPath: wr.ProjectPath,
		}); cerr != nil && !errors.Is(cerr, projects.ErrConflict) {
			d.recordFailure(ctx, wr.ID, fmt.Errorf("register project: %w", cerr))
			return cerr
		}
	}

	if err := d.WorkspaceRepos.SetStatus(ctx, wr.ID, workspacerepos.StatusIndexing, result.HeadSHA, "", nil); err != nil {
		// Non-fatal — still chain the index job.
		d.Logger.Warn("workspacejobs: set status indexing failed", "repo_id", wr.ID, "err", err)
	}

	// Chain index_repo. Use the same dedupe pattern so a manual reindex
	// fired by the user mid-clone collapses into the natural follow-up.
	if _, eerr := d.Jobs.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeIndexRepo,
		DedupeKey: "index:" + wr.ID,
		Payload:   IndexPayload{RepoID: wr.ID},
	}); eerr != nil && !errors.Is(eerr, jobs.ErrDuplicate) {
		d.recordFailure(ctx, wr.ID, fmt.Errorf("enqueue index: %w", eerr))
		return eerr
	}
	return nil
}

func handleIndex(ctx context.Context, d Deps, job jobs.Job) error {
	var p IndexPayload
	if err := jobs.UnmarshalPayload(job, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if p.RepoID == "" {
		return errors.New("empty repo_id")
	}
	wr, err := d.WorkspaceRepos.GetByID(ctx, p.RepoID)
	if err != nil {
		return fmt.Errorf("load workspace_repo: %w", err)
	}
	cloneDir := repocloner.LocalDirFor(d.DataDir, wr.ID)

	_, _, err = repoindexer.IndexDir(ctx, d.Indexer, wr.ProjectPath, cloneDir, repoindexer.DefaultFilter(), d.Logger)
	if err != nil {
		d.recordFailure(ctx, wr.ID, fmt.Errorf("index: %w", err))
		return err
	}

	// Post-index — build the call-graph used by Louvain in PR5. Non-fatal:
	// if extraction fails, we still consider the repo "indexed" (semantic
	// search continues to work without the graph). The error gets logged
	// so operators can diagnose.
	if stats, cgErr := callgraph.Build(ctx, d.DB, wr.ProjectPath, callgraph.DefaultOptions()); cgErr != nil {
		d.Logger.Warn("workspacejobs: callgraph build failed",
			"repo_id", wr.ID, "err", cgErr)
	} else {
		d.Logger.Info("workspacejobs: callgraph built",
			"repo_id", wr.ID,
			"project_path", wr.ProjectPath,
			"refs_considered", stats.RefsConsidered,
			"refs_with_caller", stats.RefsWithCaller,
			"edges", stats.EdgesAccumulated)
	}

	now := time.Now().UTC()
	if err := d.WorkspaceRepos.SetStatus(ctx, wr.ID, workspacerepos.StatusIndexed, "", "", &now); err != nil {
		return fmt.Errorf("mark indexed: %w", err)
	}

	// Chain a debounced community recompute. We pass the workspace_id
	// from the workspace_repos row, not the job payload, so a future
	// hand-triggered index doesn't need to remember to enqueue it.
	if err := EnqueueComputeCommunities(ctx, d.Jobs, wr.WorkspaceID); err != nil {
		d.Logger.Warn("workspacejobs: could not enqueue communities recompute",
			"workspace_id", wr.WorkspaceID, "err", err)
	}
	return nil
}

// recordFailure flips the workspace_repo into status=failed with the
// error message attached. Logs the error too (handler return value also
// gets logged by the jobs service but at a different layer — duplicate
// is fine).
func (d Deps) recordFailure(ctx context.Context, repoID string, err error) {
	if err == nil {
		return
	}
	d.Logger.Error("workspacejobs: repo failed", "repo_id", repoID, "err", err)
	msg := err.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if uerr := d.WorkspaceRepos.SetStatus(ctx, repoID, workspacerepos.StatusFailed, "", msg, nil); uerr != nil {
		d.Logger.Error("workspacejobs: could not write failed status", "repo_id", repoID, "err", uerr)
	}
}

// Compile-time guard: ClonePayload / IndexPayload encode cleanly.
var _ = func() (any, any) {
	a, _ := json.Marshal(ClonePayload{})
	b, _ := json.Marshal(IndexPayload{})
	return a, b
}
