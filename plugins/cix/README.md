# cix — Claude Code plugin

Semantic code search and navigation for Claude Code, powered by the
[cix](https://github.com/dvcdsys/code-index) index.

## What you get

- **`/cix:search`, `/cix:def`, `/cix:refs`, `/cix:init`, `/cix:status`,
  `/cix:summary`** — slash commands wrapping the most-used `cix` CLI
  operations.
- **Bundled cix CLI** — the plugin auto-installs `cix` on first use if
  it isn't already in your `PATH` (no sudo, installs to `~/.local/bin`).
  If you already have `cix` installed via the official `install.sh`, the
  plugin just uses it.
- **`cix` skill (SKILL.md)** — lazy-loaded full instruction sheet
  covering when to use cix vs Grep, query patterns, scoring landscape,
  and CLI flags. Loads into the conversation only when Claude or you
  invoke it (`/cix:search`, `/cix-skill`, or auto-trigger on a relevant
  prompt). Stays in context for the rest of the session — never
  duplicated.
- **Behavioral nudges (hooks):**
  - **SessionStart** — calls `cix status` (2 s timeout). Caches the
    yes/no verdict in `$CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID`,
    injects a one-line reminder on success, stays silent on failure.
  - **PreToolUse(Grep|Glob)** — reads the SessionStart cache only; no
    inline `cix` calls. If the verdict is "yes" (`1`), suggests
    `cix search` instead of Grep, throttled with exponential backoff
    (fires on call #1, 2, 4, 8, 16, …). If the verdict is "no" (`0`)
    or missing, **stays silent for the entire session** — by design,
    so a flaky server doesn't cause intermittent nudges.

## Install

From an existing Claude Code marketplace:

```
/plugin marketplace add dvcdsys/code-index
/plugin install cix@code-index
/reload-plugins   # or restart Claude Code
```

Or for local development against this repo:

```
/plugin marketplace add /path/to/code-index
/plugin install cix@code-index --scope local
```

## Requirements

- **Claude Code v2.1.0+** (uses `hookSpecificOutput.additionalContext`
  for hook-driven nudges).
- **`curl`** — only needed the first time, for the auto-bootstrap of
  the `cix` CLI.
- **A reachable `cix-server`** — the CLI is a thin client. If you don't
  yet have a server, see the project README for Docker setup
  instructions.

## How adoption works (the design)

The plugin uses a 4-layer approach so SKILL.md loads at most once and
nudges don't spam the context:

| Layer | Mechanism | Cost over a 100-prompt session |
|---|---|---|
| 1. Skill description | Native Claude Code (always-in-context, ~200 B) | ~200 B once |
| 2. SessionStart hook | One-time reminder in indexed projects | ~200 B once |
| 3. PreToolUse(Grep\|Glob) hook | Exponential-backoff nudge | ~80 B × ~7 calls = ~560 B |
| 4. SKILL.md body | Native lazy-load (skill mechanism) | ~7 KB **once** if invoked |

Total plugin context overhead in a session that uses cix heavily:
~8 KB. In a session that doesn't touch cix at all: ~400 B (skill
description + slash command metadata).

The SKILL.md body is **never duplicated** — Claude Code's skill
mechanism guarantees a single insertion that stays in context for the
session. See the [skill content lifecycle](https://code.claude.com/docs/en/skills#skill-content-lifecycle)
docs.

## Configuration

### Where the bundled CLI is installed

The wrapper installs `cix` to `~/.local/bin/cix` by default. To override
the install location, set `CIX_PLUGIN_BIN_DIR` in your environment:

```bash
export CIX_PLUGIN_BIN_DIR=/usr/local/bin   # if you want sudo-installed
```

If you've already installed `cix` system-wide (e.g. via the project's
`install.sh`), the wrapper detects it and uses that binary — no second
copy is downloaded.

### Skipping the auto-install

Set `CIX_PLUGIN_BIN_DIR` to a directory that already contains a working
`cix` binary, or simply make sure `cix` is in your `$PATH` before
enabling the plugin.

### Hook state cleanup

Two per-session marker files live in `$CLAUDE_PLUGIN_DATA`
(resolves to `~/.claude/plugins/data/cix-code-index/`):
- `cix-aware-$SESSION_ID` — written by SessionStart, read by
  PreToolUse. Single-byte file (`0` or `1`).
- `cix-grep-count-$SESSION_ID` — counter for the exponential backoff.

This directory is plugin-managed and **not** cleaned by the OS
(unlike `/tmp`, which macOS purges daily). SessionStart opportunistically
deletes its own markers older than 30 days on each invocation, so files
don't accumulate forever.

## Files

| Path | Purpose |
|---|---|
| `.claude-plugin/plugin.json` | Plugin manifest |
| `skills/cix/SKILL.md` | Lazy-loaded usage skill (~7 KB) |
| `commands/*.md` | Six slash commands |
| `hooks/hooks.json` | SessionStart + PreToolUse(Grep\|Glob) registration |
| `scripts/cix-wrapper.sh` | "Use system or auto-install" CLI wrapper |
| `scripts/session-start.sh` | One-time session reminder |
| `scripts/grep-nudge.sh` | Exponential-backoff Grep nudge |
| `bin/cix` | Symlink to wrapper, exposed on `$PATH` while plugin enabled |

## Troubleshooting

- **"cix: command not found" inside Claude Code Bash tool** — the
  plugin isn't enabled or `bin/cix` isn't on `$PATH`. Run
  `/plugin list` and `which cix` from inside a Claude Code session.
- **Hooks not firing** — run Claude Code with `--debug` and look for
  hook registration messages. Check `/Users/dvcdsys/.claude/...` (or
  your local cache path) for the hook scripts and verify they're
  executable: `ls -la $(claude plugin list ... | path)/scripts/`.
- **Nudges feel too frequent / too rare** — edit the power-of-2 check
  in `scripts/grep-nudge.sh` to your taste. The current schedule
  (1, 2, 4, 8, 16, …) was chosen to balance "loud at start" with
  "fade away".
- **"This project has a cix semantic code index" never appears** —
  the project must contain a `.cix/` directory. Run `/cix:init` first.

## License

MIT — same as the parent project.
