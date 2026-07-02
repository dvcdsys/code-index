---
description: Read a file (whole or a line range) from an external cix project's server-side checkout
argument-hint: <file> [-n <external-project>] [--lines N:M]
allowed-tools: Bash(cix *)
---

Read a file from the server-side checkout of an **external (GitHub-backed)**
cix project: **$ARGUMENTS**

```!
cix file $ARGUMENTS
```

This works only for external / workspace repos the server keeps on disk — the
way to read source from a repo you don't have locally. For the **current /
local** project, use the native Read tool instead (the file is already on your
machine; this command will return an error pointing you there).

Tips:
- Target the repo with `-n github.com/owner/repo@main` (from `cix list`).
- `--lines N:M` selects a 1-based inclusive range (`N`, `N:`, `:M` also work).
- To find the path first, use `/cix:search` or `cix files <pattern>`; to browse
  the tree, use `/cix:tree`.
