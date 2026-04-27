package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/daemon"
	"github.com/anthropics/code-index/cli/internal/indexer"
	"github.com/spf13/cobra"
)

var (
	initWatch bool
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init [path]",
	Short: "Initialize a project for indexing",
	Long: `Initialize a project by registering it with the API server and starting indexing.

If --watch is specified, also starts a file watcher daemon for auto-reindexing.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVarP(&initWatch, "watch", "w", true, "Start file watcher after init")
}

func runInit(cmd *cobra.Command, args []string) error {
	// Get project path
	projectPath := "."
	if len(args) > 0 {
		projectPath = args[0]
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Check if directory exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", absPath)
	}

	fmt.Printf("Initializing project: %s\n", absPath)

	// Get API client
	client, err := getClient()
	if err != nil {
		return err
	}

	// Check if project already exists
	existing, err := client.GetProject(absPath)
	if err == nil {
		fmt.Printf("✓ Project already exists (status: %s)\n", existing.Status)
	} else {
		// Create new project
		fmt.Println("Creating project...")
		project, err := client.CreateProject(absPath)
		if err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		fmt.Printf("✓ Project created (status: %s)\n", project.Status)
	}

	// Trigger indexing
	cfg, _ := config.Load()
	batchSize := cfg.Indexing.BatchSize
	fmt.Printf("Starting indexing (batch size: %d)...\n", batchSize)
	result, err := indexer.Run(cmd.Context(), client, absPath, false, batchSize, indexer.AutoProgressMode())
	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}
	fmt.Printf("Indexing complete (%d files, %d chunks)\n", result.FilesProcessed, result.ChunksCreated)

	// Add to config
	if err := config.AddProject(absPath, initWatch); err != nil {
		return fmt.Errorf("save to config: %w", err)
	}

	// Start watcher daemon if requested
	if initWatch {
		fmt.Println("\nStarting file watcher daemon...")
		pid, err := daemon.Start(absPath)
		if err != nil {
			fmt.Printf("Warning: could not start watcher daemon: %v\n", err)
			fmt.Println("You can start it manually with: cix watch start")
		} else {
			fmt.Printf("Watcher daemon started (PID: %d)\n", pid)
		}
	}

	fmt.Println("\n✅ Initialization complete!")
	return nil
}
