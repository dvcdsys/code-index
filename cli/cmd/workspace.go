package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/spf13/cobra"
)

// workspaceCmd routes every workspace-scoped CLI verb. The user-facing
// argument grammar is name-first:
//
//	cix ws                          → list workspaces (default)
//	cix ws list                     → list workspaces (alternate)
//	cix ws <name>                   → describe workspace (list repos + status)
//	cix ws <name> list              → list repos in the workspace
//	cix ws <name> repos             → list repos (alias)
//	cix ws <name> describe          → describe (same as `cix ws <name>`)
//	cix ws <name> search <query>    → two-stage workspace search
//
// We deliberately roll the dispatch by hand instead of using cobra
// subcommands so the workspace NAME can sit in the first positional
// slot — cobra can't recognise a dynamic value (workspace name) as a
// command name. The trade-off is no auto-completion on `<name>`; in
// exchange the surface reads the way operators think about workspaces.
var workspaceCmd = &cobra.Command{
	Use:     "workspace [name] [verb] [args...]",
	Aliases: []string{"ws"},
	Short:   "Cross-project semantic search via workspaces",
	Long: `Workspaces group GitHub repositories for cross-project semantic search.

Argument grammar — name-first:

  cix ws                          list workspaces visible to me
  cix ws list                     list workspaces (alternate form)
  cix ws <name>                   describe a workspace (repos + status)
  cix ws <name> list              list repos in <name>
  cix ws <name> repos             same as <name> list
  cix ws <name> search <query>    two-stage semantic search in <name>

Examples:
  cix ws
  cix ws platform
  cix ws platform list
  cix ws platform search "JWT validation"
  cix ws platform search "rate limiting" --top-communities 8 --top-chunks 30 --json

Workspace identifiers accept the opaque id OR the (case-insensitive)
name. Repository attachment, GitHub token management, and the
detailed dashboard view all live at /dashboard on the cix-server.`,
	Args: cobra.ArbitraryArgs,
	RunE: runWorkspace,
}

var (
	wsJSON                bool
	wsVerbose             bool
	wsSearchTopCommunities int
	wsSearchTopChunks      int
)

func init() {
	rootCmd.AddCommand(workspaceCmd)
	// Flags live on the parent — applies to every verb. `cobra` parses
	// flags before our manual routing runs, so `cix ws platform search
	// "..." --json` works regardless of where the user puts the flag.
	workspaceCmd.Flags().BoolVar(&wsJSON, "json", false, "Emit raw JSON instead of formatted output")
	workspaceCmd.Flags().BoolVarP(&wsVerbose, "verbose", "v", false, "Show extra columns on list / describe")
	workspaceCmd.Flags().IntVar(&wsSearchTopCommunities, "top-communities", 5, "Search: top-N centroids to fan out (1-50)")
	workspaceCmd.Flags().IntVar(&wsSearchTopChunks, "top-chunks", 20, "Search: top-K chunks returned overall (1-200)")
}

func runWorkspace(cmd *cobra.Command, args []string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	switch {
	case len(args) == 0:
		return cmdListWorkspaces(cli)
	case len(args) == 1 && strings.EqualFold(args[0], "list"):
		return cmdListWorkspaces(cli)
	case len(args) == 1:
		// `cix ws <name>` — describe.
		return cmdDescribeWorkspace(cli, args[0])
	}

	// 2+ args. First is the workspace name, second the verb.
	name := args[0]
	verb := strings.ToLower(args[1])
	rest := args[2:]

	switch verb {
	case "list", "repos":
		if len(rest) > 0 {
			return fmt.Errorf("%q takes no extra arguments", verb)
		}
		return cmdListRepos(cli, name)
	case "describe":
		if len(rest) > 0 {
			return fmt.Errorf("describe takes no extra arguments")
		}
		return cmdDescribeWorkspace(cli, name)
	case "search":
		if len(rest) == 0 {
			return errors.New("search needs a query string (cix ws <name> search \"<query>\")")
		}
		query := strings.Join(rest, " ")
		return cmdWorkspaceSearch(cli, name, query)
	default:
		return fmt.Errorf("unknown verb %q — use one of: list, repos, describe, search", verb)
	}
}

// ---------------------------------------------------------------------------
// `cix ws list`
// ---------------------------------------------------------------------------

func cmdListWorkspaces(cli *client.Client) error {
	resp, err := cli.ListWorkspaces()
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(resp)
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
		if wsVerbose {
			// In verbose mode we follow each workspace with its repo
			// count + indexed status. Two extra HTTP calls per
			// workspace; acceptable at typical scale (<10 workspaces).
			if reposResp, rerr := cli.ListWorkspaceRepos(w.ID); rerr == nil {
				indexed := 0
				for _, r := range reposResp.Repos {
					if r.Status == "indexed" {
						indexed++
					}
				}
				fmt.Printf("       %d repos (%d indexed)\n", reposResp.Total, indexed)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name> list` / `<name> repos`
// ---------------------------------------------------------------------------

func cmdListRepos(cli *client.Client, identifier string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	resp, err := cli.ListWorkspaceRepos(id)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(resp)
	}
	if resp.Total == 0 {
		fmt.Fprintln(os.Stderr, "no repos attached — add one at /dashboard/workspaces")
		return nil
	}
	for _, r := range resp.Repos {
		statusBadge := r.Status
		switch r.Status {
		case "indexed":
			statusBadge = "✓ indexed"
		case "failed":
			statusBadge = "✗ failed"
		case "cloning", "indexing", "pending":
			statusBadge = "… " + r.Status
		}
		fmt.Printf("%s  %s@%s\n", statusBadge, r.GitHubURL, r.Branch)
		if wsVerbose {
			fmt.Printf("       project: %s\n", r.ProjectPath)
			if r.LastIndexedAt != nil {
				fmt.Printf("       last indexed: %s\n", *r.LastIndexedAt)
			}
			if r.LastError != nil && *r.LastError != "" {
				fmt.Printf("       last error: %s\n", *r.LastError)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name>` / `<name> describe`
// ---------------------------------------------------------------------------

func cmdDescribeWorkspace(cli *client.Client, identifier string) error {
	list, err := cli.ListWorkspaces()
	if err != nil {
		return err
	}
	var ws *client.Workspace
	for i := range list.Workspaces {
		w := &list.Workspaces[i]
		if w.ID == identifier || strings.EqualFold(w.Name, identifier) {
			ws = w
			break
		}
	}
	if ws == nil {
		return fmt.Errorf("workspace %q not found (run `cix ws list`)", identifier)
	}
	reposResp, err := cli.ListWorkspaceRepos(ws.ID)
	if err != nil {
		return err
	}

	if wsJSON {
		return emitJSON(map[string]any{
			"workspace": ws,
			"repos":     reposResp.Repos,
			"total":     reposResp.Total,
		})
	}

	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("  id: %s\n", ws.ID)
	if ws.Description != "" {
		fmt.Printf("  description: %s\n", ws.Description)
	}
	indexed := 0
	for _, r := range reposResp.Repos {
		if r.Status == "indexed" {
			indexed++
		}
	}
	fmt.Printf("  repos: %d (%d indexed)\n", reposResp.Total, indexed)
	if reposResp.Total == 0 {
		fmt.Fprintln(os.Stderr, "\n  (no repos attached — add at /dashboard/workspaces)")
		return nil
	}
	fmt.Println()
	for _, r := range reposResp.Repos {
		statusBadge := r.Status
		switch r.Status {
		case "indexed":
			statusBadge = "✓"
		case "failed":
			statusBadge = "✗"
		default:
			statusBadge = "…"
		}
		fmt.Printf("  %s  %s@%s\n", statusBadge, r.GitHubURL, r.Branch)
		fmt.Printf("     project: %s\n", r.ProjectPath)
		if r.LastIndexedAt != nil {
			fmt.Printf("     last indexed: %s\n", *r.LastIndexedAt)
		}
		if r.LastError != nil && *r.LastError != "" {
			fmt.Printf("     last error: %s\n", *r.LastError)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name> search <query>`
// ---------------------------------------------------------------------------

func cmdWorkspaceSearch(cli *client.Client, identifier, query string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	resp, err := cli.WorkspaceSearch(id, query, wsSearchTopCommunities, wsSearchTopChunks)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(resp)
	}
	return renderSearch(resp)
}

// resolveWorkspaceID maps a user-typed identifier (id or name) to the
// canonical opaque id used by the API. One ListWorkspaces call regardless
// — keeps the surface uniform across `list`, `describe`, `search`.
func resolveWorkspaceID(cli *client.Client, identifier string) (string, error) {
	list, err := cli.ListWorkspaces()
	if err != nil {
		return "", err
	}
	for i := range list.Workspaces {
		w := &list.Workspaces[i]
		if w.ID == identifier || strings.EqualFold(w.Name, identifier) {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("workspace %q not found (run `cix ws list`)", identifier)
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
