# Using cix from Codex

The `cix-openai` plugin is the Codex/ChatGPT plugin-format counterpart to the
Claude Code marketplace integration. It reuses the Claude Code `cix` and
`cix-workspace` skill bodies byte-for-byte, while adding the Codex manifest,
marketplace entry, and UI invocation policy. It is deliberately **CLI-first**:
the skills call the installed `cix` client through the terminal. The plugin does
not configure MCP, ship a background service, or depend on Claude hooks.

## Install the cix client

Deploy or connect to a self-hosted cix server, then install the client:

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash
cix config set server.main.url <server-url>
cix config set server.main.key <api-key>
cix status
```

You can copy the complete connect command from the dashboard's API-key dialog
instead of entering the URL and key separately.

## Add the marketplace

```bash
codex plugin marketplace add dvcdsys/code-index
```

Open `/plugins` in Codex, select **Code Index**, install **cix — Code Search**,
and begin a new conversation.

For development against a local checkout:

```bash
codex plugin marketplace add /absolute/path/to/code-index
```

Codex discovers the catalog at `.agents/plugins/marketplace.json` and the
plugin manifest at `plugins/cix-openai/.codex-plugin/plugin.json`.

## Behavior

The plugin includes two skills:

- `$cix` is available for automatic selection and loads the same single-repo
  guidance as the Claude Code plugin.
- `$cix-workspace` is explicit-only and loads the same cross-repository flow
  plus investigator guidance as the Claude Code plugin.

`plugins/cix/scripts/sync-skills.sh` owns the mirrors and CI checks the generated
Codex projections for drift. Skill bodies remain byte-identical; only the
frontmatter is adapted for Codex.

The package contains no MCP configuration. That keeps local repository access,
server selection, and credentials in the existing `cix` CLI configuration at
`~/.cix/config.yaml`.

## ChatGPT compatibility boundary

The manifest and skills use the shared OpenAI plugin format. Full execution
requires a surface with local terminal access and the `cix` binary installed,
which includes Codex in the ChatGPT desktop app and Codex CLI. A web-only
ChatGPT session cannot call a local CLI; supporting it would require a remote
HTTP tool connection, which this CLI-first plugin intentionally does not add.
