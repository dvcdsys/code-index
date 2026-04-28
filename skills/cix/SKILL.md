---
name: cix
description: Semantic code search and navigation using the cix index. Reach for cix when you don't already know where to look. Covers search, definitions, references, symbols, files, and indexing.
user-invocable: true
---

# Code Index (`cix`) — Semantic Code Search & Navigation

You have access to `cix`, a semantic code index that understands the
codebase via embeddings + AST parsing. The right reflex is **"cix when
you don't have a pointer; grep when you do."**

## When to use which

**Reach for `cix` first when:**
- The starting point is open-ended ("how does indexing work?", "find the
  authentication middleware", "where is the main entry point?")
- You need cross-file navigation (definitions / references / callers)
- You're searching by *meaning*, not by an exact string
  (`"JWT validation"` should find `verifyToken` even without that phrase)
- You're exploring an unfamiliar package or codebase

**Skip `cix`, use Read / Grep / Glob directly when:**
- A failing test or stack trace already names the file and function —
  just `Read` it
- You're chasing an exact literal: a specific error message, a config
  key, a commit-message phrase, an import path
- You're inside dependencies (`node_modules`, `vendor`, `.venv`) — they
  aren't indexed
- You're editing a non-code file (Dockerfile, yaml, lockfile)

If `cix` returns nothing relevant after one well-formed query, fall
back to grep — don't loop on cix.

---

## Commands Reference

### Semantic Search — find code by meaning
```bash
cix search "authentication middleware"
cix search "database connection retry logic"
cix search "error handling in payment flow" --limit 20
cix search "config parsing" --in ./internal/config/
cix search "API routes" --lang go
cix search "main entry point" --exclude bench/fixtures --exclude legacy
```

**Flags:**
- `--in <path>` — restrict to file or directory (can repeat)
- `--exclude <path>` — drop a directory or substring from results (can repeat)
- `--lang <language>` — filter by language (can repeat)
- `--limit <n>` — max **files** returned (default: 10) — output is
  grouped per file with all matches inside, so 10 files ≈ many snippets
- `--min-score <f>` — minimum relevance 0.0–1.0 (default: **0.4**)

### Go to Definition — find where a symbol is defined
```bash
cix definitions HandleRequest
cix def AuthMiddleware --kind function
cix def Config --file ./internal/config.go
```
Aliases: `definitions`, `def`, `goto`. Flags: `--kind`, `--file`, `--limit`.

### Find References — find where a symbol is used
```bash
cix references HandleRequest
cix refs AuthMiddleware --limit 50
cix usages UserService --file ./internal/api/
```
Aliases: `references`, `refs`, `usages`. Flags: `--file`, `--limit`.

### Symbol Search — find symbols by name
```bash
cix symbols handleRequest
cix symbols User --kind class
cix symbols Auth --kind function --kind method
```
Flags: `--kind` (function/class/method/type, repeatable), `--limit`.

### File Search — find files by path pattern
```bash
cix files "config"
cix files "middleware" --limit 20
```

### Project Overview
```bash
cix summary        # languages, top dirs, key symbols
cix status         # indexing status + file watcher status
cix list           # all indexed projects
```

### Indexing
```bash
cix init [path]         # register + index + start watcher
cix reindex             # incremental
cix reindex --full      # full reindex
cix cancel              # cancel an in-flight indexing run
cix watch               # start file-change auto-reindex daemon
cix watch stop          # stop daemon
```

The watcher auto-reindexes on file change — manual `reindex` is rarely
needed. `cix status` shows whether the watcher is running and the
last-sync timestamp.

---

## Search quality — what scores mean

Default `--min-score 0.4` is calibrated for the production embedding
model (CodeRankEmbed-Q8 with path-aware preamble). Rough landscape:

| Score    | Meaning                                                 |
|----------|---------------------------------------------------------|
| 0.65+    | Exact / very strong match — almost certainly relevant   |
| 0.50–0.65| Strong match — usually relevant                         |
| 0.40–0.50| Weaker match — sometimes useful, sometimes not          |
| <0.40    | Noise — filtered out by default                         |

**If a query returns nothing**, lower the floor explicitly:
`--min-score 0.2` for very specific or long-tail queries. Don't drop
below 0.2 — results below that are noise.

---

## Writing better queries — leverage path-aware embedding

Each chunk is embedded with its file path, language, and symbol name in
the preamble. This means **mentioning a file/dir/symbol you already
know about boosts ranking**:

```bash
# Generic
cix search "validation"
# Better — pins the search to the auth area
cix search "validation in auth middleware"
# Even better when you know the symbol
cix search "ValidateToken" --kind function
```

Natural-language queries that name the *kind of thing* and *where it
lives* outperform single-word queries.

---

## Usage Patterns

### Exploring unfamiliar code (`cix`'s strongest case)
```bash
cix summary                              # project structure, top dirs
cix search "main entry point server"     # find where it starts
cix search "database connection setup"   # find DB wiring
cix search "request handler" --in ./api  # narrow to API
```

### Tracing a symbol end-to-end
```bash
cix def HandleRequest        # where is it defined?
cix refs HandleRequest       # who calls it?
cix search "HandleRequest error handling"   # how are errors handled?
```

### Chasing a known target (often grep is enough)
```bash
# Stack trace says "internal/auth/middleware.go:42 — invalid token"
# → just Read that file. No cix needed.

# Config key "max_concurrent_requests" used somewhere?
# → grep is more precise.
```

### Narrowing scope
```bash
cix search "middleware" --in ./api/
cix search "config" --in ./cmd/ --exclude legacy
cix refs Config --file ./internal/server.go
```

---

## Tips

- Search queries are natural language, not regex. Write what you'd ask
  a colleague.
- Output groups by file: each result line is a file with all relevant
  matches inside, ordered top-to-bottom by line number. The
  `[best 0.NN]` is the score of the top hit in that file.
- `cix def` is a faster path than `cix symbols` when you already know
  the exact name.
- `--exclude` complements `--in` — use it to drop noisy dirs (`bench/`,
  `legacy/`, vendored code) inline without touching `.cixignore`.
- The watcher keeps the index fresh. If results feel stale, check
  `cix status` first — `Watcher: ✗ not running` is the usual cause.
- Don't loop. If a query returns nothing useful after one well-phrased
  attempt + one `--min-score 0.2` retry, drop to grep.
