package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	filesLimit   int
	filesProject string
)

// filesCmd represents the files command
var filesCmd = &cobra.Command{
	Use:   "files <pattern>",
	Short: "Search files by path pattern",
	Long: `Search for files in an indexed project by path pattern.

Useful for finding files when you know part of the name or path.

Examples:
  cix files "auth"
  cix files "controller" --limit 20
  cix files "config.yaml" -p /path/to/project`,
	Args: cobra.ExactArgs(1),
	RunE: runFiles,
}

func init() {
	rootCmd.AddCommand(filesCmd)
	filesCmd.Flags().IntVarP(&filesLimit, "limit", "l", 20, "Maximum number of results")
	filesCmd.Flags().StringVarP(&filesProject, "project", "p", "", "Project path (default: current directory)")
}

func runFiles(cmd *cobra.Command, args []string) error {
	pattern := args[0]

	projectPath := filesProject
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

	fmt.Printf("Searching files in %s...\n\n", absPath)

	results, err := apiClient.SearchFiles(absPath, pattern, filesLimit)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if len(results.Files) == 0 {
		fmt.Println("No files found")
		return nil
	}

	fmt.Printf("Found %d file(s):\n\n", results.Total)

	for i, f := range results.Files {
		// Try to show relative path
		relPath, relErr := filepath.Rel(absPath, f.Path)
		displayPath := f.Path
		if relErr == nil {
			displayPath = relPath
		}

		fmt.Printf("%d. %s", i+1, displayPath)
		if f.Language != "" {
			fmt.Printf("  (%s)", f.Language)
		}
		fmt.Println()
	}

	return nil
}