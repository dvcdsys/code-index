package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Default result limits when a tool call omits one. Tuned for agent context
// budgets: search returns whole-file groups (heavy), so it stays small;
// metadata-only tools can afford more rows.
const (
	mcpDefaultSearchLimit     = 5
	mcpDefaultDefinitionLimit = 10
	mcpDefaultReferenceLimit  = 20
	mcpDefaultSymbolLimit     = 20
	mcpDefaultFileLimit       = 20
	mcpDefaultTopProjects     = 5
	mcpDefaultTopChunks       = 10
)

// ---- tool input schemas -----------------------------------------------------
//
// The model is server-centric AND multi-server. Every tool that hits a server
// takes an optional "server" (a name from cix_list_servers; omit for the
// default). Scope within a server is always explicit: per-project tools need a
// "project" (a host_path from cix_list_projects); the cross-project tool needs a
// "workspace" (id or name from cix_list_workspaces). Nothing is inferred from
// the environment. Fields tagged with `,omitempty` are optional; plain-tagged
// fields are required in the generated JSON Schema. The `jsonschema` tag becomes
// the property description the model sees.

type mcpServerArg struct {
	Server string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
}

type mcpProjectArg struct {
	Server  string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project string `json:"project" jsonschema:"host_path of an indexed repository, from cix_list_projects. Required — there is no default project."`
}

type mcpWorkspaceArg struct {
	Server    string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Workspace string `json:"workspace" jsonschema:"id or name of a workspace, from cix_list_workspaces. Required."`
}

type mcpSearchInput struct {
	Server   string   `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project  string   `json:"project" jsonschema:"host_path of the indexed repository to search, from cix_list_projects. Required."`
	Query    string   `json:"query" jsonschema:"natural-language description of the code to find. Search is by meaning, not exact text — 'JWT validation' finds verifyToken even without that phrase."`
	Limit    int      `json:"limit,omitempty" jsonschema:"maximum number of files to return (default 5)."`
	Lang     []string `json:"lang,omitempty" jsonschema:"restrict to these languages, e.g. go, python, typescript."`
	In       []string `json:"in,omitempty" jsonschema:"restrict the search to these paths; relative paths are resolved against the repository root."`
	Exclude  []string `json:"exclude,omitempty" jsonschema:"drop these paths from the results; relative paths are resolved against the repository root."`
	MinScore *float64 `json:"min_score,omitempty" jsonschema:"minimum relevance, 0.0-1.0. Omit for the default 0.4; lower to 0.2 for very specific or long-tail queries, or 0 to let the server decide."`
}

type mcpDefinitionsInput struct {
	Server  string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project string `json:"project" jsonschema:"host_path of the indexed repository, from cix_list_projects. Required."`
	Symbol  string `json:"symbol" jsonschema:"exact symbol name to locate the definition of."`
	Kind    string `json:"kind,omitempty" jsonschema:"restrict to a symbol kind: function, class, method, or type."`
	File    string `json:"file,omitempty" jsonschema:"restrict to definitions inside this file or directory."`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results (default 10)."`
}

type mcpReferencesInput struct {
	Server  string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project string `json:"project" jsonschema:"host_path of the indexed repository, from cix_list_projects. Required."`
	Symbol  string `json:"symbol" jsonschema:"exact symbol name to find references/usages of."`
	File    string `json:"file,omitempty" jsonschema:"restrict to references inside this file or directory."`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results (default 20)."`
}

type mcpSymbolsInput struct {
	Server  string   `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project string   `json:"project" jsonschema:"host_path of the indexed repository, from cix_list_projects. Required."`
	Query   string   `json:"query" jsonschema:"symbol name (or fragment) to search for."`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"restrict to these symbol kinds: function, class, method, type."`
	Limit   int      `json:"limit,omitempty" jsonschema:"maximum number of results (default 20)."`
}

type mcpFilesInput struct {
	Server  string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Project string `json:"project" jsonschema:"host_path of the indexed repository, from cix_list_projects. Required."`
	Pattern string `json:"pattern" jsonschema:"path substring or pattern to match file paths against."`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of results (default 20)."`
}

type mcpWorkspaceSearchInput struct {
	Server      string `json:"server,omitempty" jsonschema:"name of the cix server to target, from cix_list_servers. Omit to use the default server."`
	Workspace   string `json:"workspace" jsonschema:"id or name of the workspace to search across, from cix_list_workspaces. Required."`
	Query       string   `json:"query" jsonschema:"natural-language description of the code to find across all repositories in the workspace."`
	TopProjects int      `json:"top_projects,omitempty" jsonschema:"maximum repositories to rank (default 5)."`
	TopChunks   int      `json:"top_chunks,omitempty" jsonschema:"maximum code chunks to return across all repositories (default 10)."`
	MinScore    *float64 `json:"min_score,omitempty" jsonschema:"minimum relevance, 0.0-1.0. Omit for the default 0.4; pass 0 for an intentional cross-cutting sweep that should surface long-tail, low-score repos."`
}

// registerCixTools wires the cix server registry into MCP tools. The surface is
// server-centric and multi-server: cix_list_servers picks which server; then
// discovery tools (list workspaces/projects), one cross-project research tool
// (workspace search), and per-project drill-down tools that each take an
// explicit project. Tool-level failures are returned as IsError results
// (visible to the model) rather than protocol errors.
func registerCixTools(server *mcp.Server, reg *serverRegistry) {
	// ── Servers ──────────────────────────────────────────────────────────────
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_list_servers",
		Description: "List the cix servers this connection can reach. cix is multi-server; pass a returned name as the 'server' argument to any other tool, or omit it to use the default. Most setups have just one server.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return mcpText(formatServers(mcpListServers())), nil, nil
	})

	// ── Discovery ────────────────────────────────────────────────────────────
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_list_projects",
		Description: "List every repository indexed by a cix server, with status. Use a returned host_path as the 'project' argument to the per-project tools. Start here when you need to scope work to one repo.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpServerArg) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		projects, err := c.ListProjects()
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatProjects(projects)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_list_workspaces",
		Description: "List workspaces on a cix server. A workspace groups several repositories for cross-project research. Use a returned id or name as the 'workspace' argument to cix_workspace_search. Start here for broad, multi-repo research.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpServerArg) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		resp, err := c.ListWorkspaces()
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatWorkspaces(resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_list_workspace_projects",
		Description: "List the repositories in a workspace, with each one's indexing status and host_path. Call this before cix_workspace_search to see what the workspace covers, and to get the host_path values to pass as 'project' when drilling into a single repo.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpWorkspaceArg) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		id, err := mcpResolveWorkspace(c, in.Workspace)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		resp, err := c.ListWorkspaceProjects(id)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatWorkspaceProjects(resp)), nil, nil
	})

	// ── Cross-project research ───────────────────────────────────────────────
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_workspace_search",
		Description: "Semantic search across ALL repositories in a workspace at once. Ranks the most relevant repos and returns matching code chunks from across them. This is the tool for broad, cross-repository research — prefer it over per-project search when the question spans multiple repos or you do not yet know which repo holds the answer.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpWorkspaceSearchInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		id, err := mcpResolveWorkspace(c, in.Workspace)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		topProjects := in.TopProjects
		if topProjects <= 0 {
			topProjects = mcpDefaultTopProjects
		}
		topChunks := in.TopChunks
		if topChunks <= 0 {
			topChunks = mcpDefaultTopChunks
		}
		resp, err := c.WorkspaceSearch(id, in.Query, topProjects, topChunks, in.MinScore)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatWorkspaceSearch(resp)), nil, nil
	})

	// ── Per-project drill-down (explicit project) ────────────────────────────
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_search",
		Description: "Semantic code search within ONE repository: find code by meaning. Requires an explicit 'project'. Use this when you know which repo to look in; use cix_workspace_search when the question spans repos.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSearchInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = mcpDefaultSearchLimit
		}
		// Mirror the `cix search` CLI: an omitted min_score applies the shared
		// default floor (searchDefaultMinScore). An explicit value — including 0
		// to defer to the server — is passed through verbatim.
		minScore := searchDefaultMinScore
		if in.MinScore != nil {
			minScore = *in.MinScore
		}
		resp, err := c.Search(proj, in.Query, client.SearchOptions{
			Limit:     limit,
			Languages: in.Lang,
			Paths:     mcpResolveScopePaths(proj, in.In),
			Excludes:  mcpResolveScopePaths(proj, in.Exclude),
			MinScore:  minScore,
		})
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatSearch(proj, resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_definitions",
		Description: "Go-to-definition within one repository: locate where a symbol is defined. Metadata only (file, line, signature) — cheap on tokens. Requires an explicit 'project'.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDefinitionsInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = mcpDefaultDefinitionLimit
		}
		resp, err := c.SearchDefinitions(proj, in.Symbol, in.Kind, in.File, limit)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatDefinitions(proj, in.Symbol, resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_references",
		Description: "Find references within one repository: locate every place a symbol is used. Metadata only (file, line) — cheap on tokens. Requires an explicit 'project'.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpReferencesInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = mcpDefaultReferenceLimit
		}
		resp, err := c.SearchReferences(proj, in.Symbol, in.File, limit)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatReferences(proj, in.Symbol, resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_symbols",
		Description: "Find symbols by name (functions, classes, methods, types) within one repository — a fuzzy name match returning metadata only (file, line, signature). Use when you have a partial or approximate name; use cix_definitions for an exact known name, or cix_search to find code by meaning. Requires an explicit 'project'.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSymbolsInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = mcpDefaultSymbolLimit
		}
		resp, err := c.SearchSymbols(proj, in.Query, in.Kinds, limit)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatSymbols(proj, in.Query, resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_files",
		Description: "Find files by path or filename pattern within one repository — matches the file PATH, not its contents. Cheap, metadata-only; use it to locate a file when you know part of its name or directory. Requires an explicit 'project'.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFilesInput) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = mcpDefaultFileLimit
		}
		resp, err := c.SearchFiles(proj, in.Pattern, limit)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatFiles(proj, in.Pattern, resp)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cix_summary",
		Description: "Project overview for one repository: languages, file/chunk/symbol totals, top directories, and key symbols. Requires an explicit 'project'. A good first call when drilling into an unfamiliar repo.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpProjectArg) (*mcp.CallToolResult, any, error) {
		c, err := reg.clientFor(in.Server)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		proj, err := mcpResolveProject(c, in.Project)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		resp, err := c.GetSummary(proj)
		if err != nil {
			return mcpErr(err), nil, nil
		}
		return mcpText(formatSummary(proj, resp)), nil, nil
	})
}

// ---- result helpers ---------------------------------------------------------

func mcpText(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func mcpErr(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "cix error: " + err.Error()}},
		IsError: true,
	}
}

// relPath renders a server-absolute file path relative to a root when possible,
// so the model reads shorter paths and we don't leak the full layout.
func relPath(root, file string) string {
	if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return file
}

// ---- formatters: servers, discovery & workspace -----------------------------

func formatServers(servers []serverSummary) string {
	if len(servers) == 0 {
		return "No cix servers configured. Set a server URL + API key in the extension settings, or run `cix config set api.url <url>` and `cix config set api.key <key>`."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d cix server(s):\n", len(servers))
	for _, s := range servers {
		marker := ""
		if s.IsDefault {
			marker = "  (default)"
		}
		fmt.Fprintf(&b, "- %s — %s%s\n", s.Name, s.URL, marker)
	}
	b.WriteString("\nPass a name above as the \"server\" argument to target it; omit \"server\" to use the default.")
	return strings.TrimRight(b.String(), "\n")
}

func formatProjects(projects []client.Project) string {
	if len(projects) == 0 {
		return "No projects are indexed on this server. Run `cix init` in a repository, or attach a repo via the dashboard."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d indexed project(s):\n", len(projects))
	for _, p := range projects {
		langs := ""
		if len(p.Languages) > 0 {
			langs = "  [" + strings.Join(p.Languages, ", ") + "]"
		}
		fmt.Fprintf(&b, "- %s  (%s, %d files)%s\n", p.HostPath, p.Status, p.Stats.TotalFiles, langs)
	}
	b.WriteString("\nPass a host_path above as the \"project\" argument to the per-project tools.")
	return strings.TrimRight(b.String(), "\n")
}

func formatWorkspaces(resp *client.WorkspaceListResponse) string {
	if resp == nil || len(resp.Workspaces) == 0 {
		return "No workspaces are configured on this server. Workspaces group repos for cross-project research; create one via the dashboard, or use cix_list_projects + per-project tools instead."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d workspace(s):\n", len(resp.Workspaces))
	for _, w := range resp.Workspaces {
		desc := ""
		if strings.TrimSpace(w.Description) != "" {
			desc = " — " + w.Description
		}
		fmt.Fprintf(&b, "- %s (id %s)%s\n", w.Name, w.ID, desc)
	}
	b.WriteString("\nPass an id or name above as the \"workspace\" argument to cix_workspace_search.")
	return strings.TrimRight(b.String(), "\n")
}

func formatWorkspaceProjects(resp *client.WorkspaceProjectListResponse) string {
	if resp == nil || len(resp.Projects) == 0 {
		return "This workspace has no repositories linked yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d repositor(y/ies) in this workspace:\n", len(resp.Projects))
	for _, wp := range resp.Projects {
		p := wp.Project
		langs := ""
		if len(p.Languages) > 0 {
			langs = "  [" + strings.Join(p.Languages, ", ") + "]"
		}
		fmt.Fprintf(&b, "- %s  (%s, %d files)%s\n", p.HostPath, p.Status, p.Stats.TotalFiles, langs)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatWorkspaceSearch(resp *client.WorkspaceSearchResponse) string {
	if resp == nil || (len(resp.Projects) == 0 && len(resp.Chunks) == 0) {
		return "No matches across the workspace. Try rephrasing the query, or widen scope with a higher top_projects/top_chunks."
	}
	var b strings.Builder
	// Surface a degraded status the same way the `cix ws search` CLI does —
	// otherwise the model reads a partial result as if it were complete.
	if resp.Status == "partial_failure" {
		b.WriteString("⚠ Partial result: at least one repository errored, so these matches are INCOMPLETE. Treat coverage as unreliable and check the server logs.\n\n")
	}
	fmt.Fprintf(&b, "Workspace search — %d repo(s) ranked, %d chunk(s):\n\n", len(resp.Projects), len(resp.Chunks))

	if len(resp.Projects) > 0 {
		b.WriteString("Repositories by relevance:\n")
		for _, p := range resp.Projects {
			label := p.Label
			if label == "" {
				label = p.ProjectPath
			}
			// Surface the blended project_score AND its bm25 (literal overlap) /
			// dense (semantic) components — the cix-workspace skill's trust rules
			// key off bm25-vs-dense, so the model must actually see them.
			fmt.Fprintf(&b, "  - %s  (%s)  [score %.2f, %d hit(s); bm25 %.2f, dense %.2f]\n",
				label, p.ProjectPath, p.ProjectScore, p.NumHits, p.BM25Score, p.DenseScore)
		}
		b.WriteString("\n")
	}

	if len(resp.Chunks) > 0 {
		b.WriteString("Top matches across repos:\n")
		for i, ch := range resp.Chunks {
			label := ch.Language
			if ch.SymbolName != "" {
				label = strings.TrimSpace(ch.Language + " " + ch.SymbolName)
			}
			fmt.Fprintf(&b, "%d. %s :: %s:%d  [%.2f]  (%s)\n",
				i+1, ch.ProjectPath, relPath(ch.ProjectPath, ch.FilePath), ch.StartLine, ch.Score, label)
			fmt.Fprintf(&b, "   ```%s\n", ch.Language)
			for _, line := range strings.Split(strings.TrimRight(ch.Content, "\n"), "\n") {
				fmt.Fprintf(&b, "   %s\n", line)
			}
			b.WriteString("   ```\n")
		}
	}

	if len(resp.StaleFTSRepos) > 0 {
		names := make([]string, len(resp.StaleFTSRepos))
		for i, s := range resp.StaleFTSRepos {
			names[i] = s.ProjectPath
		}
		fmt.Fprintf(&b, "\nNote: keyword (BM25) index not yet backfilled for: %s — these ranked on dense similarity only; reindex to enable hybrid.\n", strings.Join(names, ", "))
	}

	return strings.TrimRight(b.String(), "\n")
}

// ---- formatters: per-project ------------------------------------------------

func formatSearch(projectRoot string, resp *client.SearchResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return "No results found. Try lowering min_score (e.g. 0.2) or rephrasing the query."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d file(s) in %s:\n\n", resp.Total, projectRoot)
	for i, file := range resp.Results {
		lang := ""
		if file.Language != "" {
			lang = " · " + file.Language
		}
		fmt.Fprintf(&b, "%d. %s  [best %.2f]  %d match(es)%s\n",
			i+1, relPath(projectRoot, file.FilePath), file.BestScore, len(file.Matches), lang)
		for _, m := range file.Matches {
			label := m.ChunkType
			if m.SymbolName != "" {
				label = m.ChunkType + " " + m.SymbolName
			}
			rangeStr := fmt.Sprintf("line %d", m.StartLine)
			if m.EndLine != m.StartLine {
				rangeStr = fmt.Sprintf("lines %d-%d", m.StartLine, m.EndLine)
			}
			fmt.Fprintf(&b, "   [%.2f] %s  (%s)\n", m.Score, rangeStr, label)
			fmt.Fprintf(&b, "   ```%s\n", file.Language)
			for _, line := range strings.Split(strings.TrimRight(m.Content, "\n"), "\n") {
				fmt.Fprintf(&b, "   %s\n", line)
			}
			b.WriteString("   ```\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatDefinitions(projectRoot, symbol string, resp *client.DefinitionResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Sprintf("No definitions found for %q.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d definition(s) of %q:\n", resp.Total, symbol)
	for _, d := range resp.Results {
		fmt.Fprintf(&b, "- %s (%s) — %s:%d", d.Name, d.Kind, relPath(projectRoot, d.FilePath), d.Line)
		if d.Signature != nil && *d.Signature != "" {
			fmt.Fprintf(&b, "\n    %s", strings.TrimSpace(*d.Signature))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatReferences(projectRoot, symbol string, resp *client.ReferenceResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Sprintf("No references found for %q.", symbol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d reference(s) to %q:\n", resp.Total, symbol)
	for _, r := range resp.Results {
		loc := fmt.Sprintf("%s:%d", relPath(projectRoot, r.FilePath), r.StartLine)
		label := r.ChunkType
		if r.SymbolName != "" {
			label = r.ChunkType + " " + r.SymbolName
		}
		fmt.Fprintf(&b, "- %s  (%s)\n", loc, label)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSymbols(projectRoot, query string, resp *client.SymbolSearchResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Sprintf("No symbols found for %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d symbol(s) matching %q:\n", resp.Total, query)
	for _, s := range resp.Results {
		fmt.Fprintf(&b, "- %s (%s) — %s:%d", s.Name, s.Kind, relPath(projectRoot, s.FilePath), s.Line)
		if s.Signature != nil && *s.Signature != "" {
			fmt.Fprintf(&b, "\n    %s", strings.TrimSpace(*s.Signature))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatFiles(projectRoot, pattern string, resp *client.FileSearchResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Sprintf("No files found matching %q.", pattern)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) matching %q:\n", resp.Total, pattern)
	for _, f := range resp.Results {
		lang := ""
		if f.Language != nil && *f.Language != "" {
			lang = "  · " + *f.Language
		}
		fmt.Fprintf(&b, "- %s%s\n", relPath(projectRoot, f.FilePath), lang)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSummary(projectRoot string, s *client.ProjectSummary) string {
	if s == nil {
		return "No summary available."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", s.HostPath)
	fmt.Fprintf(&b, "Status: %s\n", s.Status)
	if len(s.Languages) > 0 {
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(s.Languages, ", "))
	}
	fmt.Fprintf(&b, "Files: %d · Chunks: %d · Symbols: %d\n", s.TotalFiles, s.TotalChunks, s.TotalSymbols)
	if len(s.TopDirectories) > 0 {
		b.WriteString("Top directories:\n")
		for _, d := range s.TopDirectories {
			fmt.Fprintf(&b, "  - %s (%d files)\n", d.Path, d.FileCount)
		}
	}
	if len(s.RecentSymbols) > 0 {
		b.WriteString("Key symbols:\n")
		for _, sym := range s.RecentSymbols {
			fmt.Fprintf(&b, "  - %s (%s) — %s\n", sym.Name, sym.Kind, relPath(projectRoot, sym.FilePath))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
