// Package repoindexer is the in-process driver that turns a cloned git
// repository on disk into an indexed cix project. It bridges the
// clone_repo → index_repo job pipeline (run by package repojobs) and
// the existing three-phase indexer that drives all other code indexing
// in cix.
//
// Why in-process: the CLI traditionally walks the filesystem locally,
// hashes files, then streams batches to the server over HTTP. For
// server-cloned GitHub repos the "source" is already on the server's
// disk (the worker just cloned it). Going out-and-back through HTTP
// for that case would mean dragging the entire 3-phase NDJSON
// streaming machinery into the worker, when we can call the same
// Service.BeginIndexing / ProcessFiles / FinishIndexing methods
// directly.
//
// Boundary: this package owns walking + chunk-payload construction. It
// does NOT own embedding, tokenisation, vectorstore mutation — those
// continue to live in indexer.Service. If embeddings are not configured
// (e.g. in CI tests), the indexer service returns errors that propagate
// back as job failures.
package repoindexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
)

// BatchSize controls how many files we hand the indexer per ProcessFiles
// call. A few hundred is typical CLI behaviour — keeps batch tx commits
// tight and bounds memory.
const BatchSize = 50

// busyRetryCap bounds a single backpressure sleep so an over-large Retry-After
// hint can't stall the walk for minutes between attempts.
const busyRetryCap = 30 * time.Second

// maxBusyWait bounds the TOTAL time spent waiting out embedding-queue
// backpressure for ONE batch before giving up. Generous because saturation is
// transient by design (slots free as in-flight embeds finish), but finite so a
// genuinely wedged embedder surfaces as a failure — which, with the session now
// released on abort, the job queue requeues to resume via reconcile.
const maxBusyWait = 5 * time.Minute

// processBatch sends one batch to the indexer, transparently waiting out
// transient embedding-queue backpressure. ErrBusy ("embedding queue saturated,
// retry after Ns") is the queue asking us to slow down, NOT a real failure: the
// HTTP/CLI client honours it via 503 + Retry-After, and the in-process driver
// must do the same instead of failing the whole run (which previously also
// orphaned the indexer session and bounced every retry off ErrSessionConflict).
// Retries the SAME batch after the hinted delay (capped), bounded by
// maxBusyWait. ProcessFiles bumps the session's lastActivity in its prepare
// stage before the embed stage that returns ErrBusy, so these waits don't trip
// the idle reaper. Any non-busy error is returned immediately.
func processBatch(ctx context.Context, idx *indexer.Service, projectPath, runID string, batch []indexer.FilePayload, logger *slog.Logger) (int, error) {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		_, chunks, _, err := idx.ProcessFiles(ctx, projectPath, runID, batch)
		if err == nil {
			return chunks, nil
		}
		retryAfter, busy := embeddings.IsBusy(err)
		if !busy {
			return 0, err
		}
		delay := time.Duration(retryAfter) * time.Second
		if delay <= 0 {
			delay = time.Second
		}
		if delay > busyRetryCap {
			delay = busyRetryCap
		}
		if waited+delay > maxBusyWait {
			return 0, fmt.Errorf("embedding queue still saturated after %s and %d attempts: %w",
				waited.Round(time.Second), attempt, err)
		}
		logger.Warn("repoindexer: embedding queue saturated — backing off",
			"project", projectPath, "attempt", attempt,
			"retry_after_s", int(delay.Seconds()), "waited_s", int(waited.Seconds()),
			"batch", len(batch))
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return 0, ctx.Err()
		case <-t.C:
		}
		waited += delay
	}
}

// FileFilter decides whether a candidate file should be indexed. Returning
// false skips it silently (no log noise). The default filter rejects
// node_modules, hidden dirs, common build outputs, and files over a size
// cap.
type FileFilter struct {
	ExcludeDirs []string // path segment match — "node_modules", ".git", etc.
	MaxFileSize int64    // bytes; 0 disables the check
	// SkipBinaries, when true (default), drops files whose first 512
	// bytes contain a NUL — a cheap-and-cheerful proxy for "not text".
	SkipBinaries bool
}

// DefaultFilter returns a sensible default ruleset. Mirrors the CLI's
// "obvious junk to skip" list so per-repo settings remain consistent
// across local + workspace projects.
func DefaultFilter() FileFilter {
	return FileFilter{
		ExcludeDirs: []string{
			".git", "node_modules", ".venv", "__pycache__",
			"dist", "build", ".next", ".cache", ".DS_Store",
			"target", ".idea", ".vscode", ".gradle",
			"vendor", // Go vendor — usually mirror of deps already indexed elsewhere
		},
		MaxFileSize:  524288, // 512 KiB
		SkipBinaries: true,
	}
}

// IndexDir runs an end-to-end index pass against a local directory. Three
// behaviours, selected by (wipe, changes):
//
//   - wipe=true (changes ignored) → FULL REBUILD: BeginIndexing(full=true)
//     wipes the previous index state + collection, WalkDir visits every
//     file and re-embeds it. Use only when existing vectors are no longer
//     valid — embedding model/provider change — or an explicit force-rebuild.
//   - wipe=false, changes == nil → RECONCILE / RESUME: BeginIndexing(full=
//     false) preserves existing state; WalkDir visits every file but SKIPS
//     those whose on-disk content hash already matches file_hashes, so a
//     crashed/aborted run resumes for the cost of the un-embedded remainder
//     only. Files gone from disk are deleted. Use for first-index and
//     crash/abort recovery — cheap and idempotent.
//   - wipe=false, changes != nil → INCREMENTAL: only changes.Modified+Added
//     are read+processed and changes.Deleted is fed to FinishIndexing. Use
//     for normal webhook + manual reindex paths where tree.Diff already told
//     us exactly what moved.
//
// Resumability rests on file_hashes: ProcessFiles commits each file's hash
// in the SAME per-file tx as its vectors/symbols/FTS, so every recorded file
// is durably indexed — reconcile can trust a hash match to skip the work.
//
// Returns (filesIndexed, chunksCreated, err). Counts cover only files
// (re)embedded this pass — not unchanged files skipped or left untouched.
func IndexDir(
	ctx context.Context,
	idx *indexer.Service,
	projectPath, rootDir string,
	wipe bool,
	changes *repocloner.ChangeSet,
	filter FileFilter,
	logger *slog.Logger,
) (int, int, error) {
	if idx == nil {
		return 0, 0, errors.New("indexer not configured")
	}
	if logger == nil {
		logger = slog.Default()
	}

	walkAll := changes == nil
	reconcile := walkAll && !wipe

	runID, storedHashes, err := idx.BeginIndexing(ctx, projectPath, wipe)
	if err != nil {
		return 0, 0, fmt.Errorf("begin indexing: %w", err)
	}

	// Release the session on any abort path. A mid-run error (e.g. a transient
	// "embedding queue saturated" or a walk failure) otherwise leaves the
	// session active until the idle-timeout reaps it, and every retry / manual
	// Sync in that window bounces off ErrSessionConflict. FailIndexing is
	// idempotent, so it no-ops on the success path (FinishIndexing already
	// completed the session) and when a force-stop already removed it.
	indexOK := false
	defer func() {
		if !indexOK {
			idx.FailIndexing(ctx, projectPath, runID)
		}
	}()

	// Incremental: the change set already tells us how many files we'll
	// process, so publish the denominator up front for GET /index/status.
	// Walk modes can't know the total until the walk completes (no
	// pre-count), so the status endpoint reports progress without a total.
	if !walkAll {
		idx.SetDiscoveredTotal(projectPath, len(changes.Modified)+len(changes.Added))
	}

	totalFiles := 0
	totalChunks := 0
	totalAccepted := 0
	skipped := 0
	batch := make([]indexer.FilePayload, 0, BatchSize)
	// seen tracks every on-disk path visited in reconcile mode so the
	// delete pass can reclaim file_hashes rows whose file vanished.
	var seen map[string]struct{}
	if reconcile {
		seen = make(map[string]struct{}, len(storedHashes))
	}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		chunks, ferr := processBatch(ctx, idx, projectPath, runID, batch, logger)
		if ferr != nil {
			return fmt.Errorf("process batch: %w", ferr)
		}
		totalAccepted += len(batch)
		totalChunks += chunks
		batch = batch[:0]
		return nil
	}

	// considerFile builds a payload for a single relative path and
	// appends it to the current batch. Shared by the WalkDir (full) and
	// the explicit-list (incremental) drivers so the per-file pipeline
	// stays identical: stat → size cap → read → binary probe → langdetect.
	considerFile := func(rel string) error {
		// Apply ExcludeDirs at the path-prefix level. In WalkDir mode
		// the exclusion already happens via shouldSkipDir (fs.SkipDir
		// prunes the subtree before we ever see its files), so this
		// only fires for the incremental path where the caller hands
		// us paths directly without a directory traversal.
		if filter.shouldSkipPath(rel) {
			return nil
		}
		absPath := filepath.Join(rootDir, filepath.FromSlash(rel))
		fp, ok, ferr := buildPayload(absPath, rel, filter)
		if ferr != nil {
			// Incremental mode: tree.Diff lists a file that vanished
			// between fetch+reset and our read. Rare but observable
			// (very fast subsequent push). Log and skip — the next
			// reindex will see it as Deleted. In reconcile mode treat a
			// transient read error as "still present" so a one-off IO
			// hiccup doesn't drop the file's existing index.
			if reconcile {
				seen[rel] = struct{}{}
			}
			logger.Warn("repoindexer: file dropped", "path", rel, "err", ferr)
			return nil
		}
		if !ok {
			// Not indexable (binary / too large / unknown language). Leaving
			// it out of `seen` lets the reconcile delete pass reclaim any
			// stale rows from when it WAS indexable.
			return nil
		}
		if reconcile {
			seen[rel] = struct{}{}
			if h, found := storedHashes[rel]; found && h == fp.ContentHash {
				// Already embedded under the current model — resume skip.
				skipped++
				return nil
			}
		}
		batch = append(batch, fp)
		if len(batch) >= BatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		return nil
	}

	if walkAll {
		err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Permission errors on a subtree shouldn't kill the whole index.
				logger.Warn("repoindexer: walk skipped", "path", path, "err", walkErr)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if filter.shouldSkipDir(path, rootDir, d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			// Regular file.
			rel, rerr := filepath.Rel(rootDir, path)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			totalFiles++
			return considerFile(rel)
		})
		if err != nil {
			return totalAccepted, totalChunks, fmt.Errorf("walk: %w", err)
		}
	} else {
		// Incremental: iterate the explicit Modified+Added list. Deleted
		// paths flow into FinishIndexing below (no read/process needed).
		// DefaultFilter still applies — e.g. tree.Diff may list a now-too-large
		// file or a path under vendor/, considerFile silently skips both.
		// Modified and Added are processed identically; the indexer's
		// per-file replace semantics make the prior state irrelevant.
		for _, rel := range changes.Modified {
			totalFiles++
			if err := considerFile(rel); err != nil {
				return totalAccepted, totalChunks, err
			}
		}
		for _, rel := range changes.Added {
			totalFiles++
			if err := considerFile(rel); err != nil {
				return totalAccepted, totalChunks, err
			}
		}
	}

	if err := flush(); err != nil {
		return totalAccepted, totalChunks, err
	}

	var deletedPaths []string
	switch {
	case reconcile:
		// Files recorded in file_hashes but no longer present on disk.
		for stored := range storedHashes {
			if _, ok := seen[stored]; !ok {
				deletedPaths = append(deletedPaths, stored)
			}
		}
		logger.Info("repoindexer: reconcile done",
			"project", projectPath, "walked", totalFiles, "embedded", totalAccepted,
			"skipped_unchanged", skipped, "deleted", len(deletedPaths))
	case !walkAll:
		deletedPaths = changes.Deleted
	}
	if _, _, _, ferr := idx.FinishIndexing(ctx, projectPath, runID, deletedPaths, totalFiles); ferr != nil {
		return totalAccepted, totalChunks, fmt.Errorf("finish indexing: %w", ferr)
	}
	indexOK = true
	return totalAccepted, totalChunks, nil
}

// buildPayload reads a file and turns it into an indexer.FilePayload.
// Returns (payload, true, nil) on success, (_, false, nil) when the file
// should be silently skipped (size cap, binary content), and an error on
// IO failure.
func buildPayload(absPath, relPath string, filter FileFilter) (indexer.FilePayload, bool, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return indexer.FilePayload{}, false, err
	}
	if !info.Mode().IsRegular() {
		return indexer.FilePayload{}, false, nil
	}
	if filter.MaxFileSize > 0 && info.Size() > filter.MaxFileSize {
		return indexer.FilePayload{}, false, nil
	}

	raw, err := os.ReadFile(absPath)
	if err != nil {
		return indexer.FilePayload{}, false, err
	}
	if filter.SkipBinaries && looksBinary(raw) {
		return indexer.FilePayload{}, false, nil
	}

	sum := sha256.Sum256(raw)
	lang := langdetect.Detect(relPath)
	if lang == "" {
		return indexer.FilePayload{}, false, nil
	}

	return indexer.FilePayload{
		Path:        relPath,
		Content:     string(raw),
		ContentHash: hex.EncodeToString(sum[:]),
		Language:    lang,
		Size:        len(raw),
	}, true, nil
}

// shouldSkipDir returns true when the directory should be pruned from
// the walk. We match on the leaf segment for the common cases
// (node_modules anywhere in the tree), not the full path.
func (f FileFilter) shouldSkipDir(absPath, rootDir, name string) bool {
	if absPath == rootDir {
		return false
	}
	for _, ex := range f.ExcludeDirs {
		if strings.EqualFold(name, ex) {
			return true
		}
	}
	return false
}

// shouldSkipPath returns true when any component of the slash-separated
// relative path matches an ExcludeDirs entry. Used by the incremental
// driver, which receives explicit per-file paths (no WalkDir, so
// shouldSkipDir's "prune the subtree" trick doesn't apply). Matching is
// case-insensitive to mirror shouldSkipDir.
func (f FileFilter) shouldSkipPath(relPath string) bool {
	if relPath == "" {
		return false
	}
	for _, seg := range strings.Split(relPath, "/") {
		for _, ex := range f.ExcludeDirs {
			if strings.EqualFold(seg, ex) {
				return true
			}
		}
	}
	return false
}

func looksBinary(b []byte) bool {
	const probe = 512
	if len(b) < probe {
		probe := len(b)
		_ = probe
	}
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}
