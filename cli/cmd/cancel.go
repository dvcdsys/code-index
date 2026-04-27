package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var cancelProject string

var cancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "Cancel an active indexing session",
	Long: `Cancel any in-flight indexing session for a project.

Useful when a previous 'cix reindex' was interrupted by a network issue or
client-side timeout but the server is still holding a session lock and
returning 409 Conflict on subsequent /index/begin attempts.

Idempotent: succeeds (no-op) when no session is active.

Examples:
  cix cancel
  cix cancel -p /path/to/project`,
	RunE: runCancel,
}

func init() {
	rootCmd.AddCommand(cancelCmd)
	cancelCmd.Flags().StringVarP(&cancelProject, "project", "p", "", "Project path (default: current directory)")
}

func runCancel(cmd *cobra.Command, args []string) error {
	projectPath := cancelProject
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

	resp, err := apiClient.CancelIndex(absPath)
	if err != nil {
		return fmt.Errorf("cancel: %w", err)
	}

	if resp.Cancelled {
		fmt.Printf("✓ Cancelled active indexing session for %s\n", absPath)
	} else {
		fmt.Printf("No active session for %s (nothing to cancel)\n", absPath)
	}
	return nil
}
