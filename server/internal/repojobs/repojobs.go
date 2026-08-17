// Package repojobs wires the GitHub-repo lifecycle's job handlers into
// the generic internal/jobs queue. It owns nothing — just composes
// gitrepos, githubtokens, repocloner, and repoindexer behind a thin
// Register function called from main.
//
// Despite living next to workspaces in the codebase's history (the
// previous package name was workspacejobs back when GitHub-cloned
// repos only existed as workspace members), nothing in this package
// talks to the workspaces or workspace_projects tables. A repo can
// have an active clone+index pipeline without belonging to any
// workspace; workspaces are an orthogonal organisational layer.
//
// Lifecycle for an external project:
//
//  1. POST /api/v1/git-repos
//     - inserts a projects row (status='pending') and a git_repos row
//     - enqueues clone_repo job (dedupe_key="clone:<path_hash>")
//
//  2. clone_repo handler
//     - reveals PAT via githubtokens.Reveal (if token_id set)
//     - calls repocloner.CloneOrFetch into DataDir/repos/<path_hash>/
//     (passing git_repos.indexed_sha so the cloner can compute a
//     tree.Diff change set when the repo was indexed before)
//     - flips projects.status → indexing
//     - enqueues index_repo job with the determined mode + change set
//     (dedupe_key="index:<path_hash>")
//
//  3. index_repo handler
//     - calls repoindexer.IndexDir(changes=nil) for full reindex or
//     repoindexer.IndexDir(changes=&cs) for incremental — handed off
//     from clone_repo's mode determination
//     - flips projects.status → indexed (or 'error' on failure)
//     - writes last_indexed_at on the projects row
//     - writes git_repos.indexed_sha = last_sha so the NEXT clone job
//     can diff against this point
//
// Mode determination (in handleClone):
//   - ClonePayload.ForceFull (dashboard "force full reindex") → full WIPE
//   - projects.indexed_with_model != currentModel → full WIPE (embedding drift)
//   - g.IndexedSHA empty (first index, OR a run that crashed / was
//     force-stopped before writing indexed_sha) → reconcile RESUME: walk
//     every file but skip those whose on-disk hash already matches
//     file_hashes, so recovery costs only the un-embedded remainder — no wipe
//   - tree.Diff unavailable (old commit gone) → reconcile
//   - otherwise → incremental with the computed ChangeSet
//
// Workspace-level search (when workspaces are in use) is served from
// per-project vector collections via a weighted fan-out, defined in
// internal/httpapi/workspacesearch.go — independent of this package.
package repojobs

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
	"github.com/dvcdsys/code-index/server/internal/repolocks"
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
	// ForceFull requests a full wipe + rebuild. Set by the dashboard's
	// "force full reindex" (POST /reindex?full=true). It is the ONLY way to
	// deliberately discard existing vectors short of an embedding-model
	// change: without it a project with no indexed_sha (first index OR a
	// run that crashed/was force-stopped before finishing) RESUMES via
	// reconcile instead of wiping. omitempty so older queued payloads decode
	// to false (reconcile/incremental) — the safe, non-destructive default.
	ForceFull bool `json:"force_full,omitempty"`
}

// Index modes — what flavour of indexing the index_repo handler should
// run. Stored on IndexPayload.Mode. handleIndex defaults to ModeFull on
// empty/unknown values so a legacy payload (pre-incremental) still does
// the safe thing.
const (
	ModeFull        = "full"
	ModeIncremental = "incremental"
	// ModeReconcile walks the whole tree but skips files whose on-disk
	// content hash already matches file_hashes (no wipe), so a crashed or
	// aborted index resumes from where it stopped instead of re-embedding
	// from zero. It is the default for first-index and recovery; only an
	// embedding model/provider change still uses ModeFull (which wipes).
	ModeReconcile = "reconcile"
)

// IndexPayload carries the mode and (for incremental) the exact change
// set determined by handleClone. The change set lives on the job row,
// not on git_repos, because it's job-scoped state — every clone job
// produces a fresh change set, and we don't want to overwrite the
// previous one until that job completes.
type IndexPayload struct {
	ProjectPath  string   `json:"project_path"`
	Mode         string   `json:"mode,omitempty"`          // "full" | "incremental"; empty = "full" for legacy rows
	ChangedPaths []string `json:"changed_paths,omitempty"` // incremental: Modified + Added paths (incremental only)
	DeletedPaths []string `json:"deleted_paths,omitempty"` // incremental: paths whose chunks/symbols must be removed
	// TargetSHA is the SHA that handleIndex must persist to
	// git_repos.indexed_sha on success. Captured here so a delayed job
	// pickup doesn't race against another clone job that might have
	// updated last_sha in the meantime.
	TargetSHA string `json:"target_sha,omitempty"`
}

// Deps bundles everything the handlers need.
type Deps struct {
	DB           *sql.DB
	Jobs         *jobs.Service
	GitRepos     *gitrepos.Service
	GithubTokens *githubtokens.Service
	Indexer      *indexer.Service
	VectorStore  vectorstore.Interface
	DataDir      string // root for cloned repos: <DataDir>/repos/<path_hash>/
	// RepoLocks serialises the worktree rewrite (git reset --hard inside
	// CloneOrFetch) against concurrent file/tree reads served by the HTTP
	// layer. Shared (same instance) with httpapi.Deps.RepoLocks. Nil-safe:
	// when unset the clone path simply runs without the read/write barrier.
	RepoLocks *repolocks.Locks
	Logger    *slog.Logger
	// DefaultPollIntervalSeconds / MinPollIntervalSeconds resolve the poll
	// cadence when a clone/index cycle finishes for a polling-enabled repo.
	// Mirror config.DefaultPollInterval / config.MinPollInterval.
	DefaultPollIntervalSeconds int
	MinPollIntervalSeconds     int
}

// Register hooks the clone_repo and index_repo job handlers into a
// jobs.Service. Call once from main during startup.
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
	cloneDir := repocloner.LocalDirFor(d.DataDir, hash)

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

	// Serialise the worktree rewrite (git reset --hard inside CloneOrFetch)
	// against concurrent file/tree reads and indexing. WithWrite holds the
	// write-lock only for the duration of the on-disk mutation (not the later
	// DB/enqueue work) and releases it even if CloneOrFetch panics — the jobs
	// worker recovers panics, so a plain Lock/Unlock would leak it forever.
	var result repocloner.Result
	clone := func() error {
		var cerr error
		result, cerr = repocloner.CloneOrFetch(ctx, repocloner.CloneOptions{
			GitHubURL:      g.GitHubURL,
			Branch:         g.Branch,
			PAT:            pat,
			LocalDir:       cloneDir,
			PrevIndexedSHA: g.IndexedSHA,
		})
		return cerr
	}
	if d.RepoLocks != nil {
		err = d.RepoLocks.WithWrite(hash, clone)
	} else {
		err = clone()
	}
	if err != nil {
		d.recordFailure(ctx, g, fmt.Errorf("clone: %w", err))
		return err
	}
	if result.RecloneReason != "" {
		d.Logger.Info("repojobs: checkout discarded and re-cloned",
			"project", g.ProjectPath, "reason", result.RecloneReason)
	}

	if err := d.GitRepos.SetClone(ctx, g.ProjectPath, result.HeadSHA, ""); err != nil {
		d.Logger.Warn("repojobs: set last_sha failed", "project", g.ProjectPath, "err", err)
	}

	// No-op fetch: remote HEAD matches what we already had AND it's
	// already in the index. Skip the whole index job — there's
	// genuinely nothing to do. Still bump last_indexed_at + status so
	// the dashboard reflects that we processed the webhook.
	if result.NoChanges && g.IndexedSHA == result.HeadSHA {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := d.DB.ExecContext(ctx,
			`UPDATE projects
			    SET status = 'indexed', last_indexed_at = ?, updated_at = ?
			  WHERE host_path = ?`,
			now, now, g.ProjectPath,
		); err != nil {
			d.Logger.Warn("repojobs: refresh last_indexed_at failed", "project", g.ProjectPath, "err", err)
		}
		d.Logger.Info("repojobs: no changes, skipping index",
			"project", g.ProjectPath, "head", result.HeadSHA)
		// Terminal point for this cycle (no index job enqueued) — advance
		// the poll clock from here.
		d.reschedulePoll(ctx, g)
		return nil
	}

	// Mode determination. The decision tree errs on the side of full
	// because full is always correct (just slower); incremental is
	// only correct when every precondition holds.
	mode := ModeIncremental
	reason := "tree-diff"
	switch {
	case p.ForceFull:
		// Operator explicitly asked for a full wipe + rebuild
		// (POST /reindex?full=true). This is the only non-model-change
		// path that discards existing vectors — everything else resumes.
		mode, reason = ModeFull, "force-full"
	case g.IndexedSHA == "":
		// First index, OR a prior run that crashed / was force-stopped
		// before writing indexed_sha. Reconcile (not wipe) so recovery
		// resumes from whatever file_hashes already holds instead of
		// re-embedding the whole repo from zero. On a genuinely first
		// index file_hashes is empty, so reconcile embeds everything —
		// same result, no wipe.
		mode, reason = ModeReconcile, "first-or-resume"
	case result.Changes == nil:
		// Either no PrevIndexedSHA was effective (handled above) or
		// tree.Diff failed (old commit not in objects). Walk everything,
		// but reconcile still skips unchanged files by hash.
		mode, reason = ModeReconcile, "no-changeset"
	case d.Indexer != nil && d.Indexer.EmbeddingModel() != "" &&
		d.Indexer.EmbeddingModel() != projectIndexedWithModel(ctx, d.DB, g.ProjectPath):
		// Embedding model changed since the last index — the stored
		// vectors are no longer comparable to fresh queries. Force a
		// full wipe so the whole index uses the current model.
		mode, reason = ModeFull, "model-change"
	}

	if err := setProjectStatus(ctx, d.DB, g.ProjectPath, "indexing"); err != nil {
		d.Logger.Warn("repojobs: set status indexing failed", "project", g.ProjectPath, "err", err)
	}

	payload := IndexPayload{
		ProjectPath: g.ProjectPath,
		Mode:        mode,
		TargetSHA:   result.HeadSHA,
	}
	if mode == ModeIncremental {
		payload.ChangedPaths = append(append([]string{}, result.Changes.Modified...), result.Changes.Added...)
		payload.DeletedPaths = result.Changes.Deleted
	}

	d.Logger.Info("repojobs: clone done, enqueueing index",
		"project", g.ProjectPath,
		"head", result.HeadSHA,
		"mode", mode,
		"reason", reason,
		"changed", len(payload.ChangedPaths),
		"deleted", len(payload.DeletedPaths))

	if _, eerr := d.Jobs.Enqueue(ctx, jobs.EnqueueRequest{
		Type:      TypeIndexRepo,
		DedupeKey: "index:" + hash,
		Payload:   payload,
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

	// Map the payload mode to IndexDir's (wipe, changes) behaviour:
	//   ModeIncremental → wipe=false + changeset (process only the diff).
	//   ModeReconcile   → wipe=false + nil       (walk all, hash-skip, resume).
	//   ModeFull        → wipe=true              (model/provider change).
	//   empty/legacy    → wipe=true              (safe full rebuild).
	var changes *repocloner.ChangeSet
	wipe := false
	switch p.Mode {
	case ModeIncremental:
		changes = &repocloner.ChangeSet{
			Modified: p.ChangedPaths,
			Deleted:  p.DeletedPaths,
		}
	case ModeReconcile:
		// walk all, skip unchanged by hash, no wipe — resumes a crashed run.
	case ModeFull:
		wipe = true
	default:
		// empty Mode from legacy payloads / unknown values: full rebuild.
		wipe = true
	}

	// Read-lock the worktree while indexing: IndexDir walks and reads the same
	// checkout the clone worker rewrites with git reset --hard. Clone (write) and
	// index (read) jobs for one repo can otherwise run on separate workers and a
	// concurrent reset would tear the index across two revisions. Held for the
	// full walk so the writer waits rather than resetting under us.
	index := func() error {
		_, _, ierr := repoindexer.IndexDir(ctx, d.Indexer, g.ProjectPath, cloneDir, wipe, changes, repoindexer.DefaultFilter(), d.Logger)
		return ierr
	}
	if d.RepoLocks != nil {
		err = d.RepoLocks.WithRead(projects.HashPath(g.ProjectPath), index)
	} else {
		err = index()
	}
	if err != nil {
		// A force-stop deletes the active indexer session out from under
		// IndexDir; the next ProcessFiles/FinishIndexing then returns
		// ErrNoSession. That's a deliberate cancellation, not a failure —
		// don't mark the project 'error' or set last_error (CancelIndexing
		// already flipped status back to a terminal state). Return nil so
		// the queue marks the (likely already-deleted) job done instead of
		// retrying it, which would just re-index what we asked to stop.
		if errors.Is(err, context.Canceled) {
			// Server shutting down mid-index (jobsCancel fired). Do NOT mark
			// the project 'error' — return the error so the queue requeues the
			// job; the next start resumes from file_hashes via reconcile.
			d.Logger.Info("repojobs: index interrupted by shutdown — will resume on restart", "project", g.ProjectPath)
			return err
		}
		if errors.Is(err, indexer.ErrNoSession) {
			// The session vanished mid-run. Distinguish a deliberate
			// force-stop (user cancel) — which we swallow as before — from
			// an involuntary loss (idle reap / crash). The latter MUST
			// surface as a failure so the queue retries and the reconcile
			// path resumes, instead of silently leaving the project stuck
			// in 'indexing' with a partial index.
			reason := d.Indexer.ConsumeGoneReason(g.ProjectPath)
			if reason == "user-cancel" {
				d.Logger.Info("repojobs: index cancelled (force-stop)", "project", g.ProjectPath)
				d.reschedulePoll(ctx, g)
				return nil
			}
			d.Logger.Warn("repojobs: index session lost mid-run — will retry/resume",
				"project", g.ProjectPath, "reason", reason)
			d.recordFailure(ctx, g, fmt.Errorf("index: session lost mid-run (reason=%q): %w", reason, err))
			return err
		}
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

	// Record which SHA is now in the index so the NEXT clone job has a
	// stable diff base. Use TargetSHA from the payload (captured when
	// handleClone decided the mode) rather than re-reading last_sha,
	// which a concurrent clone job may have moved forward.
	targetSHA := p.TargetSHA
	if targetSHA == "" {
		// Legacy payload (queued by older binary). Fall back to last_sha
		// — slightly racy but correct in steady state.
		targetSHA = g.LastSHA
	}
	if targetSHA != "" {
		if err := d.GitRepos.SetIndexedSHA(ctx, g.ProjectPath, targetSHA); err != nil {
			d.Logger.Warn("repojobs: set indexed_sha failed", "project", g.ProjectPath, "err", err)
		}
	}
	// Index finished — this is the terminal point of the cycle, so the
	// next poll is scheduled relative to "now" (end of indexing).
	d.reschedulePoll(ctx, g)
	return nil
}

// projectIndexedWithModel reads projects.indexed_with_model for one
// project. Returns empty on NULL OR on any error — empty maps to "we
// don't know what model this was indexed with", which the caller treats
// as "trust the live model is fine". This avoids forcing a full
// reindex on legacy pre-PR-E rows that just haven't been touched since
// drift tracking was added.
func projectIndexedWithModel(ctx context.Context, db *sql.DB, projectPath string) string {
	var m sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT indexed_with_model FROM projects WHERE host_path = ?`, projectPath,
	).Scan(&m); err != nil {
		return ""
	}
	return m.String
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
	d.Logger.Error("repojobs: repo failed", "project", g.ProjectPath, "err", err)
	msg := err.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	if uerr := d.GitRepos.SetClone(ctx, g.ProjectPath, "", msg); uerr != nil {
		d.Logger.Error("repojobs: could not write last_error", "project", g.ProjectPath, "err", uerr)
	}
	if uerr := setProjectStatus(ctx, d.DB, g.ProjectPath, "error"); uerr != nil {
		d.Logger.Error("repojobs: could not write status=error", "project", g.ProjectPath, "err", uerr)
	}
	// Even on terminal failure, keep polling alive so the repo retries
	// on its next interval rather than going dark.
	d.reschedulePoll(ctx, g)
}

// reschedulePoll advances next_poll_at to now + the effective interval for a
// polling-enabled repo. Called at every terminal point of a clone/index cycle
// (no-change clone, index success, terminal failure) so the poll cadence is
// measured from the END of the last cycle. No-op for non-polling repos. The
// passed-in repo snapshot's PollIntervalSeconds is authoritative for the
// interval; PollingEnabled gates the write.
func (d Deps) reschedulePoll(ctx context.Context, g gitrepos.GitRepo) {
	if !g.PollingEnabled {
		return
	}
	secs := gitrepos.EffectivePollInterval(g, d.DefaultPollIntervalSeconds, d.MinPollIntervalSeconds)
	next := time.Now().UTC().Add(time.Duration(secs) * time.Second)
	if err := d.GitRepos.RescheduleNextPoll(ctx, g.ProjectPath, next); err != nil {
		d.Logger.Warn("repojobs: reschedule next poll failed", "project", g.ProjectPath, "err", err)
	}
}

// ReconcileStuckProjects fixes external projects left in a non-terminal status
// ('indexing'/'cloning') by a process killed mid-pipeline (e.g. OOM) when no
// job remains to drive them — recoverOrphanedJobs may have marked the
// abandoned job 'failed' after exhausting retries, and a 'failed' job is never
// re-claimed. Without this the dashboard shows "indexing" forever. We flip
// such projects to 'error' so the state is honest; the operator can then Sync,
// which resumes from file_hashes via reconcile. Call once at startup, AFTER
// jobs.Service.Start (which runs orphan recovery synchronously).
func ReconcileStuckProjects(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.host_path, p.path_hash
		FROM projects p
		JOIN git_repos g ON g.project_path = p.host_path
		WHERE p.status IN ('indexing', 'cloning')`)
	if err != nil {
		logger.Error("repojobs: reconcile stuck projects — query failed", "err", err)
		return
	}
	type stuck struct{ path, hash string }
	var candidates []stuck
	for rows.Next() {
		var s stuck
		if err := rows.Scan(&s.path, &s.hash); err != nil {
			rows.Close()
			logger.Error("repojobs: reconcile stuck projects — scan failed", "err", err)
			return
		}
		candidates = append(candidates, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		logger.Error("repojobs: reconcile stuck projects — iterate failed", "err", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	fixed := 0
	for _, s := range candidates {
		// Leave projects that still have a live (pending/running) pipeline job
		// — those WILL run (or were just requeued by orphan recovery).
		var active int
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM jobs
			WHERE status IN ('pending', 'running')
			  AND dedupe_key IN (?, ?)`,
			"clone:"+s.hash, "index:"+s.hash,
		).Scan(&active); err != nil {
			logger.Warn("repojobs: reconcile — active-job check failed", "project", s.path, "err", err)
			continue
		}
		if active > 0 {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE projects SET status = 'error', updated_at = ? WHERE host_path = ?`,
			now, s.path,
		); err != nil {
			logger.Warn("repojobs: reconcile — set error failed", "project", s.path, "err", err)
			continue
		}
		fixed++
		logger.Warn("repojobs: project stuck mid-index with no active job after restart; marked 'error' — Sync to resume",
			"project", s.path)
	}
	if fixed > 0 {
		logger.Info("repojobs: reconciled stuck projects on startup", "count", fixed)
	}
}

// Compile-time guard: payloads encode cleanly.
var _ = func() (any, any) {
	a, _ := json.Marshal(ClonePayload{})
	b, _ := json.Marshal(IndexPayload{})
	return a, b
}
