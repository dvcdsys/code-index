# cix-cowork

cix **skills** for Claude Cowork — the `cix` (single-repo) and `cix-workspace`
(cross-project) guidance, adapted from the Claude Code CLI to the `cix_*` **MCP
tools**.

This plugin is **skills only**: no MCP server, no CLI, no hooks. It teaches the
agent *how* to use the cix tools well (cix-vs-grep judgment, choosing the
cheapest tool, query writing, the cross-project workflow, and the workspace
trust rules). The tools themselves come from the cix MCP connector, which you
install separately.

## Two steps

**Step 1 — connect the cix MCP server (required first):**
```
cix mcp connect claude
```
This registers the `cix mcp` connector in Claude Desktop (one command, no
clicking). After restarting Claude Desktop you have the `cix_*` tools, and the
connector's built-in `instructions` already give the agent the essentials.

**Step 2 — install this plugin (optional, for richer guidance):**
Install `cix-cowork@code-index` from this marketplace. It adds the two skills,
so the agent gets the full nuance — the workspace trust rules, query tips, and
the per-repo drill-down workflow — lazy-loaded only when relevant.

> You can stop at Step 1: the connector alone works, with condensed guidance.
> Step 2 is the upgrade for heavy cross-repo research, where the `cix-workspace`
> skill's detail pays off.

## Requirements

- The cix MCP connector installed (Step 1), which needs the `cix` binary on PATH
  and a reachable cix server (configured in `~/.cix/config.yaml`).
- Repositories indexed server-side — the skills cannot index. If a repo isn't in
  `cix_list_projects`, index it via the cix dashboard or `cix init` on the
  server host.

## Components

- **`skills/cix`** — single-repo guidance: when to use cix vs read/grep, the
  cheapest-tool rule, query writing, score interpretation. Key behavior it
  teaches: there is no current project — list projects, then pass an explicit
  `project` (host_path).
- **`skills/cix-workspace`** — cross-project research workflow (which repos /
  which code / what changes), with the trust rules and troubleshooting catalog
  in `references/`. Uses `cix_workspace_search` to scope, then `cix_search` to
  drill into one repo at a time (no sub-agents).

This is the Cowork counterpart to the Claude Code `cix` plugin — same cix server,
MCP tools instead of the CLI.
