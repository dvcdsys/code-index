package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/spf13/cobra"
)

var (
	searchLimit     int
	searchLanguages []string
	searchPaths     []string
	searchMinScore  float64
	searchProject   string
)

// searchCmd represents the search command
var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search code semantically",
	Long: `Search code using semantic understanding.

The search understands natural language queries like:
  - "authentication middleware"
  - "database connection retry logic"
  - "error handling in payment flow"

Use --in to restrict search to a specific file or directory.

Examples:
  cix search "JWT validation"
  cix search "user authentication" --limit 20
  cix search "API endpoints" --lang go --lang python
  cix search "error handling" --in src/api/
  cix search "config" --in README.md
  cix search "routes" --in ./api --in ./mcp_server`,
	Args: cobra.ExactArgs(1),
	RunE: runSearch,
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "Maximum number of results")
	searchCmd.Flags().StringSliceVar(&searchLanguages, "lang", nil, "Filter by language")
	searchCmd.Flags().StringSliceVar(&searchPaths, "in", nil, "Search within file or directory (relative or absolute path)")
	searchCmd.Flags().Float64Var(&searchMinScore, "min-score", 0.1, "Minimum relevance score")
	searchCmd.Flags().StringVarP(&searchProject, "project", "p", "", "Project path (default: current directory)")
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := args[0]

	// Get project path
	projectPath := searchProject
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

	// Resolve --in paths to absolute
	resolvedPaths := make([]string, 0, len(searchPaths))
	for _, p := range searchPaths {
		ap, err := filepath.Abs(p)
		if err == nil {
			resolvedPaths = append(resolvedPaths, ap)
		} else {
			resolvedPaths = append(resolvedPaths, p)
		}
	}

	// Perform search
	opts := client.SearchOptions{
		Limit:     searchLimit,
		Languages: searchLanguages,
		Paths:     resolvedPaths,
		MinScore:  searchMinScore,
	}

	if len(resolvedPaths) > 0 {
		fmt.Printf("Searching in %s (filtered: %s)...\n\n", absPath, strings.Join(resolvedPaths, ", "))
	} else {
		fmt.Printf("Searching in %s...\n\n", absPath)
	}

	results, err := apiClient.Search(absPath, query, opts)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if len(results.Results) == 0 {
		fmt.Println("No results found")
		return nil
	}

	// Print results
	fmt.Printf("Found %d result(s) (%.1fms):\n\n", results.Total, results.QueryTimeMS)

	for i, result := range results.Results {
		// Format score as colored
		scoreStr := fmt.Sprintf("%.2f", result.Score)

		// Print result header
		fmt.Printf("%d. [%s] %s:%d-%d\n",
			i+1, scoreStr, result.FilePath, result.StartLine, result.EndLine)

		// Print metadata
		meta := []string{}
		if result.SymbolName != "" {
			meta = append(meta, fmt.Sprintf("Symbol: %s", result.SymbolName))
		}
		meta = append(meta, fmt.Sprintf("Type: %s", result.ChunkType))
		if result.Language != "" {
			meta = append(meta, fmt.Sprintf("Lang: %s", result.Language))
		}
		fmt.Printf("   %s\n", strings.Join(meta, " | "))

		fmt.Printf("   ```%s\n", result.Language)
		content := result.Content
		for _, line := range strings.Split(content, "\n") {
			fmt.Printf("   %s\n", line)
		}
		fmt.Printf("   ```\n\n")
	}

	return nil
}
