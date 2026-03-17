package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all indexed projects",
	Long:  `Display a list of all projects registered in the index.`,
	RunE:  runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	// Get API client
	apiClient, err := getClient()
	if err != nil {
		return err
	}

	// List projects
	projects, err := apiClient.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects found. Use 'cix init' to add a project.")
		return nil
	}

	// Print projects
	fmt.Printf("Found %d project(s):\n\n", len(projects))

	for i, project := range projects {
		// Status icon
		statusIcon := getStatusIcon(project.Status)

		// Print header
		fmt.Printf("%d. [%s] %s\n", i+1, statusIcon, project.HostPath)

		// Stats
		fmt.Printf("   Status: %s | Files: %d | Chunks: %d | Symbols: %d\n",
			project.Status,
			project.Stats.TotalFiles,
			project.Stats.TotalChunks,
			project.Stats.TotalSymbols)

		// Languages
		if len(project.Languages) > 0 {
			langs := strings.Join(project.Languages, ", ")
			if len(langs) > 60 {
				langs = langs[:60] + "..."
			}
			fmt.Printf("   Languages: %s\n", langs)
		}

		// Last indexed
		if project.LastIndexedAt != nil {
			fmt.Printf("   Last indexed: %s\n", project.LastIndexedAt.Format("2006-01-02 15:04:05"))
		} else {
			fmt.Printf("   Last indexed: never\n")
		}

		fmt.Println()
	}

	return nil
}

func getStatusIcon(status string) string {
	switch status {
	case "indexed":
		return "✓"
	case "indexing":
		return "⏳"
	case "created":
		return "○"
	case "error":
		return "✗"
	default:
		return "?"
	}
}
