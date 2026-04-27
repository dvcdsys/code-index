package indexer

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/discovery"
)

const defaultBatchSize = 5

// Result holds the outcome of an indexing run.
type Result struct {
	RunID           string
	FilesDiscovered int
	FilesProcessed  int
	ChunksCreated   int
	Elapsed         time.Duration
}

// Run performs a complete index cycle: begin → discover → diff → send batches → finish.
//
// ctx is honoured for cancellation: a SIGINT-derived ctx (or a watcher's stop
// signal) propagates through to the streaming SendFilesStreaming call, which
// closes the HTTP connection. The server-side streaming handler sees the
// disconnect and frees the project's session lock immediately, so the next
// reindex doesn't hit 409. As a belt-and-braces, this function defers an
// explicit CancelIndex call for the active run on early exit.
//
// mode controls how per-file progress events are rendered. Pass
// AutoProgressMode() for `cix reindex` (TTY-aware), ProgressQuiet for the
// watcher (only summary + errors hit the log).
func Run(
	ctx context.Context,
	apiClient *client.Client,
	projectPath string,
	full bool,
	batchSize int,
	mode ProgressMode,
) (*Result, error) {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	startTime := time.Now()

	// Phase 1: Begin — get stored hashes for diffing
	fmt.Println("Starting indexing session...")
	beginResp, err := apiClient.BeginIndex(projectPath, full)
	if err != nil {
		return nil, fmt.Errorf("begin index: %w", err)
	}
	fmt.Printf("  Session: %s\n", beginResp.RunID)

	// Belt-and-braces: if we exit early (ctx cancellation, network error,
	// SendFilesStreaming failure), tell the server to release the project
	// lock instead of leaving it for the 1-hour TTL. CancelIndex is
	// idempotent and fast.
	cancelDone := false
	defer func() {
		if !cancelDone {
			_, _ = apiClient.CancelIndex(projectPath)
		}
	}()

	// Phase 2: Discover files on disk
	fmt.Println("Discovering files...")
	discovered, err := discovery.Discover(projectPath, discovery.Options{})
	if err != nil {
		return nil, fmt.Errorf("discover files: %w", err)
	}
	fmt.Printf("  Found %d files\n", len(discovered))

	// Diff local hashes against stored hashes to determine what needs processing
	var toProcess []discovery.DiscoveredFile
	deletedPaths := make([]string, 0)

	if full {
		toProcess = discovered
	} else {
		storedHashes := beginResp.StoredHashes
		currentPaths := make(map[string]bool, len(discovered))

		for _, f := range discovered {
			currentPaths[f.Path] = true
			storedHash, exists := storedHashes[f.Path]
			if !exists || storedHash != f.ContentHash {
				toProcess = append(toProcess, f)
			}
		}

		// Files present in the server but gone from disk
		for storedPath := range storedHashes {
			if !currentPaths[storedPath] {
				deletedPaths = append(deletedPaths, storedPath)
			}
		}
	}

	if len(deletedPaths) > 0 {
		fmt.Printf("  %d deleted file(s)\n", len(deletedPaths))
	}

	if len(toProcess) == 0 {
		fmt.Println("  No files changed")
	} else {
		fmt.Printf("  %d file(s) to process\n", len(toProcess))
	}

	// Phase 3: Send files in batches via streaming. Each batch gets its own
	// progressRenderer so per-file indices restart from 1 in the renderer's
	// context but display globally as (batchOffset+i).
	for i := 0; i < len(toProcess); i += batchSize {
		// Honour ctx cancellation between batches; mid-batch cancellation
		// is handled inside SendFilesStreaming.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		end := i + batchSize
		if end > len(toProcess) {
			end = len(toProcess)
		}

		batch := toProcess[i:end]
		payloads := make([]client.FilePayload, 0, len(batch))

		for _, f := range batch {
			content, err := os.ReadFile(f.Path)
			if err != nil {
				fmt.Printf("  Warning: cannot read %s: %v\n", f.Path, err)
				continue
			}

			payloads = append(payloads, client.FilePayload{
				Path:        f.Path,
				Content:     string(content),
				ContentHash: f.ContentHash,
				Language:    f.Language,
				Size:        int(f.Size),
			})
		}

		if len(payloads) == 0 {
			continue
		}

		// batchOffset is 1-based offset of the first payload in this batch
		// within the overall toProcess slice. Renderer adds ev.FileIndex
		// (which is also 1-based per batch) and prints `[N/total]`.
		renderer := newProgressRenderer(mode, len(toProcess), i)
		_, err := apiClient.SendFilesStreaming(
			ctx, projectPath, beginResp.RunID, payloads, renderer.onEvent,
		)
		if err != nil {
			return nil, fmt.Errorf("send files (batch %d-%d): %w", i+1, end, err)
		}
	}

	// Phase 4: Finish — server cleans up deleted files and finalizes the run.
	// We mark cancelDone before this point so the deferred CancelIndex doesn't
	// fire on the happy path.
	cancelDone = true
	finishResp, err := apiClient.FinishIndex(
		projectPath, beginResp.RunID, deletedPaths, len(discovered),
	)
	if err != nil {
		// Restore the deferred cancel — finish failed, lock should be released.
		_, _ = apiClient.CancelIndex(projectPath)
		return nil, fmt.Errorf("finish index: %w", err)
	}

	elapsed := time.Since(startTime)

	return &Result{
		RunID:           beginResp.RunID,
		FilesDiscovered: len(discovered),
		FilesProcessed:  finishResp.FilesProcessed,
		ChunksCreated:   finishResp.ChunksCreated,
		Elapsed:         elapsed,
	}, nil
}
