// Package repoindexer is the in-process driver that turns a cloned git
// repository on disk into an indexed cix project. It bridges the
// workspaces feature's job pipeline (clone_repo → ??? → workspace_repo
// status=indexed) and the existing three-phase indexer that drives all
// other code indexing in cix.
//
// Why in-process: the CLI traditionally walks the filesystem locally,
// hashes files, then streams batches to the server over HTTP. For the
// workspaces feature the "source" is already on the server's disk (the
// worker just cloned it). Going out-and-back through HTTP for that case
// would mean dragging the entire 3-phase NDJSON streaming machinery into
// the worker, when we can call the same Service.BeginIndexing /
// ProcessFiles / FinishIndexing methods directly.
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

	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
)

// BatchSize controls how many files we hand the indexer per ProcessFiles
// call. A few hundred is typical CLI behaviour — keeps batch tx commits
// tight and bounds memory.
const BatchSize = 50

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

// IndexDir runs a full end-to-end index pass against a local directory:
// BeginIndexing(full=true) → ProcessFiles batches → FinishIndexing. The
// projects table row must already exist (caller's responsibility — the
// worker creates it before clone_repo runs). On any error mid-way, the
// indexer's internal session timer cleans up after an hour; we don't
// explicitly cancel since "best-effort retry" is the expected pattern.
//
// Returns (filesIndexed, chunksCreated, err).
func IndexDir(
	ctx context.Context,
	idx *indexer.Service,
	projectPath, rootDir string,
	filter FileFilter,
	logger *slog.Logger,
) (int, int, error) {
	if idx == nil {
		return 0, 0, errors.New("indexer not configured")
	}
	if logger == nil {
		logger = slog.Default()
	}

	runID, _, err := idx.BeginIndexing(ctx, projectPath, true)
	if err != nil {
		return 0, 0, fmt.Errorf("begin indexing: %w", err)
	}

	totalFiles := 0
	totalChunks := 0
	totalAccepted := 0
	batch := make([]indexer.FilePayload, 0, BatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, chunks, _, ferr := idx.ProcessFiles(ctx, projectPath, runID, batch)
		if ferr != nil {
			return fmt.Errorf("process batch: %w", ferr)
		}
		totalAccepted += len(batch)
		totalChunks += chunks
		batch = batch[:0]
		return nil
	}

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

		fp, ok, ferr := buildPayload(path, rel, filter)
		if ferr != nil {
			logger.Warn("repoindexer: file dropped", "path", rel, "err", ferr)
			return nil
		}
		if !ok {
			return nil
		}
		batch = append(batch, fp)
		if len(batch) >= BatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return totalAccepted, totalChunks, fmt.Errorf("walk: %w", err)
	}
	if err := flush(); err != nil {
		return totalAccepted, totalChunks, err
	}

	if _, _, _, ferr := idx.FinishIndexing(ctx, projectPath, runID, nil, totalFiles); ferr != nil {
		return totalAccepted, totalChunks, fmt.Errorf("finish indexing: %w", ferr)
	}
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
