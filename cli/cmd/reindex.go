package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/indexer"
	"github.com/spf13/cobra"
)

var (
	reindexFull    bool
	reindexProject string
)

// reindexCmd represents the reindex command
var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Trigger manual reindexing",
	Long: `Trigger reindexing for a project.

By default, performs incremental reindexing (only changed files).
Use --full to reindex all files from scratch.
Use 'cix config set indexing.batch_size 10' to control memory usage.`,
	RunE: runReindex,
}

func init() {
	rootCmd.AddCommand(reindexCmd)
	reindexCmd.Flags().BoolVar(&reindexFull, "full", false, "Full reindex (all files)")
	reindexCmd.Flags().StringVarP(&reindexProject, "project", "p", "", "Project path (default: current directory)")
}

func runReindex(cmd *cobra.Command, args []string) error {
	// Get project path
	projectPath := reindexProject
	if projectPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		projectPath = cwd
	}

	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Get API client
	apiClient, err := getClient()
	if err != nil {
		return err
	}

	absPath = findProjectRoot(absPath, apiClient)

	indexType := "Incremental"
	if reindexFull {
		indexType = "Full"
	}

	cfg, _ := config.Load()
	batchSize := cfg.Indexing.BatchSize

	fmt.Printf("%s reindexing: %s (batch size: %d)\n", indexType, absPath, batchSize)

	result, err := indexer.Run(apiClient, absPath, reindexFull, batchSize)
	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	fmt.Printf("\nIndexing complete\n")
	fmt.Printf("  Files: %d discovered, %d processed\n", result.FilesDiscovered, result.FilesProcessed)
	fmt.Printf("  Chunks: %d created\n", result.ChunksCreated)
	fmt.Printf("  Time: %s\n", result.Elapsed.Round(time.Millisecond))

	return nil
}
