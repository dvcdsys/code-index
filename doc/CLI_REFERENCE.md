# CLI reference

Full command surface for the `cix` CLI. For config keys specifically,
`cix config keys` is the canonical runtime view and
[`CLI_CONFIG.md`](CLI_CONFIG.md) the prose reference.

## Project management

| Command | Description |
|---------|-------------|
| `cix init [path]` | Register + index + start file watcher |
| `cix status` | Show indexing status and progress |
| `cix list` | List all indexed projects |
| `cix reindex [--full]` | Trigger manual reindex |
| `cix cancel` | Cancel an in-flight indexing run |
| `cix summary` | Project overview: languages, directories, symbols |

## Search

```bash
# Semantic search — natural language, finds by meaning
cix search <query> [flags]
  --in <path>          restrict to file or directory (repeatable)
  --exclude <path>     exclude file or directory (repeatable)
  --lang <language>    filter by language (repeatable)
  --limit, -l <n>      max results (default: 10)
  --min-score <0-1>    minimum relevance score (default: 0.4)
  -p <path>            project path (default: cwd)

# Symbol search — fast lookup by name
cix symbols <name> [flags]
  --kind <type>        function | class | method | type (repeatable)
  --limit, -l <n>      max results (default: 20)

# Definition / reference navigation
cix definitions <symbol> [--kind <type>] [--file <path>] [--limit <n>]
cix references <symbol> [--file <path>] [--limit <n>]

# File search by path pattern
cix files <pattern> [--limit <n>]
```

See [`SEARCH_ALGORITHM.md`](SEARCH_ALGORITHM.md) for how results are ranked
and how to tune `--min-score`.

## Workspaces (cross-repo)

Read / search (`ws` is an alias for `workspace`):

```bash
cix workspace list                                          # all workspaces
cix workspace "<name>"                                      # describe (default verb)
cix workspace "<name>" describe                             # same, explicit
cix workspace "<name>" repos                                # list repos in the workspace
cix workspace "<name>" search "<query>" [--limit <n>]       # hybrid BM25 + dense
```

Manage (owner/admin):

```bash
cix ws create "<name>" [--description "<text>"]             # create a workspace
cix ws "<name>" add <project…>                              # link indexed project(s)
cix ws "<name>" remove <project…>                           # unlink project(s)
cix ws "<name>" rename "<new-name>"                         # rename
cix ws "<name>" update [--name "<new>"] [--description "<text>"]  # patch fields
cix ws "<name>" delete [-y]                                 # delete (prompts; -y skips)
```

A `<project>` is any **already-indexed** project, addressed by its absolute
path, its host_path (e.g. `github.com/owner/repo@main`), or its 16-hex
`path_hash` — run `cix list` to see them. `add`/`remove` accept several at
once, and with no project default to the current directory. `add` links an
existing index; it does **not** clone. To clone and index a *new* GitHub
repo into a workspace, use the dashboard's **Add repo** flow (see
[`WORKSPACES.md`](WORKSPACES.md)). `delete` removes the workspace and its
membership links only — the underlying projects are untouched.

The CLI uses a name-first grammar so an agent doesn't need to juggle
workspace ids. See [`../workspaces.md`](../workspaces.md) for the agent contract.

## File watcher

```bash
cix watch [path]             # start background daemon
cix watch --foreground       # run in terminal (Ctrl+C to stop)
cix watch stop               # stop daemon
cix watch status             # check if running
```

The watcher monitors the project with `fsnotify`, debounces events (5 s
default), and triggers incremental reindexing automatically. Logs:
`~/.cix/logs/watcher.log`.

## Configuration

```bash
cix config init              # first-run wizard (TUI form)
cix config edit              # interactive edit (TUI form)
cix config show              # print current config (lists servers; * marks default)
cix config keys              # list every settable key with default/env/description
cix config set <key> <val>   # set one value
cix config unset <key>       # remove a server / clear a key
cix config path              # show config file location
```

Config file: `~/.cix/config.yaml`.

### Env overrides (CI)

| Variable        | Overrides                                |
|-----------------|------------------------------------------|
| `CIX_SERVER`    | which alias resolves when `--server` is empty |
| `CIX_API_URL`   | the resolved server's URL                |
| `CIX_API_KEY`   | the resolved server's API key            |

Precedence is **flag > env > file > default**. Env overrides apply only
to the current process — they never write back to `~/.cix/config.yaml`.

### Multiple servers

`cix` can be configured with several named servers and pick one per
command with the global `--server <alias>` flag (without it, the
`default_server` is used):

```bash
cix config set server.corporate.url https://cix.corp.internal
cix config set server.corporate.key cix_...
cix config set default_server corporate     # optional
cix --server corporate search "rate limiter"
cix config unset server.corporate           # remove it
```

The legacy `api.url` / `api.key` keys and the `--api-url` / `--api-key`
flags still work — they read/override the default server — and old flat
`api:` config files are migrated to the `servers:` layout automatically
on first load.

---

## Per-project configuration

### `.cixignore` — exclude files from indexing

Works exactly like `.gitignore` (same syntax, same nesting rules). Patterns
are merged with `.gitignore` — you don't need to duplicate rules. Use this
for files you want excluded from the *index* that aren't already excluded
from git (vendored code, generated files, large test fixtures):

```gitignore
# .cixignore
api/generated/
vendor/
*.pb.go
testdata/fixtures/
```

Nested `.cixignore` files work like nested `.gitignore`. The file watcher
automatically triggers a full reindex when `.cixignore` is created,
modified, or deleted.

Two pattern shapes are matched differently from `git check-ignore`, in both the
CLI and the server:

- The allowlist idiom (`*`, then `!*/`, then `!*.go`) excludes only top-level
  files — `!*/` re-includes everything nested, not just directories. Write the
  exclusions positively instead.
- `[!abc]` is read as the literal set `{!, a, b, c}`, not as a negation
  (Go's `filepath.Match` spells that `[^abc]`), so it excludes `a.txt` where
  git would keep it. Avoid character classes here.

#### GitHub-backed projects

Both files are honoured for repos the **server** clones and indexes, not just
for local projects the CLI walks — commit a `.cixignore` and it takes effect
on the next sync. When a push touches any `.gitignore` or `.cixignore`, that
sync upgrades itself from an incremental pass to a full reconcile, so files a
new rule now covers are *removed* from the index rather than merely left
alone. Removing a rule works the same way in reverse.

Two differences from the local case are worth knowing:

- **`.gitignore` barely does anything on a cloned repo, by design.** A clone
  checks out exactly the tracked files, and git never applies ignore rules to
  files it already tracks. So the only files a `.gitignore` excludes
  server-side are ones the repo tracks *despite* its own rule — a committed
  `bin/deploy.sh` under a `bin/` rule, say. Use `.cixignore` for anything you
  actually want excluded.
- **`.cixignore` beats `.gitignore` on ties.** `!keep.log` in `.cixignore`
  re-includes a file that `*.log` in `.gitignore` excluded. The CLI resolves
  the two files independently and cannot do this.

Repos indexed before server-side support shipped are cleaned up gradually —
the first push that touches an ignore file reconciles them. To force it now,
use **Reindex** in the dashboard (`POST /api/v1/projects/{hash}/reindex?full=true`).

Ignore rules govern **indexing**, not file serving: `cix file` and `cix tree`
read the server's checkout directly and will still return an ignored path.
Don't treat `.cixignore` as an access control.

### `.cixconfig.yaml` — project-level settings

Place in the project root:

```yaml
ignore:
  submodules: true   # automatically exclude all git submodule paths
```

When `ignore.submodules` is `true`, cix reads `.gitmodules` and excludes all
submodule paths from indexing. No git binary required — the file is parsed
directly. Useful for Foundry/Forge dependencies, vendored submodules, or any
repo where submodules contain thousands of files you don't want indexed. The
watcher triggers a full reindex when this file changes.

This one is CLI-only: the server clones without `--recurse-submodules`, so a
cloned repo's submodule directories are empty and there is nothing to exclude.
