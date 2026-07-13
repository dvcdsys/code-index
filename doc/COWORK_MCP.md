# Using cix in Claude Desktop (and Cowork) via MCP

The cix **Claude Code plugin** (`plugins/cix/`) does not work in **Claude
Desktop** or **Cowork**. Cowork runs inside Claude Desktop, in a local VM on your
machine; neither surface loads Claude Code plugins, hooks, or slash commands, and
the agent does not shell out to the `cix` CLI the way a Claude Code session does.
The supported extension channel there is **MCP**.

So cix ships a built-in MCP server: the **`cix mcp`** command. It exposes cix's
semantic search to MCP host apps (Claude Desktop, and Cowork inside it) as tools
the agent calls directly, instead of shelling out to the cix CLI.

This is a **thin MCP front-end over the same HTTP client every other cix command
uses**. It still needs a reachable `cix` server — the binary is a client, not the
index itself.

> Claude Code already reaches cix through the cix CLI + plugin, so this MCP path
> is specifically its **Claude Desktop** counterpart.

## Quick start

Two steps: register the MCP server, then (optionally) add the skills.

### 1. Register the MCP server with Claude Desktop

One command points Claude Desktop at this already-installed `cix` binary:

```bash
cix mcp install claude-desktop
```

This writes an `mcpServers` entry into Claude Desktop's config
(`claude_desktop_config.json`) whose command is the absolute path to your `cix`
binary plus the `mcp` subcommand. It:

- preserves every other server and setting (and keeps a `.bak`),
- is idempotent (safe to re-run),
- writes **no secrets** — the cix server URL and API key come from
  `~/.cix/config.yaml`.

Then **restart Claude Desktop**; the `cix_*` tools appear once it reconnects.

```bash
cix mcp install claude-desktop --print   # show what would be added, change nothing
cix mcp uninstall claude-desktop         # remove the registration
cix mcp install claude-desktop --name cix-prod   # register under a custom key
```

### 2. Install the Cowork skills (optional)

Install the `cix-cowork` plugin from the marketplace for the richer guidance —
the `cix` (single-repo) and `cix-workspace` (cross-project) skills adapted to the
`cix_*` MCP tools:

```
/plugin marketplace add dvcdsys/code-index
/plugin install cix-cowork@code-index
```

The MCP server alone (step 1) already gives the agent the essentials via its
built-in `instructions`; the skills add the workspace trust rules, query tips,
and the per-repo drill-down workflow, lazy-loaded only when relevant.

## Server-centric, multi-server model (no "current project")

This connection talks to cix **server(s)**, each of which may hold many indexed
repositories. There is **no implicit "current project"** and nothing is inferred
from a working directory — the MCP server behaves the same no matter where the
host launched it. Scope is always **explicit**: the agent discovers what's
available, then names the server / workspace / project it means.

cix is **multi-server**, exactly like the CLI (`cix --server <alias> …`). The one
`cix mcp` process can reach every server in `~/.cix/config.yaml`. Every tool
takes an optional **`server`** argument (a name from `cix_list_servers`); omit it
to use the default server. Most setups have just one.

## Tools exposed

**Discovery**

| Tool | Purpose |
|---|---|
| `cix_list_servers` | List the cix servers this connection can reach (pick one with `server`) |
| `cix_list_workspaces` | List workspaces (cross-project research targets) |
| `cix_list_projects` | List every indexed repository (use a `host_path` as `project`) |
| `cix_list_workspace_projects` | List the repositories in a workspace |

**Cross-project research**

| Tool | Purpose |
|---|---|
| `cix_workspace_search` | Semantic search across **all** repos in a workspace at once — ranks the relevant repos and returns hits from across them. The tool for broad, multi-repo research. Requires an explicit `workspace` (id or name). |

**Per-project drill-down** (each requires an explicit `project` — a `host_path`
from `cix_list_projects`)

| Tool | Purpose |
|---|---|
| `cix_search` | Semantic code search within one repository |
| `cix_definitions` | Go-to-definition for a symbol (metadata only) |
| `cix_references` | Find references to a symbol (metadata only) |
| `cix_symbols` | Find symbols by name |
| `cix_files` | Find files by path pattern |
| `cix_summary` | Project overview (languages, top dirs, key symbols) |

Typical agent flow: `cix_list_workspaces` / `cix_list_projects` to see what's
indexed → `cix_workspace_search` for broad research, or pass a `host_path` as
`project` to drill into one repo. An unknown or missing `project`/`workspace`
returns an error listing the valid choices, so the agent self-corrects. In
`cix_search`, relative `in`/`exclude` paths resolve against the **repository
root**, never a working directory.

> **Read-only surface.** These MCP tools list, search, and read — they never
> create, delete, or link anything. Workspace *management* (creating a
> workspace, linking/unlinking projects, deleting one) is done with the `cix`
> CLI (`cix ws create` / `add` / `remove` / `delete`) or the dashboard; an MCP
> host consumes workspaces that already exist. Full verbs:
> [`CLI_REFERENCE.md`](CLI_REFERENCE.md#workspaces-cross-repo).

## Configuration precedence

`cix mcp` resolves its target server exactly like every other cix command, plus
one fallback for the zero-prior-setup case:

1. `--api-url` / `--api-key` flags (rarely used here).
2. `CIX_API_URL` / `CIX_API_KEY` env vars (e.g. when a host injects them into the
   server's environment).
3. `~/.cix/config.yaml` (named servers, custom headers, default server).
4. **Fallback:** if no config file exists yet but an explicit URL **and** key are
   present from flags/env, a client is synthesized directly from them.

The above resolves the **default** server (used when a tool omits `server`). A
tool that passes `server: "<name>"` is resolved straight from that named entry
in `~/.cix/config.yaml` (its own URL, key, and custom headers) — the default's
flag/env overrides never leak across to a named server. Configure extra servers
the usual way:

```bash
cix config set server.corporate.url https://cix.corp.internal
cix config set server.corporate.key <bearer-token>
```

Both then appear in `cix_list_servers`, and the agent can target either.

## Running it manually

`cix mcp` speaks newline-delimited JSON-RPC on stdin/stdout. It is launched by an
MCP host, not run interactively, but you can smoke-test it:

```bash
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | cix mcp
```

(Feed requests with stdin held open — closing stdin immediately is treated as a
clean shutdown.)

## Supporting other hosts (Codex, …)

`cix mcp install <host>` is host-pluggable. Today the only implemented host is
`claude-desktop` (JSON `mcpServers` in `claude_desktop_config.json`). The host
registry in `cli/cmd/mcp_connect.go` is the seam for adding other agents and
harnesses — each host supplies where its MCP config lives and how to merge/remove
a server entry in that config's format. For example, Codex stores MCP servers as
TOML (`[mcp_servers.<name>]` in `~/.codex/config.toml`), so adding it is a matter
of registering its path resolver and TOML merge/remove funcs — the install /
uninstall / `--print` / `--name` wiring is shared and untouched. The MCP tool
layer (`cix mcp`) is identical across all hosts; only registration differs.

## Alternative: a remote MCP connector (Path 2, not yet implemented)

Instead of a per-machine local stdio server, cix could expose an HTTP `/mcp`
endpoint on the **server** itself, added once as a remote custom connector
(org-wide, zero-touch for end users). That path requires implementing MCP's
OAuth 2.1 authorization on the server (Claude's custom remote connectors
authenticate via OAuth, not a static Bearer key), which is a larger piece of
work. The local `cix mcp install` path above is the pragmatic default and also
the reusable foundation for it — the MCP tool layer is identical; only the
transport and auth differ.

## Troubleshooting

- **Tools don't appear** — confirm `cix mcp install claude-desktop` wrote the
  entry (`--print` shows the target path), then fully restart Claude Desktop.
- **"no servers configured" / auth errors** — set the server URL + API key so
  `~/.cix/config.yaml` exists: `cix config set api.url <url>` and
  `cix config set api.key <key>`.
- **Empty results** — the server must have the repo indexed. Run `cix init` in
  the repo (or attach it via the dashboard), and pass that repo's absolute path
  as the `project` argument.
- **Nothing prints when piping** — `cix mcp` writes only JSON-RPC to stdout; all
  logs go to stderr. An immediate stdin EOF is a clean shutdown (exit 0).

## References

- Custom connectors (remote MCP): https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp
- Cowork overview: https://support.claude.com/en/articles/13345190-get-started-with-claude-cowork
- MCP Go SDK: https://github.com/modelcontextprotocol/go-sdk
