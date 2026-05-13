---
description: Semantic code search via cix — find code by meaning, not by exact strings
argument-hint: <query>
allowed-tools: Bash(cix *)
---

Run a semantic search through the cix index for the query: **$ARGUMENTS**

```!
cix search "$ARGUMENTS"
```

Summarize the most relevant matches above. If results look weak, try:
- A more specific phrasing that names the area or symbol
- `cix search "$ARGUMENTS" --min-score 0.2` to lower the relevance floor
- `cix search "$ARGUMENTS" --in <subdir>` to narrow scope

If `cix` is not yet initialized in this project, run `/cix:init` first.
