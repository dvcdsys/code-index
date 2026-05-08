# TODO / Roadmap

Tracked deferred work for the cix project. Items here are deliberately
postponed — typically because they require data from real-world usage,
need a separate design pass, or sit outside the current release scope.

When you start an item, link the PR/branch in the relevant section.

---

## Plugin v0.2

### `PostToolUseFailure` hook for `Bash(cix *)` — graceful degradation when cix-server is unreachable

**Status:** designed, not implemented.

**Problem.** During a Claude Code session the cix-server can become
unreachable mid-flight (Docker restart, OOM, OS sleep, network blip).
The plugin's `SessionStart` hook ran successfully at session start and
cached `cix-aware = "1"` for the project. PreToolUse(Grep|Glob) keeps
nudging the model to use `cix search`. The model dutifully runs `cix
search …`, the CLI exits non-zero, and the model sees an error. Plugin
keeps nudging on the next Grep — model retries cix — fails again. Loop.

**Why we're deferring.** The edge case is rare on a stable local server,
the loop is annoying but not destructive (model picks up after a couple
of failures and switches back to Grep), and the manual workaround
(restart Claude Code session, or set the cache file to `0`) is trivial.
We'd rather collect data on real failure rates from v0.1 before adding
more state machinery.

**The interactive-prompt question.** Initial intent was an actual UI
dialog: "cix-server unreachable. Disable cix nudges for this session?
[Yes] [No]". After investigating Claude Code's hook API, this isn't
available — `permissionDecision: "ask"` only works for `PreToolUse`
events, and `PostToolUseFailure` does not accept it. There's no
mechanism for a hook to trigger an arbitrary user prompt.

**Functional equivalent that IS available:** `PostToolUseFailure` can
return `hookSpecificOutput.additionalContext`. We can use that to
1. Overwrite `$CLAUDE_PLUGIN_DATA/cix-aware-$SESSION_ID-$DIR_HASH`
   with `"0"` — silencing all subsequent PreToolUse(Grep|Glob) and
   PostCompact hooks for the rest of the session in this project.
2. Inject a one-line message via `additionalContext`:
   > 💡 cix command failed (server unreachable). Disabled cix nudges
   > for this session. Run `cix status` and restart Claude Code if
   > you've fixed the server.

The model relays this to the user in its next response. Effect is
identical to "user clicked Yes on a Disable dialog": plugin goes silent,
user is informed and decides what to do. No actual interactive UI, but
the developer experience is the same.

**Implementation sketch:**

1. New script `plugins/cix/scripts/cix-failed.sh` — reads `session_id`,
   computes `DIR_HASH`, overwrites the cache file with `"0"`, emits
   the JSON message.
2. Register it in `plugins/cix/hooks/hooks.json`:
   ```json
   "PostToolUseFailure": [
     {
       "matcher": "Bash",
       "hooks": [
         {
           "type": "command",
           "command": "${CLAUDE_PLUGIN_ROOT}/scripts/cix-failed.sh"
         }
       ]
     }
   ]
   ```
3. Inside the script, parse `tool_input.command` from stdin and exit
   silently if it doesn't start with `cix ` — so unrelated Bash
   failures don't trigger the disable path.
4. Idempotent: if cache is already `"0"`, no-op (avoid re-injecting the
   message on every subsequent failure).

**Ship criteria.** Wait for at least one user report (or one self-observed
incident) where v0.1 plugin loops on cix failures before implementing.
Otherwise we're solving a phantom problem.

**Estimate:** ~1 day. ~50 lines of bash, hooks.json registration, doc
updates in `CLAUDE-CODE-PLUGIN.md`, manual test scenario covering
`docker compose stop` mid-session.

---

### Other deferred plugin work

- **MCP server** exposing `cix_search` / `cix_definitions` / `cix_references`
  as native Claude tools, so cix becomes available in pure Claude
  Desktop chat (where plugins don't run).
- **`PreToolUse(Bash)` matcher** that catches inline `grep` calls
  (`Bash(grep ...)`) — currently the plugin only nudges on the dedicated
  `Grep`/`Glob` tools, not on `grep` invoked through `Bash`.
- **`cix-explorer` subagent** preconfigured for codebase exploration —
  `Skill: cix` preloaded + `context: fork` + `agent: Explore` + read-only
  tool whitelist.
- **Plugin tag stream + `release-plugin.yml` workflow** so the plugin
  has its own version tags (`plugin/v0.1.0`, `plugin/v0.2.0`, …)
  alongside `cli/v*` and `server/v*`.

---

## Server / CLI

(none currently tracked here; server and CLI roadmap is in their
respective changelogs and release-server.yml / release-cli.yml workflows)
