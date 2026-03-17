package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	defKind    string
	defFile    string
	defLimit   int
	defProject string
)

var definitionsCmd = &cobra.Command{
	Use:     "definitions <symbol>",
	Aliases: []string{"def", "goto"},
	Short:   "Find where a symbol is defined (Go to Definition)",
	Long: `Find the definition location of a symbol — functions, classes, methods, types.

Examples:
  cix definitions HandleRequest
  cix def AuthMiddleware --kind function
  cix goto UserService --file ./internal/service.go`,
	Args: cobra.ExactArgs(1),
	RunE: runDefinitions,
}

func init() {
	rootCmd.AddCommand(definitionsCmd)
	definitionsCmd.Flags().StringVar(&defKind, "kind", "", "Filter by symbol kind (function, class, method, type)")
	definitionsCmd.Flags().StringVar(&defFile, "file", "", "Narrow to a specific file")
	definitionsCmd.Flags().IntVarP(&defLimit, "limit", "l", 10, "Maximum results")
	definitionsCmd.Flags().StringVarP(&defProject, "project", "p", "", "Project path (default: current directory)")
}

func runDefinitions(cmd *cobra.Command, args []string) error {
	symbol := args[0]

	projectPath := defProject
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

	// Resolve file path to absolute
	filePath := defFile
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

	results, err := apiClient.SearchDefinitions(absPath, symbol, defKind, filePath, defLimit)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if len(results.Results) == 0 {
		fmt.Printf("No definitions found for '%s'\n", symbol)
		return nil
	}

	fmt.Printf("Found %d definition(s) for '%s':\n\n", results.Total, symbol)

	for _, def := range results.Results {
		fmt.Printf("[%s] %s\n", def.Kind, def.Name)
		fmt.Printf("  %s:%d-%d (%s)\n", def.FilePath, def.Line, def.EndLine, def.Language)
		if def.Signature != nil && *def.Signature != "" {
			fmt.Printf("  Signature: %s\n", *def.Signature)
		}
		if def.ParentName != nil && *def.ParentName != "" {
			fmt.Printf("  Parent: %s\n", *def.ParentName)
		}
		fmt.Println()
	}

	return nil
}
