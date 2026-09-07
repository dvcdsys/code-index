# cix for Codex

Native Codex packaging for the same `cix` and `cix-workspace` skills shipped by
the Claude Code plugin. The skill bodies are synchronized byte-for-byte; only
the Codex manifest and UI metadata are product-specific. This plugin contains
no MCP server, hooks, or auto-installer: Codex calls the same installed `cix`
executable a developer uses in a terminal.

## Prerequisites

Install the client and connect it to a self-hosted cix server:

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
cix config set server.main.url <server-url>
cix config set server.main.key <api-key>
cix status
```

The ready-made connect command in the cix dashboard can replace the two
`cix config` commands.

## Install from this marketplace

Register the repository as a Codex marketplace:

```bash
codex plugin marketplace add dvcdsys/code-index
```

Then start Codex, open `/plugins`, choose the **Code Index** marketplace, and
install **cix — Code Search**. Start a new conversation so the skills are
available.

For local development, register the repository checkout instead:

```bash
codex plugin marketplace add /absolute/path/to/code-index
```

## Skills

- `cix` activates automatically for open-ended code discovery and symbol
  navigation. It uses the same workflow body as the Claude Code skill.
- `cix-workspace` is explicit-only. Invoke `$cix-workspace` for the same
  cross-repository workflow and investigator fan-out available in Claude Code.

Both skills use the CLI directly. They do not require or configure MCP.

Run `plugins/cix/scripts/sync-skills.sh` after changing either canonical Claude
Code skill. CI runs the same script with `--check` so the shared bodies cannot
silently drift while Codex keeps its native frontmatter.
