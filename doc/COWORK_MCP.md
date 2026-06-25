# Using cix in Cowork (and Claude Desktop) via MCP

The cix **Claude Code plugin** (`plugins/cix/`) does not work in **Cowork**.
Cowork is a different surface from Claude Code: it does not load Claude Code
plugins, hooks, or slash commands, and its agent does not shell out to the
`cix` CLI the way a Claude Code session does. Cowork's supported extension
channel is **MCP** — in fact, Cowork runs in a local VM on your machine where
"network egress permissions don't apply to MCPs", so MCP is the intended way to
give the agent a tool like cix.

So cix ships a built-in MCP server: the **`cix mcp`** command. It exposes cix's
semantic search to any MCP client (Cowork, the Claude Desktop Chat tab, other
MCP hosts) as tools the agent calls directly. Packaged as a `.mcpb` desktop
extension, it installs into Cowork in a couple of clicks.

This is a **thin MCP front-end over the same HTTP client every other cix
command uses**. It still needs a reachable `cix` server — the binary is a
client, not the index itself.

## Quick start (recommended: local `.mcpb` desktop extension)

1. **Build the bundle** for your platform:

   ```bash
   cd cli
   make mcpb                       # → dist/cix-<os>-<arch>.mcpb
   # or stamp a version: make mcpb VERSION=cli/v0.6.0
   ```

2. **Install it** in Claude Desktop: **Customize → Connectors → `+` → install
   extension**, and pick the `.mcpb` file.

3. **Configure the server** in the extension's settings:
   - **cix server URL** — e.g. `http://localhost:21847`, or your tunnel URL.
   - **cix API key** — the Bearer key (stored in the OS keychain).

   Leave both blank to fall back to whatever is in `~/.cix/config.yaml` — if you
   already use the cix CLI on this machine, it just works with no extra config.

4. In **Cowork**, the cix tools are now available; the agent calls them
   automatically when a task needs to search code by meaning.

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

## Configuration precedence

`cix mcp` resolves its target server exactly like every other cix command, plus
one fallback for the zero-prior-setup case:

1. `--api-url` / `--api-key` flags (rarely used here).
2. `CIX_API_URL` / `CIX_API_KEY` env vars — this is what the `.mcpb`
   `user_config` injects.
3. `~/.cix/config.yaml` (named servers, custom headers, default server).
4. **Fallback:** if no config file exists yet but an explicit URL **and** key are
   present from flags/env, a client is synthesized directly from them. This is
   what lets a fresh machine work from the extension settings alone.

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

## Cross-platform bundles

The bundled binary is platform-specific, so one `.mcpb` is produced per
platform. Cross-build with `GOOS`/`GOARCH`:

```bash
GOOS=darwin  GOARCH=arm64 make mcpb
GOOS=darwin  GOARCH=amd64 make mcpb
GOOS=windows GOARCH=amd64 make mcpb
```

Each bundle pins its `compatibility.platforms`, so a host won't offer a bundle
on a mismatched OS.

## Alternative: a remote MCP connector (Path 2, not yet implemented)

Instead of a per-machine extension, cix could expose an HTTP `/mcp` endpoint on
the **server** itself, added once as a remote custom connector (org-wide,
zero-touch for end users). That path requires implementing MCP's OAuth 2.1
authorization on the server (Claude's custom remote connectors authenticate via
OAuth, not a static Bearer key), which is a larger piece of work. The local
`.mcpb` path above is the pragmatic default and also the reusable foundation for
it — the MCP tool layer is identical; only the transport and auth differ.

## Troubleshooting

- **Tools don't appear in Cowork** — confirm the extension installed under
  Customize → Connectors and is enabled. The bundle must match your OS/arch.
- **"no servers configured" / auth errors** — set the server URL + API key in the
  extension settings, or run `cix config set api.url <url>` and
  `cix config set api.key <key>` once so `~/.cix/config.yaml` exists.
- **Empty results** — the server must have the repo indexed. Run `cix init` in
  the repo (or attach it via the dashboard), and pass that repo's absolute path
  as the `project` argument.
- **Nothing prints when piping** — `cix mcp` writes only JSON-RPC to stdout; all
  logs go to stderr. An immediate stdin EOF is a clean shutdown (exit 0).

## References

- Custom connectors (remote MCP): https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp
- Cowork overview: https://support.claude.com/en/articles/13345190-get-started-with-claude-cowork
- MCPB (desktop extension) manifest: https://github.com/anthropics/mcpb
- MCP Go SDK: https://github.com/modelcontextprotocol/go-sdk
