package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/spf13/cobra"
)

// workspaceCmd routes every workspace-scoped CLI verb. The user-facing
// argument grammar is name-first:
//
//	cix ws                          → list workspaces (default)
//	cix ws list                     → list workspaces (alternate)
//	cix ws create <name>            → create a workspace
//	cix ws <name>                   → describe workspace (list repos + status)
//	cix ws <name> list              → list repos in the workspace
//	cix ws <name> repos             → list repos (alias)
//	cix ws <name> describe          → describe (same as `cix ws <name>`)
//	cix ws <name> search <query>    → two-stage workspace search
//	cix ws <name> add <project…>    → link indexed project(s)
//	cix ws <name> remove <project…> → unlink project(s)
//	cix ws <name> rename <new>      → rename the workspace
//	cix ws <name> update [flags]    → patch name / description
//	cix ws <name> delete            → delete the workspace
//
// We deliberately roll the dispatch by hand instead of using cobra
// subcommands so the workspace NAME can sit in the first positional
// slot — cobra can't recognise a dynamic value (workspace name) as a
// command name. The trade-off is no auto-completion on `<name>`; in
// exchange the surface reads the way operators think about workspaces.
//
// `create` is a reserved leading keyword (like `list`): the workspace it
// makes does not exist yet, so it cannot slot into the name-first grammar.
// This shadows describing a workspace literally named "create".
var workspaceCmd = &cobra.Command{
	Use:     "workspace [name] [verb] [args...]",
	Aliases: []string{"ws"},
	Short:   "Cross-project semantic search + workspace management",
	Long: `Workspaces group repositories for cross-project semantic search.

Argument grammar — name-first:

  cix ws                          list workspaces visible to me
  cix ws list                     list workspaces (alternate form)
  cix ws create <name>            create a workspace (--description optional)
  cix ws <name>                   describe a workspace (repos + status)
  cix ws <name> list              list repos in <name>
  cix ws <name> repos             same as <name> list
  cix ws <name> search <query>    two-stage semantic search in <name>
  cix ws <name> add <project…>    link one or more indexed projects
  cix ws <name> remove <project…> unlink one or more projects
  cix ws <name> rename <new-name> rename the workspace
  cix ws <name> update [flags]    patch --name / --description
  cix ws <name> delete            delete the workspace (prompts; -y to skip)

A <project> is any indexed project, addressed by its absolute path, its
host_path (e.g. github.com/owner/repo@main), or its 16-hex path_hash — run
'cix list' to see them. 'add'/'remove' with no project default to the
current directory. Linking requires the project to be fully indexed.

Examples:
  cix ws
  cix ws create platform --description "core platform repos"
  cix ws platform add .
  cix ws platform add github.com/owner/repo@main /Users/me/svc
  cix ws platform remove a1b2c3d4e5f60718
  cix ws platform search "JWT validation"
  cix ws platform delete -y

Workspace identifiers accept the opaque id OR the (case-insensitive)
name. Registering a brand-new GitHub repo (clone + first index) and
GitHub token management still live at /dashboard on the cix-server;
'add' here links a project that is already indexed.`,
	Args: cobra.ArbitraryArgs,
	RunE: runWorkspace,
	// The verb errors returned by runWorkspace already carry an actionable
	// message (e.g. `unknown verb "x" — use one of: …`), and the best-effort
	// add/remove paths print per-project `✗` lines of their own. Dumping the
	// full usage block on top of that is pure noise, so suppress it. We also
	// silence cobra's own error print because root.go's Execute already prints
	// `Error: %v` — without this the message shows up twice.
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	wsJSON              bool
	wsVerbose           bool
	wsSearchTopProjects int
	wsSearchTopChunks   int
	// wsSearchMinScore < 0 means "unset" — omit the param so the server
	// applies its default (0.4). 0 is a valid explicit value (broad sweep).
	wsSearchMinScore float64
	// wsDescription / wsNewName back the create + update verbs. `update`
	// distinguishes "flag unset" from "flag set to empty" via
	// cmd.Flags().Changed, so an empty --description explicitly clears it.
	wsDescription string
	wsNewName     string
	// wsYes skips the delete confirmation prompt.
	wsYes bool
)

func init() {
	rootCmd.AddCommand(workspaceCmd)
	// Flags live on the parent — applies to every verb. `cobra` parses
	// flags before our manual routing runs, so `cix ws platform search
	// "..." --json` works regardless of where the user puts the flag.
	workspaceCmd.Flags().BoolVar(&wsJSON, "json", false, "Emit raw JSON instead of formatted output")
	workspaceCmd.Flags().BoolVarP(&wsVerbose, "verbose", "v", false, "Show extra columns on list / describe")
	workspaceCmd.Flags().IntVar(&wsSearchTopProjects, "top-projects", 10, "Search: top-N projects in the projects panel (1-50)")
	workspaceCmd.Flags().IntVar(&wsSearchTopChunks, "top-chunks", 20, "Search: top-K chunks returned overall (1-200)")
	workspaceCmd.Flags().Float64Var(&wsSearchMinScore, "min-score", -1, "Search: minimum relevance 0.0-1.0 (omit for server default 0.4; pass 0 for a broad cross-cutting sweep)")
	workspaceCmd.Flags().StringVar(&wsDescription, "description", "", "Description for create / update (empty on update clears it)")
	workspaceCmd.Flags().StringVar(&wsNewName, "name", "", "New name for update")
	workspaceCmd.Flags().BoolVarP(&wsYes, "yes", "y", false, "Skip the confirmation prompt when deleting")
}

func runWorkspace(cmd *cobra.Command, args []string) error {
	cli, err := getClient()
	if err != nil {
		return err
	}

	// `create` is a reserved leading keyword — see the workspaceCmd doc
	// comment. It must be handled before the name-first arms because the
	// workspace it names does not exist yet.
	if len(args) >= 1 && strings.EqualFold(args[0], "create") {
		return cmdCreateWorkspace(cli, args[1:])
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
	case "add", "link":
		return cmdAddProjects(cli, name, rest)
	case "remove", "unlink":
		return cmdRemoveProjects(cli, name, rest)
	case "rename":
		if len(rest) != 1 {
			return errors.New("rename needs exactly one new name (cix ws <name> rename <new-name>)")
		}
		return cmdRenameWorkspace(cli, name, rest[0])
	case "update":
		if len(rest) > 0 {
			return errors.New("update takes no positional arguments — use --name / --description")
		}
		return cmdUpdateWorkspace(cmd, cli, name)
	case "delete":
		if len(rest) > 0 {
			return errors.New("delete takes no extra arguments")
		}
		return cmdDeleteWorkspace(cli, name)
	default:
		return fmt.Errorf("unknown verb %q — use one of: list, repos, describe, search, add, remove, rename, update, delete", verb)
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
			// In verbose mode we follow each workspace with its project
			// count + indexed status. Two extra HTTP calls per
			// workspace; acceptable at typical scale (<10 workspaces).
			if pr, perr := cli.ListWorkspaceProjects(w.ID); perr == nil {
				indexed := 0
				for _, wp := range pr.Projects {
					if wp.Project.Status == "indexed" {
						indexed++
					}
				}
				fmt.Printf("       %d projects (%d indexed)\n", pr.Total, indexed)
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
	resp, err := cli.ListWorkspaceProjects(id)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(resp)
	}
	if resp.Total == 0 {
		fmt.Fprintln(os.Stderr, "no projects linked — add one at /dashboard/workspaces")
		return nil
	}
	for _, wp := range resp.Projects {
		p := wp.Project
		fmt.Printf("%s  %s\n", projectStatusBadge(p.Status), p.HostPath)
		if wsVerbose {
			fmt.Printf("       path_hash: %s\n", p.PathHash)
			if p.LastIndexedAt != nil {
				fmt.Printf("       last indexed: %s\n", p.LastIndexedAt.Format(time.RFC3339))
			}
			if len(p.Languages) > 0 {
				fmt.Printf("       languages: %s\n", strings.Join(p.Languages, ", "))
			}
			fmt.Printf("       linked: %s\n", wp.AddedAt.Format(time.RFC3339))
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
	projResp, err := cli.ListWorkspaceProjects(ws.ID)
	if err != nil {
		return err
	}

	if wsJSON {
		return emitJSON(map[string]any{
			"workspace": ws,
			"projects":  projResp.Projects,
			"total":     projResp.Total,
		})
	}

	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("  id: %s\n", ws.ID)
	if ws.Description != "" {
		fmt.Printf("  description: %s\n", ws.Description)
	}
	indexed := 0
	for _, wp := range projResp.Projects {
		if wp.Project.Status == "indexed" {
			indexed++
		}
	}
	fmt.Printf("  projects: %d (%d indexed)\n", projResp.Total, indexed)
	if projResp.Total == 0 {
		fmt.Fprintln(os.Stderr, "\n  (no projects linked — add at /dashboard/workspaces)")
		return nil
	}
	fmt.Println()
	for _, wp := range projResp.Projects {
		p := wp.Project
		fmt.Printf("  %s  %s\n", projectStatusBadgeShort(p.Status), p.HostPath)
		fmt.Printf("     path_hash: %s\n", p.PathHash)
		if p.LastIndexedAt != nil {
			fmt.Printf("     last indexed: %s\n", p.LastIndexedAt.Format(time.RFC3339))
		}
		fmt.Printf("     linked: %s\n", wp.AddedAt.Format(time.RFC3339))
	}
	return nil
}

// projectStatusBadge renders the long status form used by
// `cix ws <name> list`. The new wire enum (post-split) is:
//
//	created | indexing | indexed | error
//
// Unknown values fall through to the literal string so future enum
// additions render readably without crashing the CLI.
func projectStatusBadge(status string) string {
	switch status {
	case "indexed":
		return "✓ indexed"
	case "error":
		return "✗ error"
	case "indexing", "created":
		return "… " + status
	default:
		return status
	}
}

// projectStatusBadgeShort renders the single-glyph badge used by the
// describe view's per-project bullet list.
func projectStatusBadgeShort(status string) string {
	switch status {
	case "indexed":
		return "✓"
	case "error":
		return "✗"
	default:
		return "…"
	}
}

// ---------------------------------------------------------------------------
// `cix ws <name> search <query>`
// ---------------------------------------------------------------------------

func cmdWorkspaceSearch(cli *client.Client, identifier, query string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	var minScore *float64
	if wsSearchMinScore >= 0 {
		minScore = &wsSearchMinScore
	}
	resp, err := cli.WorkspaceSearch(id, query, wsSearchTopProjects, wsSearchTopChunks, minScore)
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

// ---------------------------------------------------------------------------
// `cix ws create <name>`
// ---------------------------------------------------------------------------

func cmdCreateWorkspace(cli *client.Client, args []string) error {
	if len(args) != 1 {
		return errors.New("create needs exactly one workspace name (cix ws create <name> [--description \"...\"])")
	}
	ws, err := cli.CreateWorkspace(args[0], wsDescription)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(ws)
	}
	fmt.Printf("created workspace %s  (%s)\n", ws.Name, ws.ID)
	fmt.Fprintf(os.Stderr, "add projects with: cix ws %s add <project>\n", ws.Name)
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name> rename <new>` / `<name> update [flags]`
// ---------------------------------------------------------------------------

func cmdRenameWorkspace(cli *client.Client, identifier, newName string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	ws, err := cli.UpdateWorkspace(id, &newName, nil)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(ws)
	}
	fmt.Printf("renamed workspace to %s  (%s)\n", ws.Name, ws.ID)
	return nil
}

// cmdUpdateWorkspace patches whichever of name/description was set on the
// command line. We rely on cmd.Flags().Changed rather than the zero value so
// `--description ""` clears the description instead of being treated as unset.
func cmdUpdateWorkspace(cmd *cobra.Command, cli *client.Client, identifier string) error {
	var namePtr, descPtr *string
	if cmd.Flags().Changed("name") {
		namePtr = &wsNewName
	}
	if cmd.Flags().Changed("description") {
		descPtr = &wsDescription
	}
	if namePtr == nil && descPtr == nil {
		return errors.New("update needs at least one of --name or --description")
	}
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	ws, err := cli.UpdateWorkspace(id, namePtr, descPtr)
	if err != nil {
		return err
	}
	if wsJSON {
		return emitJSON(ws)
	}
	fmt.Printf("updated workspace %s  (%s)\n", ws.Name, ws.ID)
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name> delete`
// ---------------------------------------------------------------------------

func cmdDeleteWorkspace(cli *client.Client, identifier string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	if !wsYes {
		if !isInteractive() {
			return fmt.Errorf("refusing to delete workspace %q without confirmation — pass --yes to proceed non-interactively", identifier)
		}
		fmt.Fprintf(os.Stderr, "Delete workspace %q? This removes the workspace and its project links (the projects themselves are kept). [y/N] ", identifier)
		if !readAffirmative() {
			fmt.Fprintln(os.Stderr, "aborted")
			return nil
		}
	}
	if err := cli.DeleteWorkspace(id); err != nil {
		return err
	}
	fmt.Printf("deleted workspace %s\n", identifier)
	return nil
}

// ---------------------------------------------------------------------------
// `cix ws <name> add <project…>` / `<name> remove <project…>`
// ---------------------------------------------------------------------------

func cmdAddProjects(cli *client.Client, identifier string, projectArgs []string) error {
	return mutateProjects(cli, identifier, projectArgs, "add")
}

func cmdRemoveProjects(cli *client.Client, identifier string, projectArgs []string) error {
	return mutateProjects(cli, identifier, projectArgs, "remove")
}

// mutateProjects links or unlinks the given projects. It resolves each
// identifier to a known project's path_hash first (so a typo yields an
// actionable local error before any HTTP call), then applies the op. It is
// best-effort: one failing project is reported to stderr and does not abort
// the others. Returns a non-nil error when at least one op failed so the
// process exit code reflects it.
func mutateProjects(cli *client.Client, identifier string, projectArgs []string, op string) error {
	id, err := resolveWorkspaceID(cli, identifier)
	if err != nil {
		return err
	}
	targets, err := projectTargets(projectArgs)
	if err != nil {
		return err
	}
	projList, err := cli.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	failures := 0
	for _, t := range targets {
		hash, hostPath, rerr := resolveProjectHash(projList, t)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", t, rerr)
			failures++
			continue
		}
		var opErr error
		var verb string
		if op == "add" {
			opErr = cli.LinkProjectToWorkspace(id, hash)
			verb = "added"
		} else {
			opErr = cli.UnlinkProjectFromWorkspace(id, hash)
			verb = "removed"
		}
		if opErr != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", hostPath, opErr)
			failures++
			continue
		}
		fmt.Printf("✓ %s %s\n", verb, hostPath)
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d project(s) failed to %s", failures, len(targets), op)
	}
	return nil
}

// projectTargets returns the project identifiers to act on. An empty list
// defaults to the current working directory (absolute), mirroring how
// `cix init` operates on the cwd when given no path.
func projectTargets(args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}
	return []string{cwd}, nil
}

// resolveProjectHash maps a user-supplied project identifier to the
// path_hash of a server-registered project, returning the hash and the
// project's host_path for display. Accepted forms, in priority order:
//
//  1. an exact 16-hex path_hash
//  2. an exact host_path (e.g. github.com/owner/repo@main, or an abs path)
//  3. a filesystem path (".", relative, ~-expanded) → abs → host_path match
//
// Resolving against the live project list (rather than blindly hashing the
// input) lets the CLI reject unknown projects locally with an actionable
// hint, and sidesteps the local-vs-external hash-derivation split.
func resolveProjectHash(projects []client.Project, identifier string) (hash, hostPath string, err error) {
	// 1. exact path_hash.
	if isHex16(identifier) {
		for _, p := range projects {
			if p.PathHash == identifier {
				return p.PathHash, p.HostPath, nil
			}
		}
	}
	// 2. exact host_path — external projects (github.com/owner/repo@branch)
	//    store their identifier verbatim as host_path.
	for _, p := range projects {
		if p.HostPath == identifier {
			return p.PathHash, p.HostPath, nil
		}
	}
	// 3. filesystem path → derive the local project's path_hash the same way
	//    the server + client do (sha1 of "local:{machine_id}:{abs_path}") and
	//    match on path_hash. Local projects store host_path as that full
	//    identity key rather than the bare path, so a host_path string compare
	//    won't hit — the derived hash is the reliable join.
	if abs, aerr := filepath.Abs(identifier); aerr == nil {
		derived := client.EncodeProjectPath(abs)
		for _, p := range projects {
			if p.PathHash == derived {
				return p.PathHash, p.HostPath, nil
			}
		}
	}
	return "", "", fmt.Errorf("no indexed project matches %q — run `cix list` to see known projects", identifier)
}

// isHex16 reports whether s is exactly 16 lowercase hex chars (a path_hash).
func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// isInteractive reports whether stdin is a TTY. Dependency-free char-device
// check (mirrors indexer.isTerminal) so destructive verbs can prompt when a
// human is driving and hard-fail when the input is piped.
func isInteractive() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// readAffirmative reads one line from stdin and reports whether it is a
// yes/y (case-insensitive). Anything else — including a read error/EOF — is
// treated as a no.
func readAffirmative() bool {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func renderSearch(resp *client.WorkspaceSearchResponse) error {
	switch resp.Status {
	case "empty":
		fmt.Fprintln(os.Stderr, "no chunks matched the query")
		return nil
	case "partial_failure":
		fmt.Fprintln(os.Stderr, "at least one repo errored — results below are incomplete; check server logs")
	}

	if len(resp.StaleFTSRepos) > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %d repo(s) were indexed before BM25 was enabled; hybrid degrades to dense-only for them.\n"+
				"         reindex to fix: ", len(resp.StaleFTSRepos))
		paths := make([]string, len(resp.StaleFTSRepos))
		for i, s := range resp.StaleFTSRepos {
			paths[i] = s.ProjectPath
		}
		fmt.Fprintln(os.Stderr, strings.Join(paths, ", "))
		fmt.Fprintln(os.Stderr)
	}

	if len(resp.Projects) > 0 {
		fmt.Println("Top projects:")
		for _, p := range resp.Projects {
			label := p.Label
			if label == "" {
				label = p.ProjectPath
			}
			fmt.Printf("  [%.3f] %s  — %d hits · bm25 %.3f · dense %.3f · %s\n",
				p.ProjectScore, label, p.NumHits, p.BM25Score, p.DenseScore, p.ProjectPath)
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
