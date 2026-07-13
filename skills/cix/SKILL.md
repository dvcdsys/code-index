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

## Pick the cheapest tool that answers the question

When you already know a symbol's **name**, reach for `cix def` / `cix refs`
before `cix search`. They return **metadata only** (file, line, signature,
call sites) — no source bodies — so they cost roughly an order of magnitude
fewer tokens. Measured on one real symbol in this codebase:

| Command | Returns | Output size |
|---|---|---|
| `cix def <symbol>`      | definition location + signature        | ~250 B |
| `cix refs <symbol>`     | every call site (file:line)             | ~1 KB  |
| `cix search "<intent>"` | matching code **with full source bodies** | ~7 KB  |

So `cix search` is ~28× the bytes of `cix def` and ~6× `cix refs` for the
same target. Rule of thumb:

- Know the name, want "where is it defined / who calls it" → `cix def` /
  `cix refs`. Cheap, precise, no source noise.
- Don't know the name, searching by *meaning* → `cix search`.
- Only escalate to `cix search` for a *known* symbol when you actually need
  to read the surrounding implementation, not merely locate it.

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
- `--limit <n>` — max **files** returned (CLI default: 10) — output is
  grouped per file with all matches inside, so 10 files ≈ many snippets.
  **For agent use, prefer `--limit 5`**: five files is enough for most
  lookups and keeps the result compact. This is a usage recommendation,
  not a change to the CLI default — bump it back up when you genuinely
  need broader exploration.
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

### Read Files & List Directories — EXTERNAL repos only
```bash
cix file internal/httpapi/server.go -n github.com/owner/repo@main   # whole file
cix file README.md --lines 1:40 -n github.com/owner/repo@main       # line range
cix tree -n github.com/owner/repo@main                              # repo root, one level
cix tree internal/httpapi -n github.com/owner/repo@main             # a subdir
```

Reads file contents / the file tree from the server's checkout of an
**external (GitHub-backed)** repo — for inspecting repos you don't have locally
(workspace / external projects). **Only for OTHER repos:** for the current /
local project use native `Read`/`Grep`/`ls` (faster, no server round-trip); on a
local project these return an error saying so. `--lines` is 1-based inclusive.

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

### Servers — talk to more than one cix backend

`cix` can be configured with several **named servers** (e.g. a local
box and a remote corporate server). One is the **default**; every
command targets the default unless you pass `--server <alias>`.

```bash
cix config show                                # lists servers; * marks the default
cix --server corporate search "rate limiter"   # run any command against a named server
cix search "rate limiter" --server corporate   # --server is global; either position works
```

Servers are managed through `cix config` (persisted in
`~/.cix/config.yaml`):

```bash
cix config set server.corporate.url https://cix.corp.internal
cix config set server.corporate.key <bearer-token>
cix config set default_server corporate        # change which server is the default
cix config unset server.corporate              # remove a server
cix config unset server.corporate.key          # clear just its key
```

The legacy single-server keys still work and operate on the **default**
server, so existing setups keep working unchanged:
`cix config set api.url <url>` / `cix config set api.key <key>`. The
`--api-url` / `--api-key` flags override the selected server's URL/key
for a single invocation.

**Agent rule:** use the default server (no flag) unless the user names a
specific server. Only add `--server <alias>` when the task explicitly
targets that named backend; never guess an alias — run `cix config show`
to see the configured names if unsure.

### Workspaces — cross-repo search + management

A **workspace** groups several indexed repos into one corpus for
cross-project search. List / describe / search:

```bash
cix ws                                   # list workspaces
cix ws "<name>"                          # describe (repos + status)
cix ws "<name>" search "<query>"         # hybrid cross-repo search
```

Manage (owner/admin):

```bash
cix ws create "<name>" [--description "…"]   # create
cix ws "<name>" add <project…>               # link an indexed project (local or external)
cix ws "<name>" remove <project…>            # unlink
cix ws "<name>" rename "<new>"               # rename
cix ws "<name>" update [--name "<new>"] [--description "…"]
cix ws "<name>" delete [-y]                  # delete (prompts; -y skips)
```

`add`/`remove` take a project's absolute path, host_path
(`github.com/owner/repo@main`), or 16-hex `path_hash` (see `cix list`); no
arg defaults to the current directory. `add` links an **already-indexed**
project — it does not clone (to clone a new GitHub repo into a workspace,
use the dashboard). `delete` drops the workspace + its links, not the
projects.

For the full cross-project *research* workflow — which repos to trust, how
to read the hybrid bm25/dense scores — use the dedicated **`cix-workspace`**
skill (`/cix-workspace <task>`).

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
