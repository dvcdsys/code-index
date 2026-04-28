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
	searchExcludes  []string
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
  cix search "routes" --in ./api --in ./mcp_server
  cix search "main entry point" --exclude bench/fixtures --exclude legacy`,
	Args: cobra.ExactArgs(1),
	RunE: runSearch,
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "Maximum number of results")
	searchCmd.Flags().StringSliceVar(&searchLanguages, "lang", nil, "Filter by language")
	searchCmd.Flags().StringSliceVar(&searchPaths, "in", nil, "Search within file or directory (relative or absolute path)")
	searchCmd.Flags().StringSliceVar(&searchExcludes, "exclude", nil, "Exclude file or directory from results (relative or absolute path)")
	// Default threshold of 0.4 calibrated for CodeRankEmbed-Q8_0 with
	// path-aware embedding (CIX_EMBED_INCLUDE_PATH=true). Below 0.4 results
	// are usually unrelated; lower it explicitly for very specific or
	// long-tail queries via --min-score 0.2.
	searchCmd.Flags().Float64Var(&searchMinScore, "min-score", 0.4, "Minimum relevance score (lower with --min-score 0.2 if your query returns nothing)")
	searchCmd.Flags().StringVarP(&searchProject, "project", "p", "", "Project path (default: current directory)")
}

// resolveFilterPaths normalises --in / --exclude inputs to absolute paths
// so the server's prefix-match against canonical FilePaths in the vector
// store works regardless of whether the user wrote a relative or absolute
// argument. Inputs that don't resolve are passed through unchanged so a
// substring match (server-side) can still fire.
func resolveFilterPaths(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if ap, err := filepath.Abs(p); err == nil {
			out = append(out, ap)
		} else {
			out = append(out, p)
		}
	}
	return out
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
	resolvedPaths := resolveFilterPaths(searchPaths)
	resolvedExcludes := resolveFilterPaths(searchExcludes)

	// Perform search
	opts := client.SearchOptions{
		Limit:     searchLimit,
		Languages: searchLanguages,
		Paths:     resolvedPaths,
		Excludes:  resolvedExcludes,
		MinScore:  searchMinScore,
	}

	switch {
	case len(resolvedPaths) > 0 && len(resolvedExcludes) > 0:
		fmt.Printf("Searching in %s (filtered: %s, excluded: %s)...\n\n",
			absPath, strings.Join(resolvedPaths, ", "), strings.Join(resolvedExcludes, ", "))
	case len(resolvedPaths) > 0:
		fmt.Printf("Searching in %s (filtered: %s)...\n\n", absPath, strings.Join(resolvedPaths, ", "))
	case len(resolvedExcludes) > 0:
		fmt.Printf("Searching in %s (excluded: %s)...\n\n", absPath, strings.Join(resolvedExcludes, ", "))
	default:
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
		// Display the path relative to the project root when possible —
		// agents and humans both read shorter paths faster, and absolute
		// paths just leak filesystem layout into the agent context window.
		displayPath := file.FilePath
		if rel, relErr := filepath.Rel(absPath, file.FilePath); relErr == nil {
			displayPath = rel
		}
		fmt.Printf("%d. %s  [best %.2f]  %d %s%s\n",
			i+1, displayPath, file.BestScore, len(file.Matches), matchWord, langSuffix)

		// Suppress the per-match score line when there's exactly one match
		// and its score equals the file-level best score — the two would
		// just print the same number twice. With multiple matches we keep
		// the per-match score because it differentiates them.
		suppressMatchScore := len(file.Matches) == 1 && file.Matches[0].Score == file.BestScore

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
			if suppressMatchScore {
				fmt.Printf("   -- %s  (%s)\n", rangeStr, label)
			} else {
				fmt.Printf("   -- [%.2f] %s  (%s)\n", m.Score, rangeStr, label)
			}

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
