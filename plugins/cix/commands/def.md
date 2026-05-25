---
description: Find symbol definition(s) via cix — go-to-definition across the indexed codebase
argument-hint: <symbol> [--kind function|class|method|type] [--file <path>]
allowed-tools: Bash(cix *)
---

Look up the definition of the symbol **$ARGUMENTS** in the cix index:

```!
cix definitions $ARGUMENTS
```

If multiple matches are returned, point out the most likely one based on
context. If nothing is found, suggest `cix symbols $ARGUMENTS` for a
broader name search.
