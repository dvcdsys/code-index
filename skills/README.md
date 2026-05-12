# Skills

## cix — Semantic Code Search

Teaches an AI agent when to reach for `cix` (semantic, cross-file,
exploratory) versus Grep / Glob / Read (exact strings, known pointers,
non-code files).

### Install

```bash
cp -r skills/cix ~/.claude/skills/cix
```

### Usage

In a Claude Code session:

```
/cix
```

Loads navigation guidance into context for the rest of the session.

To activate automatically in every session, add `cix` usage instructions
to `~/.claude/CLAUDE.md` (see the [Agent Integration](../README.md#agent-integration)
section in the main README).

---

## cix-workspace — Cross-Project Research

Structures the agent's workflow for tasks that touch more than one
repo: how to identify which repos are in scope, how to investigate
them (single-project search or parallel sub-agent fan-out), and how
to synthesize a per-repo change plan. Includes a worked retro on the
"add sell flow to XYZ" failure mode that motivated the hybrid
BM25+dense algorithm.

The skill answers three questions per request:

1. Which repos does this request touch?
2. Which code in those repos is relevant?
3. What changes need to land, and in what order?

It also handles the *primary project* nuance — the agent is usually
`cd`'d into a specific repo, and the user's task is rooted there; the
workspace is for the surrounding context.

### Install

```bash
cp -r skills/cix-workspace ~/.claude/skills/cix-workspace
```

### Usage

In a Claude Code session:

```
/cix-workspace
```

Loads the cross-project research workflow into context. Pair with
`/cix` for the single-repo navigation guidance.

To activate automatically when the user's request looks cross-cutting,
mention `cix-workspace` in your `~/.claude/CLAUDE.md` alongside the
`cix` instructions.