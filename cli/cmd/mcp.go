package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// mcpInstructions is surfaced to the MCP client (e.g. Claude) on initialize.
// The model is deliberately server-centric: this connection is a window onto a
// cix SERVER that may hold many indexed repositories, NOT a single project, and
// nothing about scope is inferred from the environment (no "current project",
// no working directory).
const mcpInstructions = `cix is a semantic code index served by one or more cix servers, each of which
may hold MANY indexed repositories. This connection talks to the SERVER(S), not
a single project — there is no "current project" and nothing is inferred from a
working directory.

cix is multi-server: every tool takes an optional "server" argument (a name from
cix_list_servers); omit it to use the default server. Most setups have just one.

Start by discovering what is available:
  - cix_list_servers    — the configured cix servers (usually one).
  - cix_list_workspaces — research targets that span multiple repos.
  - cix_list_projects   — every indexed repository (use a host_path as "project").

Then choose scope explicitly:
  - Broad, cross-repository research → cix_workspace_search against a workspace.
    It ranks the relevant repos and returns hits across all of them at once.
  - Drill into one repository → pass its host_path as the "project" argument to
    cix_search / cix_definitions / cix_references / cix_symbols / cix_files /
    cix_summary. Use cix_definitions / cix_references when you already know a
    symbol name (cheap, metadata only); cix_search when searching by intent.
  - Read an actual file or browse the tree of an EXTERNAL (GitHub-backed) repo →
    cix_file (whole file or a line range) and cix_tree (one directory level).
    These work only for external projects the server keeps on disk; for a local
    project, read its files with your own filesystem tools instead.

Never assume a default project — always name the workspace or project you mean.`

// mcpCmd runs cix as a Model Context Protocol server over stdio, so cix's
// semantic search is usable from MCP host apps (notably Claude Desktop, and
// Cowork which runs inside it) without the agent shelling out to the CLI. It is
// a thin MCP front-end over the same HTTP client every other command uses, so
// it inherits the full multi-server / custom-header / env-override config
// resolution from getClient. Register it with a host via `cix mcp install`.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run cix as an MCP server over stdio (for Claude Desktop)",
	Long: `Run cix as a Model Context Protocol (MCP) server over stdio.

This exposes cix's semantic search to MCP host apps — notably the Claude Desktop
app (and Cowork, which runs inside it) — as tools the agent can call directly,
instead of shelling out to the cix CLI. Claude Code already reaches cix through
the cix CLI + plugin; this is the path for hosts that don't, starting with
Claude Desktop.

The server speaks newline-delimited JSON-RPC on stdin/stdout. It is not meant
to be run interactively; an MCP host launches it. Server selection, API key,
and custom headers are resolved exactly like every other cix command (flags >
CIX_* env vars > ~/.cix/config.yaml).

Tools exposed:
  cix_list_servers             list the cix servers this connection can reach
  cix_list_workspaces          list workspaces (cross-project research targets)
  cix_list_projects            list indexed repositories (a host_path = "project")
  cix_list_workspace_projects  list the repositories in a workspace
  cix_workspace_search         semantic search across all repos in a workspace
  cix_search                   semantic code search within one repository
  cix_definitions              go-to-definition for a symbol (metadata only)
  cix_references               find references to a symbol (metadata only)
  cix_symbols                  find symbols by name
  cix_files                    find files by path pattern
  cix_summary                  project overview (languages, top dirs, key symbols)
  cix_file                     read a file (whole or line range) — external projects only
  cix_tree                     list a directory (one level) — external projects only

Register this server with a host app using "cix mcp install <host>" (currently:
claude-desktop).`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, _ []string) error {
	reg := newServerRegistry()

	// Fail fast if the default server can't be resolved at all — a clear
	// startup error beats every tool call erroring. Named servers are resolved
	// lazily on first use.
	if _, err := reg.clientFor(""); err != nil {
		return err
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "cix",
		Title:   "cix — semantic code index",
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
	})

	registerCixTools(server, reg)

	// Stop cleanly on Ctrl-C / SIGTERM. The transport also returns when the
	// client closes stdin (EOF), which is the normal shutdown path when the
	// host app terminates the connection.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !isCleanShutdown(err) {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// isCleanShutdown reports whether a server.Run error represents the normal end
// of a stdio session rather than a real failure. The host closing stdin is the
// expected shutdown path; the SDK surfaces it as context cancellation, io.EOF,
// or its internal jsonrpc2 "server is closing" sentinel (code -32004), which
// lives in an internal package and so can only be matched by message here.
// Treating these as a clean exit keeps the host from seeing a spurious error.
func isCleanShutdown(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) ||
		strings.Contains(err.Error(), "server is closing")
}

// mcpGetClient builds the cix client for the MCP server. It first tries the
// standard resolution (getClient: flags > CIX_* env > ~/.cix/config.yaml,
// including named servers and custom headers). If that fails — typically
// because no config file exists yet — but an explicit URL and key are present
// from flags or env, it synthesizes a client from those directly.
//
// This lets a host that injects CIX_API_URL / CIX_API_KEY into the server's
// environment work with zero prior setup — a user who has never run
// `cix config` still gets a working client. When config does exist, the normal
// path is used unchanged.
func mcpGetClient() (*client.Client, error) {
	c, err := getClient()
	if err == nil {
		return c, nil
	}

	url := apiURL
	if url == "" {
		url = os.Getenv(envAPIURL)
	}
	key := apiKey
	if key == "" {
		key = os.Getenv(envAPIKey)
	}
	if url != "" && key != "" {
		return client.New(url, key), nil
	}

	// Surface the original error — it already names the exact `cix config set`
	// commands to run, which is the most actionable guidance here.
	return nil, err
}

// serverRegistry resolves and caches a cix client per named server, so the one
// `cix mcp` process can talk to ANY server configured in ~/.cix/config.yaml —
// matching the CLI's multi-server model (`cix --server <alias> ...`). The empty
// alias is the default server, resolved with the same flag/env/config
// precedence (and no-config env fallback) as every other cix command. Handlers
// may run concurrently, so access is guarded by a mutex.
type serverRegistry struct {
	mu    sync.Mutex
	cache map[string]*client.Client
}

func newServerRegistry() *serverRegistry {
	return &serverRegistry{cache: make(map[string]*client.Client)}
}

// clientFor returns the client for a server alias, building and caching it on
// first use. Empty alias → the default server.
func (r *serverRegistry) clientFor(alias string) (*client.Client, error) {
	alias = strings.TrimSpace(alias)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cache[alias]; ok {
		return c, nil
	}
	c, err := buildServerClient(alias)
	if err != nil {
		return nil, err
	}
	r.cache[alias] = c
	return c, nil
}

// buildServerClient constructs the client for one server alias. Empty alias →
// the default server via mcpGetClient (flag > CIX_* env > config default, with
// the no-config synth fallback). A named alias is looked up in the config and
// built from that entry alone (its own URL/key/headers), so per-server config
// is never cross-contaminated by the default's env overrides.
func buildServerClient(alias string) (*client.Client, error) {
	if alias == "" {
		return mcpGetClient()
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	srv, ok := cfg.GetServer(alias)
	if !ok {
		return nil, fmt.Errorf("server %q is not configured; call cix_list_servers to see the available servers", alias)
	}
	if srv.URL == "" || srv.Key == "" {
		return nil, fmt.Errorf("server %q is missing a URL or API key in ~/.cix/config.yaml", alias)
	}
	c := client.New(srv.URL, srv.Key)
	if err := applyServerHeaders(c, srv); err != nil {
		return nil, err
	}
	if cfg.Indexing.StreamingIdleTimeoutSec > 0 {
		c.SetStreamingIdleTimeout(time.Duration(cfg.Indexing.StreamingIdleTimeoutSec) * time.Second)
	}
	return c, nil
}

// applyServerHeaders attaches a named server's custom headers (with ${ENV}
// expansion + validation), mirroring getClient. No-op when none are set.
func applyServerHeaders(c *client.Client, srv *config.ServerEntry) error {
	if len(srv.Headers) == 0 {
		return nil
	}
	expanded := make(map[string]string, len(srv.Headers))
	for name, raw := range srv.Headers {
		val, err := config.ExpandEnvHeaderValue(raw)
		if err != nil {
			return fmt.Errorf("custom header %q for server %q: %w", name, srv.Name, err)
		}
		if err := config.ValidateHeader(name, val); err != nil {
			return fmt.Errorf("invalid custom header %q for server %q: %w", name, srv.Name, err)
		}
		expanded[name] = val
	}
	c.SetCustomHeaders(expanded)
	return nil
}

// serverSummary is one row of the cix_list_servers result. Keys are never
// included — only the name, URL, and whether it is the default.
type serverSummary struct {
	Name      string
	URL       string
	IsDefault bool
}

// mcpListServers reports the configured cix servers for the discovery tool.
// With a config file it lists every named server and marks the effective
// default; with no config but an explicit URL (the env-injected single-server
// path) it reports a lone synthetic default.
func mcpListServers() []serverSummary {
	if cfg, err := config.Load(); err == nil && len(cfg.Servers) > 0 {
		defName := serverName
		if defName == "" {
			defName = os.Getenv(envServer)
		}
		if defName == "" {
			defName = cfg.DefaultServer
		}
		if _, ok := cfg.GetServer(defName); !ok {
			if d, ok := cfg.DefaultServerEntry(); ok {
				defName = d.Name
			}
		}
		out := make([]serverSummary, 0, len(cfg.Servers))
		for _, s := range cfg.Servers {
			out = append(out, serverSummary{Name: s.Name, URL: s.URL, IsDefault: s.Name == defName})
		}
		return out
	}

	url := apiURL
	if url == "" {
		url = os.Getenv(envAPIURL)
	}
	if url != "" {
		return []serverSummary{{Name: config.DefaultServerName, URL: url, IsDefault: true}}
	}
	return nil
}

// mcpResolveProject turns the explicit "project" argument of a tool call into a
// registered project the server understands. It is deliberately
// environment-independent — it never consults the working directory. The agent
// is expected to pass a host_path obtained from cix_list_projects.
//
//   - exact host_path match (covers local repos and external ids like
//     github.com/owner/repo@branch) → used as-is.
//   - absolute path that sits inside a registered root → resolved to that root.
//   - anything else → an error listing the available projects, so the agent can
//     pick a valid one. Empty is rejected the same way (no implicit default).
func mcpResolveProject(apiClient *client.Client, project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", errors.New("the 'project' argument is required — call cix_list_projects and pass one of the host_path values")
	}

	projects, err := apiClient.ListProjects()
	if err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}

	// Exact registered id (host_path) — the common case.
	for _, p := range projects {
		if p.HostPath == project {
			return p.HostPath, nil
		}
	}

	// An absolute path inside a registered local root resolves to that root,
	// the way git finds the repo root from a subdirectory. This compares the
	// given path to the roots directly — no working-directory involved.
	if filepath.IsAbs(project) {
		best := ""
		for _, p := range projects {
			if strings.HasPrefix(project, p.HostPath+"/") && len(p.HostPath) > len(best) {
				best = p.HostPath
			}
		}
		if best != "" {
			return best, nil
		}
	}

	return "", fmt.Errorf("project %q is not indexed on this server; available projects:\n  - %s",
		project, strings.Join(projectHostPaths(projects), "\n  - "))
}

// mcpResolveWorkspace turns the "workspace" argument (an id or a name) into a
// workspace id the server understands. Like project resolution, it is fully
// server-driven — the candidates come from cix_list_workspaces.
func mcpResolveWorkspace(apiClient *client.Client, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", errors.New("the 'workspace' argument is required — call cix_list_workspaces and pass an id or name")
	}

	resp, err := apiClient.ListWorkspaces()
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}

	var names []string
	for _, w := range resp.Workspaces {
		if w.ID == workspace || strings.EqualFold(w.Name, workspace) {
			return w.ID, nil
		}
		names = append(names, fmt.Sprintf("%s (id %s)", w.Name, w.ID))
	}
	if len(names) == 0 {
		return "", errors.New("no workspaces are configured on this server (workspaces may be disabled, or none created yet)")
	}
	return "", fmt.Errorf("workspace %q not found; available workspaces:\n  - %s",
		workspace, strings.Join(names, "\n  - "))
}

// mcpResolveScopePaths normalises --in / --exclude filter paths for a tool
// call. Absolute paths pass through; relative paths are joined onto the
// resolved PROJECT ROOT (never the process working directory), so scoping a
// search to e.g. "internal/auth" means "inside the repo", regardless of where
// the host launched the MCP server.
func mcpResolveScopePaths(projectRoot string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p) {
			out = append(out, p)
		} else {
			out = append(out, filepath.Join(projectRoot, p))
		}
	}
	return out
}

// projectHostPaths extracts the host_path of each project for error listings.
func projectHostPaths(projects []client.Project) []string {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.HostPath
	}
	return ids
}
