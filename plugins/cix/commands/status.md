---
description: Show cix indexing status and file-watcher state for the current project
allowed-tools: Bash(cix *)
---

Show the current cix indexing status — last sync, number of indexed
files, and whether the file watcher is active.

```!
cix status
```

If `Watcher: ✗ not running`, search results may be stale. Run
`cix watch` to restart the auto-reindex daemon, or `cix reindex` for a
one-off refresh.
