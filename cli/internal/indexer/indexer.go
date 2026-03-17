package indexer

import (
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
func Run(apiClient *client.Client, projectPath string, full bool, batchSize int) (*Result, error) {
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

	// Phase 3: Send files in batches
	for i := 0; i < len(toProcess); i += batchSize {
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

		resp, err := apiClient.SendFiles(projectPath, beginResp.RunID, payloads)
		if err != nil {
			return nil, fmt.Errorf("send files (batch %d-%d): %w", i+1, end, err)
		}

		fmt.Printf("  Processed %d/%d files (%d chunks)\n",
			resp.FilesProcessedTotal, len(toProcess), resp.ChunksCreated)
	}

	// Phase 4: Finish — server cleans up deleted files and finalizes the run
	finishResp, err := apiClient.FinishIndex(
		projectPath, beginResp.RunID, deletedPaths, len(discovered),
	)
	if err != nil {
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
