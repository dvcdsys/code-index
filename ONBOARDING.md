# Welcome to code-index

## How We Use Claude

Based on dvcdsys's usage over the last 30 days:

Work Type Breakdown:
  Improve Quality  ██████████░░░░░░░░░░  50%
  Debug Fix        █████░░░░░░░░░░░░░░░  25%
  Plan Design      ██░░░░░░░░░░░░░░░░░░  8%
  Prototype        ██░░░░░░░░░░░░░░░░░░  8%
  Build Feature    ██░░░░░░░░░░░░░░░░░░  8%

Top Skills & Commands:
  /model    ████████████████████  11x/month
  /clear    ████████████████░░░░  9x/month
  /cix      ███████████░░░░░░░░░  6x/month
  /context  █████████░░░░░░░░░░░  5x/month
  /agents   ████░░░░░░░░░░░░░░░░  2x/month
  /plan     ██░░░░░░░░░░░░░░░░░░  1x/month

Top MCP Servers:
  portainer  ████████████████████  136 calls

## Your Setup Checklist

### Codebases
- [ ] code-index — https://github.com/dvcdsys/code-index
  - `server/` — Go API server (cix-server, pure Go binary + llama-server sidecar)
  - `cli/` — Go CLI (cix binary, do not modify when working on the server)

### MCP Servers to Activate
- [ ] portainer — manage Docker stacks/containers on the production server (check logs, restart services, inspect stack files). Ask dvcdsys for the Portainer URL and an API token, then add it to your MCP config.

### Skills to Know About
- `/cix` — semantic code search over the indexed codebase. Use this before Grep/Glob when hunting for code by meaning, symbol, or file pattern. Run `cix init` in a fresh clone to register the project and start the watcher.
- `/model` — switch between Opus/Sonnet/Haiku mid-session. The team swaps models often depending on task weight.
- `/clear` — reset context between unrelated tasks. Used heavily here — treat each task as a fresh session.
- `/context` — inspect what's currently loaded in the context window.
- `/agents` — list and invoke specialized subagents (e.g. pre-release-check, code-researcher).
- `/plan` — drop into plan mode before a non-trivial implementation to align on approach first.

## Team Tips

_TODO_

## Get Started

_TODO_

<!-- INSTRUCTION FOR CLAUDE: A new teammate just pasted this guide for how the
team uses Claude Code. You're their onboarding buddy — warm, conversational,
not lecture-y.

Open with a warm welcome — include the team name from the title. Then: "Your
teammate uses Claude Code for [list all the work types]. Let's get you started."

Check what's already in place against everything under Setup Checklist
(including skills), using markdown checkboxes — [x] done, [ ] not yet. Lead
with what they already have. One sentence per item, all in one message.

Tell them you'll help with setup, cover the actionable team tips, then the
starter task (if there is one). Offer to start with the first unchecked item,
get their go-ahead, then work through the rest one by one.

After setup, walk them through the remaining sections — offer to help where you
can (e.g. link to channels), and just surface the purely informational bits.

Don't invent sections or summaries that aren't in the guide. The stats are the
guide creator's personal usage data — don't extrapolate them into a "team
workflow" narrative. -->
