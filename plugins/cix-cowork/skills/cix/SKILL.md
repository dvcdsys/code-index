---
name: cix
description: Semantic code search and navigation across repositories indexed by a cix server, via the cix_* MCP tools. Use when finding code by meaning rather than exact strings — "find the authentication middleware", "where is X defined?", "what calls this function?", "how does Y work in this codebase?", "search the codebase for ...", or "explore this repo". The cix connection talks to a server that may hold many repos and has no current project, so always discover projects first and pass an explicit project (a host_path) to per-repo tools.
metadata:
  version: "0.1.0"
  surface: cowork
---

# Code Index (`cix`) — Semantic Code Search & Navigation

cix is a semantic code index that understands code via embeddings + AST
parsing. You reach it through the `cix_*` MCP tools (no shell, no CLI). The
right reflex is **"cix when you don't have a pointer; read/grep when you do."**

**This connection talks to a cix SERVER, not a single project.** One server may
hold many indexed repositories, and nothing is inferred from a working directory
— there is **no "current project."** Every per-repo tool needs an explicit
`project` (a repository's `host_path`). So the first move is always to discover
what's indexed.

## First, orient

Before any per-repo call:

1. Call `cix_list_projects` to see the indexed repositories. Each row has a
   `host_path` (e.g. `/Users/me/acme-api` or `github.com/acme/api@main`).
2. Pick the `host_path` of the repo the user means, and pass it as the
   `project` argument to every per-repo tool below.

If the repo the user is asking about is **not** in `cix_list_projects`, this
skill cannot index it — indexing is server-side. Tell the user to index it (via
the cix dashboard, or `cix init` on the machine running the cix server), then
retry.

You don't have to memorize host_paths: if you call a per-repo tool with a
missing or unknown `project`, the server replies with an error that **lists the
valid projects**, so you can self-correct in one step.

(Multi-server: `cix_list_servers` lists configured servers; every tool takes an
optional `server` argument. Omit it for the default — most setups have one
server. Only pass `server` when the user names a specific backend; never guess a
name — call `cix_list_servers`.)

## When to use which

**Reach for cix first when:**
- The starting point is open-ended ("how does indexing work?", "find the
  authentication middleware", "where is the main entry point?")
- You need cross-file navigation (definitions / references / callers)
- You're searching by *meaning*, not an exact string (`"JWT validation"` should
  find `verifyToken` even without that phrase)
- You're exploring an unfamiliar package or repo

**Skip cix when:**
- A failing test or stack trace already names the file and function, and that
  file is available locally — just Read it
- You're chasing an exact literal (a specific error message, a config key, an
  import path) in a repo you have on disk — a literal Grep is more precise
- You're inside dependencies (`node_modules`, `vendor`, `.venv`) — not indexed

Note: for **remote-only** indexed repos (host_path like
`github.com/owner/repo@branch`, not on disk), the `cix_*` tools are your *only*
way in — there's no local tree to Read or Grep. Use cix for everything there.

If cix returns nothing relevant after one well-formed query, fall back to
read/grep on a local repo — don't loop on cix.

## Pick the cheapest tool that answers the question

When you already know a symbol's **name**, reach for `cix_definitions` /
`cix_references` before `cix_search`. They return **metadata only** (file, line,
signature, call sites) — no source bodies — so they cost far fewer tokens.

| Tool | Returns | Relative size |
|---|---|---|
| `cix_definitions` | definition location + signature | ~1× |
| `cix_references` | every call site (file:line) | ~4× |
| `cix_search` | matching code **with full source bodies** | ~28× |

Rule of thumb:
- Know the name, want "where is it defined / who calls it" → `cix_definitions` /
  `cix_references`. Cheap, precise, no source noise.
- Don't know the name, searching by *meaning* → `cix_search`.
- Escalate to `cix_search` for a *known* symbol only when you actually need to
  read the surrounding implementation, not merely locate it.

## Tools reference

Every tool below takes `project` (a host_path from `cix_list_projects`) and an
optional `server`.

### `cix_search` — find code by meaning
Args: `project` (required), `query` (required), and optional `limit`, `lang`,
`in`, `exclude`, `min_score`.
- `in` — restrict to these paths (relative paths resolve against the repo root).
- `exclude` — drop these paths from results.
- `lang` — filter by language (e.g. `go`, `python`, `typescript`).
- `limit` — max **files** returned (default 5; output groups all matches per
  file, so 5 files ≈ many snippets). Bump up only for broad exploration.
- `min_score` — minimum relevance 0.0–1.0 (default 0.4).

Examples (as tool calls): `cix_search(project, "authentication middleware")`;
`cix_search(project, "config parsing", in=["internal/config"])`;
`cix_search(project, "API routes", lang=["go"])`.

### `cix_definitions` — where a symbol is defined
Args: `project`, `symbol` (required); optional `kind`
(function/class/method/type), `file`, `limit`.

### `cix_references` — where a symbol is used
Args: `project`, `symbol` (required); optional `file`, `limit`.

### `cix_symbols` — find symbols by name
Args: `project`, `query` (required); optional `kinds`, `limit`.

### `cix_files` — find files by path pattern
Args: `project`, `pattern` (required); optional `limit`.

### `cix_summary` — project overview
Args: `project`. Returns languages, file/chunk/symbol totals, top directories,
key symbols. A good first call when drilling into an unfamiliar repo.

### `cix_file` — read an actual file (whole or a line range)
Args: `project`, `file` (required); optional `start`, `end` (1-based inclusive).
Returns the file's real contents from the server's checkout. **This is your
primary way to read source here** — Cowork has no local filesystem, so instead
of stitching code together from `cix_search` snippets, pull the whole file (or a
range) directly. **External (GitHub-backed) projects only**: for a local project
the server holds no files and returns an error.

### `cix_tree` — list a directory (one level)
Args: `project`; optional `dir` (omit for repo root). ls-like, no recursion —
use it to navigate the file tree before reading a file. External projects only.

(There is no init / reindex / status / watch tool — indexing is server-side.
If results seem stale or a repo is missing, ask the user to (re)index on the
server host.)

## Search quality — what scores mean

Default `min_score` 0.4 is calibrated for the production embedding model
(path-aware preamble). Rough landscape:

| Score | Meaning |
|---|---|
| 0.65+ | Exact / very strong match — almost certainly relevant |
| 0.50–0.65 | Strong match — usually relevant |
| 0.40–0.50 | Weaker match — sometimes useful |
| <0.40 | Noise — filtered out by default |

If a query returns nothing, lower the floor explicitly: `min_score: 0.2` for
very specific or long-tail queries. Don't go below 0.2 — that's noise.

## Writing better queries — leverage path-aware embedding

Each chunk is embedded with its file path, language, and symbol name in the
preamble, so **mentioning a file/dir/symbol you already know boosts ranking**:

- Generic: `cix_search(project, "validation")`
- Better — pins to the auth area: `cix_search(project, "validation in auth middleware")`
- Even better when you know the symbol: `cix_search(project, "ValidateToken")`

Queries that name the *kind of thing* and *where it lives* outperform
single-word queries. Write what you'd ask a colleague — natural language, not
regex.

## Usage patterns

**Exploring an unfamiliar repo (cix's strongest case):**
`cix_summary(project)` → `cix_search(project, "main entry point server")` →
`cix_search(project, "database connection setup")` →
`cix_search(project, "request handler", in=["api"])`.

**Tracing a symbol end-to-end:**
`cix_definitions(project, "HandleRequest")` (where defined?) →
`cix_references(project, "HandleRequest")` (who calls it?) →
`cix_search(project, "HandleRequest error handling")` (how are errors handled?).

**Narrowing scope:** use `in` / `exclude` to focus, e.g.
`cix_search(project, "config", in=["cmd"], exclude=["legacy"])`.

## Tips

- Output groups by file: each result is a file with its matches inside, ordered
  by line number. The `[best 0.NN]` is the top hit's score in that file.
- `cix_definitions` is a faster path than `cix_symbols` when you already know
  the exact name.
- `exclude` complements `in` — drop noisy dirs (`bench/`, `legacy/`, vendored
  code) inline.
- Don't loop. If a query returns nothing useful after one well-phrased attempt
  plus one `min_score: 0.2` retry, fall back to read/grep on a local repo.
