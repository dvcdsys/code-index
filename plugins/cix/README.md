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
- **`cix-workspace` skill (SKILL.md)** *(experimental, **manual-only**)* —
  companion workflow for tasks that span more than one repo. **Does
  not auto-trigger** — invoke it explicitly with `/cix-workspace <task>`
  when you want the full cross-project workflow guidance: which repos
  are in scope, what code is relevant, what changes need to land.
  Includes ten trust rules for interpreting `projects[]` vs `chunks[]`,
  a four-part fan-out prompt template, and an anti-patterns list.
- **`cix-workspace-investigator` sub-agent** *(experimental)* — thin
  read-only shell around `cix search`/`cix def`/`cix refs` for parallel
  per-repo fan-out from the workspace skill. Hard rules baked in: one
  repo per spawn, no edits, no recursion. Methodology and output
  format are the main agent's call per spawn; the sub-agent follows
  instructions. Lives at `agents/cix-workspace-investigator.md` —
  available as `subagent_type="cix-workspace-investigator"` in `Agent`
  tool calls.
- **Behavioral nudges (5 hooks):**
  - **SessionStart** — calls `cix status` (2 s timeout). Caches the
    yes/no verdict in `$CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID-$DIR_HASH`,
    injects a one-line reminder on success.
  - **CwdChanged** — when Claude `cd`s into another directory mid-session,
    re-runs `cix status` for the new dir and caches the verdict. Silent
    (no reminder); PostToolUse handles the first-Grep-in-new-project
    nudge through its per-project backoff.
  - **PostToolUse(Grep|Glob|Bash)** — fires after a Grep/Glob call or a
    Bash command that looks like `grep`/`rg`/`find` (other Bash stays
    silent). Reads the cache for the current `(session, project_dir)`
    pair; no inline `cix` calls. If the verdict is "yes" (`1`),
    suggests `cix search` with exponential backoff per project (fires
    on call #1, 2, 4, 8, …). Missing cache or "no" (`0`) → silent for
    the rest of the session in that project. (PostToolUse instead of
    PreToolUse because current Claude Code only surfaces
    `hookSpecificOutput.additionalContext` for PostToolUse,
    UserPromptSubmit, and SessionStart — the model sees the nudge in
    time for the NEXT decision, which is behaviorally equivalent for
    an advisory hook. Rationale lives at `scripts/grep-nudge.sh:9-14`.)
  - **PostCompact** — after auto-compaction in long sessions, re-injects
    the SessionStart reminder if the current project is cix-aware
    (skill body itself survives compaction natively; the SessionStart
    one-liner does not).
  - **SessionEnd** — glob-deletes every per-(session, dir) cache file
    when the session terminates. Best-effort; the 30-day GC inside
    SessionStart catches markers left over from forced kills.

The cache key includes a project-dir hash (`shasum -a 256` first 8
chars), so per-session, per-project state is isolated — Claude can
move between projects mid-session and each one keeps its own verdict
and backoff counter.

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
| 3. PostToolUse(Grep\|Glob\|Bash) hook | Exponential-backoff nudge | ~80 B × ~7 calls = ~560 B |
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

For corporate, regulated, or air-gapped environments where the plugin
must never reach out to the network, set `CIX_NO_AUTOINSTALL=1`. When
`cix` is not already installed, the wrapper then fails with a clear
message (including the exact manual-install command) instead of
fetching `install.sh`.

The auto-bootstrap is **pinned** to a specific CLI release tag (see
`CIX_PINNED_VERSION` at the top of `scripts/cix-wrapper.sh`): both the
`install.sh` script reference and the installed binary version are
fixed, so a fresh install is reproducible rather than tracking whatever
is on `main`.

### Capping cix output (`CIX_MAX_OUTPUT_LINES`)

By default the wrapper passes cix output through untouched. Set
`CIX_MAX_OUTPUT_LINES` to a positive integer to cap stdout to that many
lines and append a one-line truncation notice:

```bash
export CIX_MAX_OUTPUT_LINES=80
```

The cap is layered on top of any `--limit N` flag, not a replacement for
it — `--limit` bounds how many *files* cix returns; `CIX_MAX_OUTPUT_LINES`
is a hard ceiling on total printed lines, useful for keeping a single
`cix search` from flooding an agent's context. Unset (the default),
`0`, or any non-numeric value disables the cap entirely: the wrapper
`exec`s cix directly with full output, streaming, and the original exit
code. stderr is never capped.

### Hook state cleanup

Two per-session marker files live in `$CLAUDE_PLUGIN_DATA`
(resolves to `~/.claude/plugins/data/cix-code-index/`):
- `cix-aware-$SESSION_ID-$DIR_HASH` — written by SessionStart (and
  refreshed by CwdChanged), read by the PostToolUse nudge.
  Single-byte file (`0` or `1`). The `$DIR_HASH` suffix isolates the
  verdict per project directory within a session.
- `cix-grep-count-$SESSION_ID` — counter for the exponential backoff.

This directory is plugin-managed and **not** cleaned by the OS
(unlike `/tmp`, which macOS purges daily). The plugin manages cleanup
in two tiers:
1. **SessionEnd hook** — deletes both markers when the session
   terminates normally. Covers the common case.
2. **30-day GC in SessionStart** — opportunistically deletes markers
   older than 30 days at every session start. Catches markers left
   over from sessions that exited forcibly (kill -9, OOM).

## Files

| Path | Purpose |
|---|---|
| `.claude-plugin/plugin.json` | Plugin manifest |
| `skills/cix/SKILL.md` | Lazy-loaded single-repo usage skill (~7 KB) |
| `skills/cix-workspace/SKILL.md` | Cross-project workflow skill *(experimental)* |
| `agents/cix-workspace-investigator.md` | Read-only per-repo investigator sub-agent *(experimental)* |
| `commands/*.md` | Six slash commands |
| `hooks/hooks.json` | SessionStart + PostToolUse(Grep\|Glob\|Bash) + CwdChanged + PostCompact + SessionEnd registration |
| `scripts/cix-wrapper.sh` | "Use system or auto-install" CLI wrapper (pinned, opt-out via `CIX_NO_AUTOINSTALL`) |
| `scripts/session-start.sh` | One-time session reminder |
| `scripts/grep-nudge.sh` | Exponential-backoff Grep nudge |
| `scripts/lib-cix-probe.sh` | Shared `cix status` probe helpers (3-state verdict), sourced by the hooks |
| `bin/cix` | Symlink to wrapper, exposed on `$PATH` while plugin enabled |

## Cross-project workflow (experimental, manual-only)

For tasks that touch more than the repo you're cd'd into, the plugin
ships a second skill — **`cix-workspace`** — plus a dedicated
**`cix-workspace-investigator`** sub-agent for parallel per-repo
fan-out. **Neither auto-triggers.** You invoke them explicitly when
you actually need them — typically with `/cix-workspace <task>`.

> *Why manual-only?* The workspace flow is heavier than single-repo
> `cix search` (multi-repo fan-out, server-side clones, sub-agent
> spawns) and only pays off when the task genuinely spans repos. We
> don't want it firing on every request that vaguely mentions
> "services". Load it deliberately, when you've decided cross-project
> research is the right shape of work. This policy may change once
> the heuristics around "is this really cross-project?" are more
> reliable.

The flow once you've invoked it:

1. `cix-workspace` skill loads, structures the request around three
   questions (which repos? what code? what changes?).
2. Main agent runs a short, term-rich workspace search and reads the
   `projects[]` panel.
3. For each relevant repo, main agent spawns a `cix-workspace-investigator`
   sub-agent with the task verbatim, the project_path, seed chunks
   plus its own interpretive commentary on them, and an explicit
   deliverable.
4. Sub-agents run in parallel with isolated context. Main agent
   synthesizes their reports.

Requirements:

- Configured cix server with **workspaces enabled**
  (`CIX_WORKSPACES_ENABLED=true`).
- At least one workspace containing the repos you're working across.

See [`workspaces.md`](https://github.com/dvcdsys/code-index/blob/main/workspaces.md)
in the parent project for setup details and the full search-algorithm
reference.

The skill body documents ten "trust rules" derived from internal
calibration testing — how to read `chunk.score=0` (BM25-only literal
match), when to drop down to per-project search, when adding a
disambiguating token helps vs hurts, and so on. Load it via
`/cix-workspace` when you need the full reference; it stays in
context for the rest of the session.

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
- **Nudge does not fire on `grep` invoked via Bash** — the `PostToolUse`
  matcher works on `tool_name`, not on the command string. The plugin
  matches `Bash` explicitly and filters grep/rg/find/fd/ag/ack from
  `tool_input.command` inside `grep-nudge.sh`. Confirm
  `hooks/hooks.json` contains both `"matcher": "Grep|Glob"` and
  `"matcher": "Bash"` entries; the regression in
  `tests/manifest.bats` enforces this.

## License

MIT — same as the parent project.
