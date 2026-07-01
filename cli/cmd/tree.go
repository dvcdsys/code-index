package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

var (
	treeProject string
	treeName    string
)

// treeCmd represents the tree command.
var treeCmd = &cobra.Command{
	Use:   "tree [dir]",
	Short: "List a directory (one level) in an external project",
	Long: `List the immediate entries of a directory in the server-side checkout of an
EXTERNAL (GitHub-backed) project — ls-like, one level, no recursion. Omit the
directory for the repository root.

Only works for external projects (the server keeps their files on disk). For a
LOCAL project, list it directly with your own tools.

Examples:
  cix tree -n github.com/owner/repo@main
  cix tree internal/httpapi -n github.com/owner/repo@main`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTree,
}

func init() {
	rootCmd.AddCommand(treeCmd)
	treeCmd.Flags().StringVarP(&treeProject, "project", "p", "", "Project path (default: current directory)")
	treeCmd.Flags().StringVarP(&treeName, "name", "n", "", "Project ID (exact match against `cix list`). Mutually exclusive with -p.")
	treeCmd.MarkFlagsMutuallyExclusive("project", "name")
}

func runTree(cmd *cobra.Command, args []string) error {
	dir := ""
	if len(args) == 1 {
		dir = args[0]
	}

	apiClient, err := getClient()
	if err != nil {
		return err
	}

	absPath, err := resolveProjectArg(treeProject, treeName, apiClient)
	if err != nil {
		return err
	}

	result, err := apiClient.ListDir(absPath, dir)
	if err != nil {
		return fmt.Errorf("list directory failed: %w", err)
	}

	shown := dir
	if shown == "" {
		shown = "."
	}
	fmt.Printf("%s\n", shown)

	if len(result.Entries) == 0 {
		fmt.Println("(empty)")
		return nil
	}

	// Dirs first, then files (server already sorts this way; keep it stable).
	sort.SliceStable(result.Entries, func(i, j int) bool {
		di, dj := result.Entries[i].Type == "dir", result.Entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return result.Entries[i].Name < result.Entries[j].Name
	})

	for _, e := range result.Entries {
		if e.Type == "dir" {
			fmt.Printf("  %s/\n", e.Name)
			continue
		}
		size := ""
		if e.Size != nil {
			size = fmt.Sprintf("  (%s)", humanSize(*e.Size))
		}
		fmt.Printf("  %s%s\n", e.Name, size)
	}
	if result.Truncated {
		fmt.Fprintln(os.Stderr, "\n(truncated — directory has more entries than the listing cap)")
	}
	return nil
}

// humanSize renders a byte count compactly (B / KB / MB).
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
