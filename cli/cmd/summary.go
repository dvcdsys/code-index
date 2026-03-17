package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var summaryProject string

// summaryCmd represents the summary command
var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show project summary overview",
	Long: `Display a summary of the indexed project including:
- Languages and file counts
- Top directories
- Symbol statistics

Examples:
  cix summary
  cix summary -p /path/to/project`,
	RunE: runSummary,
}

func init() {
	rootCmd.AddCommand(summaryCmd)
	summaryCmd.Flags().StringVarP(&summaryProject, "project", "p", "", "Project path (default: current directory)")
}

func runSummary(cmd *cobra.Command, args []string) error {
	projectPath := summaryProject
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

	apiClient, err := getClient()
	if err != nil {
		return err
	}

	absPath = findProjectRoot(absPath, apiClient)

	summary, err := apiClient.GetSummary(absPath)
	if err != nil {
		return fmt.Errorf("get summary: %w", err)
	}

	// Header
	fmt.Printf("Project: %s\n", summary.HostPath)
	fmt.Printf("Status: %s\n", formatStatus(summary.Status))
	fmt.Println()

	// Stats
	fmt.Printf("Total files:   %d\n", summary.TotalFiles)
	fmt.Printf("Total chunks:  %d\n", summary.TotalChunks)
	fmt.Printf("Total symbols: %d\n", summary.TotalSymbols)
	fmt.Println()

	// Languages
	if len(summary.Languages) > 0 {
		fmt.Printf("Languages: %s\n", strings.Join(summary.Languages, ", "))
		fmt.Println()
	}

	// Top directories
	if len(summary.TopDirectories) > 0 {
		fmt.Println("Top directories:")
		for _, dir := range summary.TopDirectories {
			path, _ := dir["path"].(string)
			count, _ := dir["file_count"].(float64)
			if path != "" {
				relPath, relErr := filepath.Rel(absPath, path)
				displayPath := path
				if relErr == nil {
					displayPath = relPath
				}
				fmt.Printf("  %s/ (%d files)\n", displayPath, int(count))
			}
		}
		fmt.Println()
	}

	// Recent symbols
	if len(summary.RecentSymbols) > 0 {
		fmt.Println("Top symbols:")
		for _, sym := range summary.RecentSymbols {
			name, _ := sym["name"].(string)
			kind, _ := sym["kind"].(string)
			if name != "" {
				fmt.Printf("  [%s] %s\n", kind, name)
			}
		}
	}

	return nil
}
