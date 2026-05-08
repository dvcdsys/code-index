# Claude Code Plugin (`cix`)

Official Claude Code plugin for `cix` — semantic code search and navigation.
Installs in two commands, bundles the `cix` CLI, ships slash commands, a
skill, and behavioral hooks that nudge Claude to prefer `cix` over `Grep`
for semantic queries.

> [!IMPORTANT]
> **The plugin does NOT include the `cix` server.** The plugin only ships
> the CLI client and Claude Code integration glue. You must run a `cix`
> server separately and point the CLI at it (see [Prerequisites](#prerequisites)
> below). Without a reachable server the plugin can install, but `cix`
> commands will fail at runtime.

---

## What you get

When the plugin is enabled, every Claude Code session in a `cix`-indexed
project automatically gets:

- **Slash commands** — `/cix:search`, `/cix:def`, `/cix:refs`, `/cix:init`,
  `/cix:status`, `/cix:summary`. Invocable manually or by Claude.
- **Bundled `cix` CLI** — the plugin auto-installs `cix` to
  `~/.local/bin/` on first use if it isn't already in your `PATH` (no
  sudo). If you already installed `cix` system-wide via `install.sh`,
  the plugin reuses that binary — no second copy.
- **`cix` skill (`SKILL.md`)** — full usage reference (when to reach for
  cix vs Grep, query patterns, scoring landscape, CLI flags). **Lazy-loaded**
  by Claude Code — enters context only when invoked, stays once per
  session.
- **Behavioral hooks (5 total):**
  - **`SessionStart`** — at session start, runs `cix status` (2-second
    timeout) to ask the cix-server whether the current project is
    registered. The verdict (`1` or `0`) is cached in
    `$CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID-$DIR_HASH`. On `1`,
    injects a one-line reminder.
  - **`CwdChanged`** — when Claude changes working directory mid-session
    (e.g. via `cd ../other-project`), evaluates cix-awareness for the
    NEW directory and caches the verdict. Silent (no reminder) — the
    next Grep/Glob call will fire the standard backoff nudge if
    appropriate. No-op if the new directory was already evaluated in
    this session (Claude bouncing back to a known project).
  - **`PreToolUse(Grep|Glob)`** — reads the cache for the current
    `(session, project_dir)` pair (~1 ms, no cix call). If the cache
    says `1`, occasionally suggests `cix search` instead of Grep,
    throttled with **exponential backoff** (fires on call #1, 2, 4, 8,
    16, 32, 64, … *per project*). Each project visited in a session
    starts a fresh backoff counter, so the first Grep in a new
    cix-aware project always gets a nudge. **Strict policy:** missing
    cache or `0` → silent for the entire session in that project.
  - **`PostCompact`** — after Claude Code compacts the conversation
    (long-running sessions), re-injects the SessionStart reminder if
    the current project is cix-aware. Skill bodies survive compaction
    natively, but the SessionStart `additionalContext` does not — this
    hook keeps cix-awareness alive after compaction without relying on
    the skill being invoked yet.
  - **`SessionEnd`** — when the session terminates, deletes every
    cache file belonging to this session (glob: all `(session, *)`
    pairs across every project visited).

**Cache key includes a project-dir hash** (`shasum -a 256` first 8
chars), so a single Claude Code session that traverses multiple
projects keeps a separate verdict per project — fresh backoff per
project, correct cix-aware state per directory.

The strict cache contract means: a session that started while the
cix-server was unreachable will stay in "silent" mode for that project
even if the server comes back online. Restart the Claude Code session
or `cd` away and back to re-evaluate. Better to miss a few nudges than
to pester a developer whose server is down.

State location: `$CLAUDE_PLUGIN_DATA` is plugin-persistent storage
(`~/.claude/plugins/data/cix-code-index/`) — it survives plugin updates
and is **not** cleaned by the OS, unlike `/tmp` (macOS purges 3-day-old
files daily; Linux clears on reboot). Cleanup is two-tiered: SessionEnd
removes per-session markers on normal exit; SessionStart opportunistically
deletes markers older than 30 days as a safety net for forced kills
(kill -9, OOM, panic).

---

## How it works

The plugin uses a **4-layer adoption design** so SKILL.md loads at most
once and nudges don't spam the context.

| Layer | Mechanism | Cost over a 100-prompt session |
|---|---|---|
| 1. Skill description | Native Claude Code (always-in-context, ~200 B) | ~200 B once |
| 2. SessionStart hook | One-time reminder in indexed projects | ~200 B once |
| 3. PreToolUse(Grep\|Glob) hook | Exponential-backoff nudge | ~80 B × ~7 calls = ~560 B |
| 4. SKILL.md body | Native lazy-load (skill mechanism) | ~7 KB **once** if invoked |

**Total plugin context overhead** in a session that uses cix heavily:
~8 KB. In a session that doesn't touch cix at all: ~400 B (skill
description + slash command metadata).

The SKILL.md body is **never duplicated** — Claude Code's skill mechanism
guarantees a single insertion that stays in context for the session
([skill content lifecycle docs](https://code.claude.com/docs/en/skills#skill-content-lifecycle)).

---

## Prerequisites

The plugin is the **client side** of the cix stack. Before installing,
make sure you have:

### 1. A reachable `cix` server

The CLI talks to a `cix-server` over HTTP. The server runs separately
(in Docker, natively on macOS, or remotely). See the [main README](README.md)
for the three deployment modes:

- Docker CPU — `docker compose up -d`
- Docker CUDA (NVIDIA GPU) — `docker compose -f docker-compose.cuda.yml up -d`
- Native macOS (Apple Silicon Metal) — `cd server && make bundle && make run`

Verify it's up:

```bash
curl http://localhost:21847/health   # → {"status":"ok"}
```

If you have a fresh database, the server requires `CIX_BOOTSTRAP_ADMIN_EMAIL`
and `CIX_BOOTSTRAP_ADMIN_PASSWORD` on first boot — see the main README's
Quick Start.

### 2. The `cix` CLI configured to point at that server

The CLI configuration is **independent of the plugin**. The plugin uses
whatever config the CLI reads from `~/.cix/config.yaml`. Configure it
once:

```bash
cix config set api.url http://localhost:21847
cix config set api.key cix_<your-api-key-here>
```

Get an API key from one of:

- The dashboard: open `http://localhost:21847/dashboard` → **API Keys** →
  mint a new key.
- Your `.env` file: `grep CIX_API_KEY /path/to/code-index/.env | cut -d= -f2`.

Verify the CLI can reach the server:

```bash
cix list   # should show projects (or "no projects yet")
```

> [!IMPORTANT]
> If the CLI is not configured (or the server is unreachable), Claude
> will see error output from `cix` commands. The plugin can't paper
> over a missing server — it's a thin wrapper, not a replacement.

### 3. Claude Code v2.1.0 or later

The plugin uses `hookSpecificOutput.additionalContext` for hook-driven
nudges, which requires Claude Code 2.1.0+. Check with `claude --version`.

---

## Installation

There are three install paths depending on your scenario.

### Option A — From GitHub (recommended for end users)

Once the plugin is merged to `main`:

```bash
claude plugin marketplace add dvcdsys/code-index --sparse .claude-plugin plugins
claude plugin install cix@code-index --scope user
```

The `--sparse` flag limits checkout to the plugin directories
(`.claude-plugin/` + `plugins/`), avoiding a full clone of the server,
CLI source, and dashboard build.

### Option B — From a specific branch or tag (testing / pinned versions)

```bash
# From a branch (e.g. testing a feature branch)
claude plugin marketplace add dvcdsys/code-index@feat/claude-code-plugin \
  --sparse .claude-plugin plugins

# From a tag (pinned release)
claude plugin marketplace add dvcdsys/code-index@plugin/v0.1.0 \
  --sparse .claude-plugin plugins

claude plugin install cix@code-index --scope user
```

### Option C — From a local clone (plugin development)

```bash
git clone https://github.com/dvcdsys/code-index
claude plugin marketplace add /absolute/path/to/code-index
claude plugin install cix@code-index --scope user
```

### Choosing the scope

`--scope user` (default in our examples) — plugin available in every
project. Stored in `~/.claude/settings.json`. **Recommended for personal
use.**

`--scope project` — committed to `.claude/settings.json`, shared with
teammates via git. Good for team-wide enable.

`--scope local` — stored in `.claude/settings.local.json`, gitignored.
Only the current project. Useful when testing.

After install, restart Claude Code (or run `/reload-plugins` in an
existing session) so hooks register.

---

## Verification

```bash
# Plugin is installed and enabled
claude plugin list
# Expected output (excerpt):
#   ❯ cix@code-index
#     Version: 0.1.0
#     Scope: user
#     Status: ✔ enabled

# Both manifests pass official validation
claude plugin validate $(claude plugin list --json | jq -r '.[] | select(.id=="cix@code-index").installPath')

# The bundled CLI wrapper works
$(claude plugin list --json | jq -r '.[] | select(.id=="cix@code-index").installPath')/bin/cix --version
# → cix v0.X.Y
```

Then in a Claude Code session inside a cix-indexed project:

1. Type `/cix` and check autocomplete shows 6 commands (`search`, `def`,
   `refs`, `init`, `status`, `summary`).
2. Run `/cix:status` — should print `cix status` output.
3. Ask Claude something semantic ("find the auth middleware") and watch
   whether it reaches for `cix search` or falls back to Grep.

---

## Uninstall

### Plugin only (keep marketplace registered)

```bash
claude plugin uninstall cix@code-index --scope user
```

### Plugin + marketplace + cache (full cleanup)

```bash
claude plugin uninstall cix@code-index --scope user
claude plugin marketplace remove code-index
rm -rf ~/.claude/plugins/cache/code-index   # belt-and-suspenders
```

This does **not** uninstall the `cix` CLI itself or stop the cix server
— those are independent. Remove them separately if needed:

```bash
# Remove the CLI binary if you used the plugin's bootstrap install
rm ~/.local/bin/cix

# Stop the server
docker compose down              # CPU mode
docker compose -f docker-compose.cuda.yml down   # CUDA mode
launchctl unload ~/Library/LaunchAgents/com.cix.server.plist   # native macOS
```

### Troubleshooting "wrong scope" errors

If `claude plugin uninstall` complains the plugin is in a different
scope, check the master state file:

```bash
jq '.plugins["cix@code-index"]' ~/.claude/plugins/installed_plugins.json
```

This shows every install (with `scope` and, for local installs,
`projectPath`). For local-scope uninstall, `cd` to the registered
`projectPath` first, then re-run the uninstall.

---

## Configuration

Most plugin behavior is automatic. The few env vars you can set:

| Variable | Default | Effect |
|---|---|---|
| `CIX_PLUGIN_BIN_DIR` | `$HOME/.local/bin` | Where the wrapper installs `cix` if it isn't on PATH yet. |

The CLI config (`~/.cix/config.yaml`) is **separate** from the plugin —
the plugin doesn't write to it. Configure the CLI once (see
[Prerequisites](#prerequisites)) and the plugin will use that config.

### Per-project trigger threshold

The plugin nudges Claude in projects that `cix status` reports as
**indexed**. The check runs once per session at SessionStart (against
the cix-server) and is cached for the remainder of the session.
PreToolUse(Grep|Glob) only ever reads the cache — it never makes its
own `cix` calls.

If the cix-server is unreachable at session start, the project is
locked into "silent" mode for the rest of the session. Restart Claude
Code (Cmd+Q + reopen, or `/reload` in CLI) once the server is back to
re-evaluate.

---

## What the plugin does NOT do

To keep the v0.1 surface focused, the plugin intentionally excludes:

- **MCP server** — cix isn't exposed as an MCP tool yet (planned for v0.2).
  This means the plugin works in Claude Code (CLI + Code mode in Claude
  Desktop) but **not** in pure Claude Desktop chat sessions, which only
  consume MCP servers.
- **Server lifecycle management** — the plugin will not start, stop, or
  configure your `cix-server`. That's intentional: the server is shared
  infrastructure (one server can index many projects, used by many
  agents and IDEs), not a per-plugin concern.
- **CLI configuration UI** — `cix config set` is the source of truth.
  The plugin reads it but doesn't replace it.

---

## Troubleshooting

### "cix: command not found" inside Claude Code

The plugin's `bin/` should be on `PATH` while the plugin is enabled.
Check:

```bash
claude plugin list   # plugin enabled?
ls -la ~/.claude/plugins/cache/code-index/cix/*/bin/cix   # symlink exists?
```

If the symlink is missing, reinstall:
`claude plugin uninstall cix@code-index --scope user && claude plugin install cix@code-index --scope user`.

### Hooks silent in indexed project

The hooks rely on `cix status` succeeding at SessionStart. Verify:

```bash
cix status -p $(pwd)        # must exit 0
echo "exit=$?"
```

If `cix status` fails:
- Server unreachable: `curl http://localhost:21847/health`
- API key not set: `cix config show`
- Project not registered: `cix init`

Once `cix status` exits 0, **restart the Claude Code session** —
SessionStart cached the previous "not indexed" verdict and the
PreToolUse hook reads only that cache. There's no inline retry by
design (a flaky server shouldn't cause intermittent nudges).

To inspect the current verdict from outside Claude Code:

```bash
ls -la ~/.claude/plugins/data/cix-code-index/cix-aware-*
cat ~/.claude/plugins/data/cix-code-index/cix-aware-<your-session-id>
# "1" → nudges allowed; "0" → silent
```

### Hooks too loud / too quiet

Edit `scripts/grep-nudge.sh` in your local clone of the plugin, change
the power-of-2 check to your taste, and reinstall. Default schedule
(1, 2, 4, 8, 16, …) was chosen to balance "loud at start" with
"fade away".

### `cix` commands return errors at runtime

The CLI runs but the server is unreachable, or the API key is invalid.
Verify each step from [Prerequisites](#prerequisites):

```bash
curl http://localhost:21847/health
cix config show
cix list
```

### Two duplicate entries in `claude plugin list`

This usually means you installed the plugin in two scopes (e.g. `local`
plus `user`). Check the master state:

```bash
jq '.plugins["cix@code-index"]' ~/.claude/plugins/installed_plugins.json
```

Uninstall from the unwanted scope (you may need to `cd` to the
registered `projectPath` for local-scope uninstall).

### Plugin installs but slash commands don't appear

Slash commands are loaded at session start. After install, run
`/reload-plugins` in an existing Claude Code session, or quit and
re-open Claude Code.

---

## Security & testing

The plugin runs bash scripts on every Claude Code session, with calls
that include `find -delete` and writes to `$CLAUDE_PLUGIN_DATA`. Three
defensive layers protect against accidental damage:

1. **Path validation guards.** Before any deletion, `session-start.sh`
   and `session-end.sh` check that `$CLAUDE_PLUGIN_DATA` falls inside
   one of the whitelisted prefixes:
   - `$HOME/.claude/plugins/data/*` (the official plugin-data dir)
   - `/tmp` or `/tmp/*`
   - `$TMPDIR/*` (macOS test sandboxes)

   If the cache dir is outside this whitelist (e.g. `/`, `$HOME`,
   `/etc`), the script prints a refusal message and exits non-zero
   without touching anything.

2. **Restrictive `find` patterns.** Every `find -delete` uses
   `-maxdepth 1`, `-type f`, and a tight `-name` filter
   (`cix-aware-*` / `cix-grep-count-*`). Subdirectories, symlinks,
   and unrelated files are never touched, even within the whitelisted
   cache dir. We deliberately avoid `rm -rf` anywhere in the plugin.

3. **Automated test suite.** `plugins/cix/tests/` contains 46
   [bats-core](https://bats-core.readthedocs.io/) tests covering all 6
   hook scripts. The test matrix includes adversarial cases:
   - `CLAUDE_PLUGIN_DATA=/`, `=$HOME`, `=/etc` — guard must refuse
   - `session_id` containing shell metacharacters — must not inject
     commands (canary file survives)
   - Other sessions' cache files — must not be touched
   - Random non-cix files in cache dir — must not be touched
   - 30-day GC — must spare files outside the cix-prefixed patterns
   - Path-with-spaces project dirs — must hash correctly

   GitHub Actions runs the suite on Ubuntu and macOS for every PR
   that touches `plugins/cix/` or `.claude-plugin/`. ShellCheck runs
   alongside, gating warnings.

To run tests locally:

```bash
brew install bats-core jq shellcheck     # macOS
sudo apt-get install bats jq shellcheck  # Debian / Ubuntu

bats plugins/cix/tests/*.bats
shellcheck --severity=warning plugins/cix/scripts/*.sh
```

See [`plugins/cix/tests/README.md`](plugins/cix/tests/README.md) for
the full test matrix and instructions for adding new cases.

## Roadmap

**v0.2** (after v0.1 feedback):

- **MCP server** exposing `cix_search`, `cix_definitions`, `cix_references`
  as native Claude tools, so cix becomes available in Claude Desktop chat
  and any other MCP-compatible client.
- **`PreToolUse(Bash)` hook** that catches inline `grep` calls (not just
  the `Grep` tool) and routes them through cix where appropriate.
- **`cix-explorer` subagent** preconfigured for codebase exploration tasks
  (`Skill: cix` + read-only tool whitelist + `context: fork`).

**v0.3+:** auto-`cix init` on first use, hot-reload of the skill from
git after a `cix reindex`, distribution as an officially-listed plugin
in `claude-plugins-official`.

---

## License

MIT — same as the parent project.
