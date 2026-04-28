package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/code-index/cli/internal/daemon"
	"github.com/spf13/cobra"
)

var statusProject string

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show project indexing status",
	Long: `Display the current indexing status for a project.

Shows: files indexed, chunks created, symbols found, languages detected, and indexing progress.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringVarP(&statusProject, "project", "p", "", "Project path (default: current directory)")
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Get project path
	projectPath := statusProject
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

	// Get project info
	project, err := apiClient.GetProject(absPath)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// Print project info
	fmt.Printf("Project: %s\n", project.HostPath)
	fmt.Printf("Status: %s\n", formatStatus(project.Status))

	if len(project.Languages) > 0 {
		fmt.Printf("Languages: %s\n", strings.Join(project.Languages, ", "))
	}

	fmt.Println()
	fmt.Printf("Files: %d (indexed: %d)\n", project.Stats.TotalFiles, project.Stats.IndexedFiles)
	fmt.Printf("Chunks: %d\n", project.Stats.TotalChunks)
	fmt.Printf("Symbols: %d\n", project.Stats.TotalSymbols)

	if project.LastIndexedAt != nil {
		fmt.Printf("\nLast indexed: %s\n", project.LastIndexedAt.Format("2006-01-02 15:04:05"))
	}

	// Watcher daemon status — surfaces silent stale-index situations where
	// the user thinks the index is fresh because LastIndexedAt is recent,
	// but the watcher has actually died and the project has drifted.
	wstatus := daemon.GetStatus(absPath)
	if wstatus.Running {
		fmt.Printf("Watcher: ✓ running (PID %d)\n", wstatus.PID)
	} else {
		fmt.Print("Watcher: ✗ not running")
		if project.LastIndexedAt != nil {
			elapsed := time.Since(*project.LastIndexedAt)
			fmt.Printf(" — last index sync %s ago", humanDuration(elapsed))
		}
		fmt.Println()
	}

	// Get indexing progress if in progress
	if project.Status == "indexing" {
		fmt.Println("\nIndexing in progress...")
		progress, err := apiClient.GetIndexStatus(absPath)
		if err != nil {
			fmt.Printf("Warning: Could not get progress: %v\n", err)
		} else {
			printProgress(progress.Progress)
		}
	}

	return nil
}

// humanDuration returns a human-readable approximation of d for the
// "watcher down for ..." status line. Coarse on purpose — exact seconds
// are noise here; the user just needs to know whether drift is plausible.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

func formatStatus(status string) string {
	switch status {
	case "indexed":
		return "✓ Indexed"
	case "indexing":
		return "⏳ Indexing"
	case "created":
		return "○ Created (not indexed)"
	case "error":
		return "✗ Error"
	default:
		return status
	}
}

func printProgress(progress map[string]interface{}) {
	if progress == nil {
		return
	}

	if phase, ok := progress["phase"].(string); ok {
		fmt.Printf("  Phase: %s\n", phase)
	}

	if filesProcessed, ok := progress["files_processed"].(float64); ok {
		if filesTotal, ok := progress["files_total"].(float64); ok {
			percent := 0.0
			if filesTotal > 0 {
				percent = (filesProcessed / filesTotal) * 100
			}
			fmt.Printf("  Progress: %d/%d files (%.1f%%)\n",
				int(filesProcessed), int(filesTotal), percent)
		}
	}

	if chunks, ok := progress["chunks_created"].(float64); ok {
		fmt.Printf("  Chunks created: %d\n", int(chunks))
	}

	if elapsed, ok := progress["elapsed_seconds"].(float64); ok {
		fmt.Printf("  Elapsed: %.1fs\n", elapsed)
	}

	if remaining, ok := progress["estimated_remaining"].(float64); ok {
		fmt.Printf("  ETA: %.1fs remaining\n", remaining)
	}
}
