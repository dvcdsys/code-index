package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
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
			if dir.Path == "" {
				continue
			}
			displayPath := dir.Path
			if relPath, relErr := filepath.Rel(absPath, dir.Path); relErr == nil {
				displayPath = relPath
			}
			fmt.Printf("  %s/ (%d files)\n", displayPath, dir.FileCount)
		}
		fmt.Println()
	}

	// Top symbols — grouped by language so it's obvious which symbols come
	// from which file type. Mixed lists used to be hard to scan ("why is
	// `s` showing up as a function?" — turns out: minified JS bundle).
	if len(summary.RecentSymbols) > 0 {
		fmt.Println("Top symbols:")
		printSymbolsByLanguage(summary.RecentSymbols)
	}

	return nil
}

// printSymbolsByLanguage groups symbols by their language and renders each
// group under a `<lang> (N):` header. Languages are sorted alphabetically;
// within each group, original order is preserved (the server already returns
// them ranked). Symbols with empty Language are bucketed under "(unknown)".
func printSymbolsByLanguage(syms []client.RecentSymbolEntry) {
	groups := map[string][]client.RecentSymbolEntry{}
	for _, sym := range syms {
		if sym.Name == "" {
			continue
		}
		lang := sym.Language
		if lang == "" {
			lang = "(unknown)"
		}
		groups[lang] = append(groups[lang], sym)
	}

	langs := make([]string, 0, len(groups))
	for l := range groups {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	for _, lang := range langs {
		entries := groups[lang]
		fmt.Printf("  %s (%d):\n", lang, len(entries))
		for _, sym := range entries {
			fmt.Printf("    [%s] %s\n", sym.Kind, sym.Name)
		}
	}
}
