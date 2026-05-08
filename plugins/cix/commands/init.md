---
description: Initialize the cix index for the current project (registers, indexes, starts file watcher)
allowed-tools: Bash(cix *)
---

Initialize the cix index for the current project. This registers the
project with the cix server, performs a full initial index, and starts
the file-watcher daemon for auto-reindex on changes.

```!
cix init
```

If the indexing run is in-progress, you can monitor it with `/cix:status`.
If it fails, common causes are: cix-server not reachable, missing
`CIX_API_KEY` env var, or `~/.cix/data` permission issues. Check
`cix status` for details.
