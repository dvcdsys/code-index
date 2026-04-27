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

	// Files-as-results: --limit is a count of files. Inside each file,
	// every match above min_score is shown, ordered by line number so the
	// reader walks the file top-to-bottom.
	fmt.Printf("Found %d file(s) (%.1fms):\n\n", results.Total, results.QueryTimeMS)

	for i, file := range results.Results {
		// File header. Best score is the rank driver; total match count
		// gives a sense of how relevant this file is overall.
		matchWord := "match"
		if len(file.Matches) != 1 {
			matchWord = "matches"
		}
		langSuffix := ""
		if file.Language != "" {
			langSuffix = " · " + file.Language
		}
		fmt.Printf("%d. %s  [best %.2f]  %d %s%s\n",
			i+1, file.FilePath, file.BestScore, len(file.Matches), matchWord, langSuffix)

		for _, m := range file.Matches {
			// Per-match separator with score + line range + label so the
			// user can scan vertically by relevance, even though matches
			// are in line order.
			label := m.ChunkType
			if m.SymbolName != "" {
				label = fmt.Sprintf("%s %s", m.ChunkType, m.SymbolName)
			}
			rangeStr := fmt.Sprintf("line %d", m.StartLine)
			if m.EndLine != m.StartLine {
				rangeStr = fmt.Sprintf("lines %d-%d", m.StartLine, m.EndLine)
			}
			fmt.Printf("   -- [%.2f] %s  (%s)\n", m.Score, rangeStr, label)

			lang := file.Language
			fmt.Printf("      ```%s\n", lang)
			for _, line := range strings.Split(m.Content, "\n") {
				fmt.Printf("      %s\n", line)
			}
			fmt.Printf("      ```\n")

			// Nested hits — chunks merged INTO this match by the server.
			// They sit textually inside m.Content; this just exposes the
			// inner anchor points so the user can jump to the exact line.
			if len(m.NestedHits) > 0 {
				fmt.Printf("      + %d more match(es) inside:\n", len(m.NestedHits))
				for _, nh := range m.NestedHits {
					nhLabel := nh.ChunkType
					if nh.SymbolName != "" {
						nhLabel = fmt.Sprintf("%s %s", nh.ChunkType, nh.SymbolName)
					}
					fmt.Printf("        · [%.2f] line %d  (%s)\n",
						nh.Score, nh.StartLine, nhLabel)
				}
			}
		}
		fmt.Println()
	}

	return nil
}
