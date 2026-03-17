# Skills

## cix — Semantic Code Search

Teaches an AI agent how to use `cix` for code navigation instead of Grep/Glob.

### Install

```bash
cp -r skills/cix ~/.claude/skills/cix
```

### Usage

In a Claude Code session:

```
/cix
```

Loads search guidance into context. Claude will use `cix search` instead of Grep/Glob for the rest of the session.

To activate automatically in every session, add `cix` usage instructions to `~/.claude/CLAUDE.md` (see the [Agent Integration](../README.md#agent-integration) section in the main README).