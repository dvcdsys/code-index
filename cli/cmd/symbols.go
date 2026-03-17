package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	symbolsLimit   int
	symbolsKinds   []string
	symbolsProject string
)

// symbolsCmd represents the symbols command
var symbolsCmd = &cobra.Command{
	Use:   "symbols <query>",
	Short: "Search for symbols (functions, classes, etc.) by name",
	Long: `Search for code symbols by name using fast indexed lookup.

Supported symbol kinds: function, class, method, type

Examples:
  cix symbols handleRequest
  cix symbols AuthMiddleware --kind function --kind method
  cix symbols User --kind class`,
	Args: cobra.ExactArgs(1),
	RunE: runSymbols,
}

func init() {
	rootCmd.AddCommand(symbolsCmd)
	symbolsCmd.Flags().IntVarP(&symbolsLimit, "limit", "l", 20, "Maximum number of results")
	symbolsCmd.Flags().StringSliceVar(&symbolsKinds, "kind", nil, "Filter by symbol kind")
	symbolsCmd.Flags().StringVarP(&symbolsProject, "project", "p", "", "Project path (default: current directory)")
}

func runSymbols(cmd *cobra.Command, args []string) error {
	query := args[0]

	// Get project path
	projectPath := symbolsProject
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

	// Search symbols
	fmt.Printf("Searching symbols in %s...\n\n", absPath)

	results, err := apiClient.SearchSymbols(absPath, query, symbolsKinds, symbolsLimit)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if len(results.Results) == 0 {
		fmt.Println("No symbols found")
		return nil
	}

	// Print results
	fmt.Printf("Found %d symbol(s):\n\n", results.Total)

	for _, symbol := range results.Results {
		// Print symbol info
		fmt.Printf("[%s] %s\n", symbol.Kind, symbol.Name)

		// Parent info
		if symbol.ParentName != nil && *symbol.ParentName != "" {
			fmt.Printf("  Parent: %s\n", *symbol.ParentName)
		}

		// Location
		fmt.Printf("  Location: %s:%d-%d (%s)\n",
			symbol.FilePath, symbol.Line, symbol.EndLine, symbol.Language)

		// Signature
		if symbol.Signature != nil && *symbol.Signature != "" {
			fmt.Printf("  Signature: %s\n", *symbol.Signature)
		}

		fmt.Println()
	}

	return nil
}
