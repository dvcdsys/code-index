package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/spf13/cobra"
)

// workspaceCmd is the parent for every workspace-scoped CLI verb. Today
// the verbs are `list` and `search` — `add`, `repos`, `tokens` can land
// later without changing the surface.
var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "Cross-project semantic search via workspaces",
	Long: `Workspaces group GitHub repositories for cross-project semantic search.

Repository attachment, GitHub token management, and the dashboard view
all live at /dashboard on the cix-server (see cix-server's
CIX_WORKSPACES_ENABLED). This CLI focuses on the search-and-go path:
` + "`cix workspace list`" + ` to see what's available, then
` + "`cix workspace search`" + ` to query a specific workspace from the terminal.`,
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspaces visible on the cix-server",
	RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := getClient()
		if err != nil {
			return err
		}
		resp, err := cli.ListWorkspaces()
		if err != nil {
			return err
		}
		if resp.Total == 0 {
			fmt.Fprintln(os.Stderr, "no workspaces — create one at /dashboard/workspaces")
			return nil
		}
		for _, w := range resp.Workspaces {
			line := w.ID + "  " + w.Name
			if w.Description != "" {
				line += "  — " + w.Description
			}
			fmt.Println(line)
		}
		return nil
	},
}

var (
	wsSearchTopCommunities int
	wsSearchTopChunks      int
	wsSearchJSON           bool
)

var workspaceSearchCmd = &cobra.Command{
	Use:   "search <workspace-id-or-name> <query>",
	Short: "Two-stage semantic search across a workspace",
	Long: `Run a natural-language query against a workspace.

The server runs a two-stage search:
  1) Match the query embedding against the workspace's centroid index
     (Louvain community embeddings) → top-N communities.
  2) Within those communities, rank chunks from every member repo.

Workspaces with no centroid index yet (newly-created, or all repos
still indexing) return an empty result with a hint — wait for the
debounced compute_workspace_communities job to finish (30s after the
last index_repo).

Examples:
  cix workspace search platform "JWT validation"
  cix workspace search platform "how do services authenticate users"
  cix workspace search platform "rate limiting" --top-communities 8 --top-chunks 30
  cix workspace search platform "config loading" --json`,
	Args: cobra.MinimumNArgs(2),
	RunE: runWorkspaceSearch,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceSearchCmd)
	workspaceSearchCmd.Flags().IntVar(&wsSearchTopCommunities, "top-communities", 5, "Top-N communities to fan out (1–50)")
	workspaceSearchCmd.Flags().IntVar(&wsSearchTopChunks, "top-chunks", 20, "Top-K chunks returned overall (1–200)")
	workspaceSearchCmd.Flags().BoolVar(&wsSearchJSON, "json", false, "Emit raw JSON instead of formatted output")
}

func runWorkspaceSearch(cmd *cobra.Command, args []string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}
	identifier := args[0]
	query := strings.Join(args[1:], " ")

	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}

	resp, err := cli.WorkspaceSearch(id, query, wsSearchTopCommunities, wsSearchTopChunks)
	if err != nil {
		return err
	}
	if wsSearchJSON {
		return emitJSON(resp)
	}
	return renderSearch(resp)
}

// resolveWorkspaceID lets callers pass either the opaque id OR a
// workspace name. ListWorkspaces is one call regardless and we cache the
// hit so chained CLI calls aren't slower than necessary.
func resolveWorkspaceID(cli *client.Client, identifier string) (string, error) {
	list, err := cli.ListWorkspaces()
	if err != nil {
		return "", err
	}
	var match *client.Workspace
	for i := range list.Workspaces {
		w := &list.Workspaces[i]
		if w.ID == identifier || strings.EqualFold(w.Name, identifier) {
			match = w
			break
		}
	}
	if match == nil {
		return "", fmt.Errorf("workspace %q not found (run `cix workspace list`)", identifier)
	}
	return match.ID, nil
}

func renderSearch(resp *client.WorkspaceSearchResponse) error {
	switch resp.Status {
	case "communities_not_built":
		fmt.Fprintln(os.Stderr, "workspace has no centroid index yet — add a repo or wait for the debounced rebuild")
		return nil
	case "empty":
		fmt.Fprintln(os.Stderr, "no chunks matched the query")
		return nil
	}

	if len(resp.Communities) > 0 {
		fmt.Println("Top communities:")
		for _, c := range resp.Communities {
			label := c.Label
			if label == "" {
				label = "(unlabelled)"
			}
			fmt.Printf("  [%.3f] %s  — %d members across %s\n",
				c.Score, label, c.MemberCount, strings.Join(c.ProjectPaths, ", "))
		}
		fmt.Println()
	}
	fmt.Println("Top chunks:")
	for _, c := range resp.Chunks {
		head := fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
		fmt.Printf("  [%.3f] %s\n", c.Score, head)
		fmt.Printf("         project: %s\n", c.ProjectPath)
		if c.SymbolName != "" {
			fmt.Printf("         symbol:  %s\n", c.SymbolName)
		}
		if c.CommunityLabel != "" {
			fmt.Printf("         community: %s\n", c.CommunityLabel)
		}
		fmt.Println()
	}
	return nil
}

// emitJSON writes a Go value as indented JSON to stdout.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
