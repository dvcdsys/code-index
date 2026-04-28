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