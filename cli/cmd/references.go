package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	refsFile    string
	refsLimit   int
	refsProject string
)

var referencesCmd = &cobra.Command{
	Use:     "references <symbol>",
	Aliases: []string{"refs", "usages"},
	Short:   "Find where a symbol is used (Find References)",
	Long: `Find all code locations where a symbol is referenced or used.

Examples:
  cix references HandleRequest
  cix refs AuthMiddleware --limit 50
  cix usages UserService --file ./internal/api/`,
	Args: cobra.ExactArgs(1),
	RunE: runReferences,
}

func init() {
	rootCmd.AddCommand(referencesCmd)
	referencesCmd.Flags().StringVar(&refsFile, "file", "", "Narrow to a specific file")
	referencesCmd.Flags().IntVarP(&refsLimit, "limit", "l", 30, "Maximum results")
	referencesCmd.Flags().StringVarP(&refsProject, "project", "p", "", "Project path (default: current directory)")
}

func runReferences(cmd *cobra.Command, args []string) error {
	symbol := args[0]

	projectPath := refsProject
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

	filePath := refsFile
	if filePath != "" {
		filePath, err = filepath.Abs(filePath)
		if err != nil {
			return fmt.Errorf("resolve file path: %w", err)
		}
	}

	apiClient, err := getClient()
	if err != nil {
		return err
	}

	results, err := apiClient.SearchReferences(absPath, symbol, filePath, refsLimit)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if len(results.Results) == 0 {
		fmt.Printf("No references found for '%s'\n", symbol)
		return nil
	}

	fmt.Printf("Found %d reference(s) for '%s':\n\n", results.Total, symbol)

	for i, ref := range results.Results {
		// Header
		label := ref.ChunkType
		if ref.SymbolName != "" {
			label = fmt.Sprintf("%s in %s", ref.ChunkType, ref.SymbolName)
		}
		fmt.Printf("%d. [%s] %s:%d-%d (%s)\n",
			i+1, label, ref.FilePath, ref.StartLine, ref.EndLine, ref.Language)

		// Content preview (truncated)
		content := ref.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		// Indent content
		for _, line := range strings.Split(content, "\n") {
			fmt.Printf("   %s\n", line)
		}
		fmt.Println()
	}

	return nil
}
