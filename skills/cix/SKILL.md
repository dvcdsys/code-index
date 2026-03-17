---
name: cix
description: Semantic code search and navigation using the cix index. Use BEFORE Grep/Glob/Read for faster, smarter code discovery. Covers search, definitions, references, symbols, files, and indexing.
user-invocable: true
---

# Code Index (`cix`) — Semantic Code Search & Navigation

You have access to `cix`, a semantic code index that understands your codebase. It uses embeddings and AST parsing to provide intelligent search — **always prefer `cix` over Grep/Glob** when looking for code.

## Why use `cix` first?

1. **Saves tokens** — returns only relevant snippets, not entire files
2. **Understands meaning** — "authentication middleware" finds auth code even if those words aren't in the source
3. **Structured navigation** — go-to-definition and find-references like an IDE
4. **Fast** — pre-indexed, no filesystem scanning needed

## Search priority

1. `cix search` or `cix symbols` — FIRST choice
2. `cix definitions` / `cix references` — for navigation
3. Grep/Glob — only if `cix` returns no results or is unavailable

---

## Commands Reference

### Semantic Search — find code by meaning
```bash
cix search "authentication middleware"
cix search "database connection retry logic"
cix search "error handling in payment flow" --limit 20
cix search "config parsing" --in ./internal/config/
cix search "API routes" --lang go
cix search "validation" --in ./api --lang python
```

**Flags:**
- `--in <path>` — restrict to file or directory (can repeat)
- `--lang <language>` — filter by language (can repeat)
- `--limit <n>` — max results (default: 10)
- `--min-score <f>` — minimum relevance 0.0-1.0 (default: 0.1)

### Go to Definition — find where a symbol is defined
```bash
cix definitions HandleRequest
cix def AuthMiddleware --kind function
cix goto UserService --kind class
cix def Config --file ./internal/config.go
```

**Aliases:** `definitions`, `def`, `goto`

**Flags:**
- `--kind <type>` — filter: function, class, method, type
- `--file <path>` — narrow to specific file
- `--limit <n>` — max results (default: 10)

### Find References — find where a symbol is used
```bash
cix references HandleRequest
cix refs AuthMiddleware --limit 50
cix usages UserService --file ./internal/api/
```

**Aliases:** `references`, `refs`, `usages`

**Flags:**
- `--file <path>` — narrow to specific file
- `--limit <n>` — max results (default: 30)

### Symbol Search — find symbols by name
```bash
cix symbols handleRequest
cix symbols User --kind class
cix symbols Auth --kind function --kind method
```

**Flags:**
- `--kind <type>` — filter: function, class, method, type (can repeat)
- `--limit <n>` — max results (default: 20)

### File Search — find files by path pattern
```bash
cix files "config"
cix files "middleware" --limit 20
```

### Project Overview
```bash
cix summary        # languages, directories, key symbols
cix status         # indexing status, file counts
cix list           # all indexed projects
```

### Indexing
```bash
cix init [path]         # register + index + start watcher
cix reindex             # incremental (only changed files)
cix reindex --full      # full reindex from scratch
cix watch               # start auto-reindex daemon
cix watch stop          # stop daemon
```

---

## Usage Patterns

### Exploring unfamiliar code
```bash
cix summary                           # understand project structure
cix search "main entry point"         # find where it starts
cix search "database" --limit 20      # find all DB-related code
```

### Finding specific functionality
```bash
cix search "JWT token validation"     # semantic — finds by meaning
cix symbols "Validate" --kind function  # exact name lookup
cix def ValidateToken                 # jump to definition
cix refs ValidateToken                # find all callers
```

### Understanding a symbol
```bash
cix def HandleRequest                 # where is it defined?
cix refs HandleRequest                # who calls it?
cix search "HandleRequest error"      # how are errors handled?
```

### Narrowing scope
```bash
cix search "middleware" --in ./api/    # only in api directory
cix search "config" --in ./cmd/       # only in cmd directory
cix refs Config --file ./internal/server.go  # only in one file
```

---

## Tips

- Search queries are natural language — write what you're looking for, not regex
- `cix def` is faster than `cix symbols` for exact name matches
- `cix refs` finds usages across the entire codebase in indexed chunks
- Use `--in` to avoid noise from irrelevant directories
- The index auto-updates via file watcher — no need to manually reindex
- If results seem stale, run `cix reindex`