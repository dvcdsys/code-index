---
description: Find symbol references via cix — locate every usage of a symbol across the codebase
argument-hint: <symbol> [--file <path>] [--limit <n>]
allowed-tools: Bash(cix *)
---

Find references to the symbol **$ARGUMENTS** in the cix index:

```!
cix references $ARGUMENTS
```

Group the references by file and call out any high-traffic call sites or
suspicious usage patterns. If you need fewer results, add `--limit 20`.
