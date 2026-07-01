---
description: List a directory (one level) in an external cix project's server-side checkout
argument-hint: "[dir] -n <external-project>"
allowed-tools: Bash(cix *)
---

List one level of a directory (ls-like, no recursion) in the server-side
checkout of an **external (GitHub-backed)** cix project: **$ARGUMENTS**

```!
cix tree $ARGUMENTS
```

This works only for external / workspace repos the server keeps on disk — use it
to navigate the file tree of a repo you don't have locally before reading a file
with `/cix:file`. For the **current / local** project, use the native `ls`/Read
tools instead.

Tips:
- Target the repo with `-n github.com/owner/repo@main` (from `cix list`).
- Omit the directory argument to list the repository root.
