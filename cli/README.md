# `cix` — Code IndeX CLI

A thin Go client for the `cix-server` semantic code index. Runs `init`,
`search`, `symbols`, `def`, `refs`, `files`, `summary`, `watch`,
`reindex`, `cancel`, `list`, `config`, `workspace`, and `version`
commands against the HTTP API.

The full user-facing command catalogue lives in the top-level
[`README.md`](../README.md#cli-reference). This file covers building
the CLI from source, the internal layout, and how to add a new command.

## Layout

```
cli/
├── cmd/                 — cobra command implementations
│   ├── root.go          — root command + global flags
│   ├── init.go          — `cix init`
│   ├── search.go        — `cix search`
│   ├── symbols.go       — `cix symbols`
│   ├── definitions.go   — `cix def` (+ goto alias)
│   ├── references.go    — `cix refs`
│   ├── files.go         — `cix files`
│   ├── summary.go       — `cix summary`
│   ├── status.go        — `cix status`
│   ├── list.go          — `cix list`
│   ├── reindex.go       — `cix reindex`
│   ├── cancel.go        — `cix cancel`
│   ├── watch.go         — `cix watch` (start/stop/status, daemon)
│   ├── config.go        — `cix config show/set/unset/path` (+ multi-server keys)
│   ├── config_keys.go   — `cix config keys` (schema-driven key listing)
│   ├── config_edit.go   — `cix config edit` / `cix config init` (huh-driven TUI)
│   ├── workspace.go     — `cix workspace …` (cross-repo, name-first)
│   └── version.go       — `cix version`
├── internal/
│   ├── client/          — HTTP client to cix-server
│   ├── config/          — YAML config (~/.cix/config.yaml)
│   │   ├── schema/      — tag-driven walker over Config (single source of truth)
│   │   └── tui/         — huh-based form for `cix config edit` / `init`
│   ├── daemon/          — PID-file based watcher daemon
│   ├── discovery/       — project-root detection for `cix init`
│   ├── fileutil/        — binary/text + size helpers
│   ├── indexer/         — file-walk + NDJSON upload pipeline
│   ├── projectconfig/   — .cixignore / .cixconfig.yaml parsing
│   └── watcher/         — fsnotify-based incremental reindex watcher
├── main.go
├── Makefile
└── README.md            — this file
```

Module path: `github.com/dvcdsys/code-index/cli`.

## Build

Prerequisites: Go 1.25+.

```bash
cd cli
make build              # → cli/build/cix
make install            # → /usr/local/bin/cix (uses sudo if needed)
```

Or without make:

```bash
cd cli
go build -o cix .
sudo mv cix /usr/local/bin/
```

For cross-builds and release tarballs, see [`doc/RELEASES.md`](../doc/RELEASES.md#cutting-a-cli-release).

## Run against a server

The CLI talks HTTP — there is no embedded server logic in this
directory. Configure once:

```bash
cix config set api.url http://localhost:21847
cix config set api.key <bearer-token>
cix config show
```

Then any command picks up the saved URL + key from `~/.cix/config.yaml`.

The server can be local Docker (`docker compose up -d` in the repo
root) or a remote server. The CLI doesn't care.

### Multiple servers

The CLI can hold several **named servers** and pick one per command. The
config stores a `servers:` list and a `default_server`; commands use the
default unless `--server <alias>` is given.

```bash
# Add a second server and switch the default
cix config set server.corporate.url https://cix.corp.internal
cix config set server.corporate.key <bearer-token>
cix config set default_server corporate

# Target a specific server for one command (alias must exist in config)
cix --server corporate search "rate limiter"

# Inspect / remove
cix config show                      # lists servers; * marks the default
cix config unset server.corporate    # remove a server
```

The legacy `api.url` / `api.key` keys and the `--api-url` / `--api-key`
flags still work — they operate on (or override) the **default** server,
so single-server setups need no changes. Old `~/.cix/config.yaml` files
that use the flat `api:` block are migrated to the `servers:` layout
automatically on first load (the old single server becomes `default`).

### Environment overrides (CI-friendly)

For CI runners, containers, and one-off scripts you can override server
selection via env vars instead of writing to `~/.cix/config.yaml`.
Precedence is always **flag > env > file > built-in default** — env
overrides never persist to disk.

| Variable        | Overrides                                | Use case |
|-----------------|------------------------------------------|----------|
| `CIX_SERVER`    | which alias resolves when `--server` is empty | Switch active server in a shell session without touching the file |
| `CIX_API_URL`   | the resolved server's `url`              | Point at a different cix-server instance per process |
| `CIX_API_KEY`   | the resolved server's `key`              | Pass a secret from `secrets.CIX_API_KEY` in GitHub Actions |

Example (GitHub Actions):

```yaml
env:
  CIX_API_URL: https://cix.corp.internal
  CIX_API_KEY: ${{ secrets.CIX_API_KEY }}
steps:
  - run: cix search "foo"
```

The 3-var surface is deliberately narrow — knobs like
`watcher.debounce_ms` or `indexing.batch_size` live in the config file
only, because they are persistent developer preferences, not per-process
overrides.

### Interactive setup (`cix config init` / `cix config edit`)

`cix config init` is the first-run wizard for fresh machines: it opens
a paged form (`huh`-driven TUI) that seeds the default server entry,
asks for the API key, and walks through the watcher + indexing knobs.
On submit it validates everything against the schema and writes
`~/.cix/config.yaml`.

`cix config edit` is the same form against an existing config — useful
when you want to flip booleans (e.g. `watcher.enabled`) or tune timeouts
without re-reading `cix config set --help`.

```
┌─ Servers ──────────────────────────────┐
│ [default] URL  http://localhost:21847  │
│ [default] API key  ●●●●●●●●            │
│ Default server  ▼ default              │
└────────────────────────────────────────┘
┌─ File watcher ─────────────────────────┐
│ Enable the watcher    [✓]              │
│ Debounce (ms)         5000             │
│ Sync interval (min)   5                │
│ Exclude patterns      node_modules,…   │
└────────────────────────────────────────┘
┌─ Indexing ─────────────────────────────┐
│ Batch size            20               │
│ Streaming idle (s)    30               │
└────────────────────────────────────────┘
       [ Submit ]   ESC to cancel
```

Add/remove of server aliases is still done via
`cix config set server.<name>.url …` / `cix config unset server.<name>`
— the form edits URL/key of *existing* aliases.

### Discovering keys (`cix config keys`)

`cix config keys` prints every settable configuration key with its
current value, default, env-var binding (if any), and a short
description. This is the canonical reference — there is no hard-coded
list anywhere else:

```bash
$ cix config keys
KEY                                  VALUE                  DEFAULT  ENV         DESCRIPTION
default_server                       default                —        CIX_SERVER  Alias of the server used when --server is omitted
watcher.enabled                      true                   true     —           Run the file watcher
watcher.debounce_ms                  5000                   5000     —           Debounce delay (ms)
watcher.exclude                      [node_modules .git …]  …        —           Paths/globs to skip (REPLACE semantics on set)
watcher.sync_interval_mins           5                      5        —           Periodic sync interval (minutes)
indexing.batch_size                  20                     20       —           Indexing batch size
indexing.streaming_idle_timeout_sec  30                     30       —           Streaming /index/files idle timeout (seconds); 0 disables
```

Slice keys (servers, projects) are not listed here — `cix config show`
displays them in their dedicated formats.

### List-valued keys (`watcher.exclude`)

`watcher.exclude` is the one list-valued scalar that `cix config set`
accepts. Input is **comma-separated**, and the semantics are
**REPLACE, not append**:

```bash
$ cix config set watcher.exclude "node_modules,vendor,build"
# overwrites the entire list; previous defaults are gone
```

There is no `cix config add` / `cix config append` — if you want to
keep the existing defaults plus add an entry, repeat the full list.
The interactive `cix config edit` form is usually nicer for this.

## Smoke test

```bash
# Server reachable?
cix status
# (without a project context, status prints the configured URL + key state)

# Index a fresh project + search it
cd /path/to/some/repo
cix init --watch=false
cix status                          # wait for: Status: ✓ Indexed
cix search "main entry point"
cix symbols "Handler" --kind function
cix files "config"
cix summary
```

Watcher smoke (in a separate terminal):

```bash
cix watch /path/to/some/repo        # starts the background daemon
cix watch status
# edit a file in the project — watcher should log a reindex
cix watch stop
```

## Adding a new command

Each command is a `cobra.Command` constructed in a `New<Name>Command()`
factory and registered from `root.go`. Conventions:

1. Place the command in `cmd/<name>.go`.
2. The factory takes no global state — it reads config and builds an
   `*client.Client` inside the command's `RunE`. This keeps unit tests
   table-driven and free of init-order surprises.
3. Network calls go through `internal/client`. Add a method there if
   the existing surface doesn't cover your endpoint; don't reach for
   `net/http` from inside `cmd/`.
4. Errors propagate through `RunE`'s return — cobra prints the message
   and sets a non-zero exit code. Don't `os.Exit` from a command.
5. Output goes to `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`, not the
   process-wide `os.Stdout` — this is what makes tests work.

Tests sit beside the file (`<name>_test.go`); they assemble the
command, set `SetArgs`, and capture output via `bytes.Buffer`. See
`cmd/root_test.go` for the established pattern.

## Tests

```bash
cd cli
go test ./...
# or, for verbose / single-package:
go test -v ./cmd/...
go test -run TestSearch ./cmd/...
```

CI runs the suite on every PR (`.github/workflows/ci-cli.yml`).

## Releasing

See [`doc/RELEASES.md`](../doc/RELEASES.md#cutting-a-cli-release).
The short version: bump `cli/cmd/version.go`, push a `cli/v<version>`
tag, CI builds the four-platform tarball set and uploads to GitHub
Releases. The install scripts pick it up on next run.

For pre-release builds tracking `develop`, see
[`doc/UPDATES.md`](../doc/UPDATES.md#cli-install-channels).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `API key not set` | `cix config set api.key <bearer>` — mint one from the dashboard's API Keys page if you don't have one. |
| `connection refused` | The server isn't running, or `api.url` is wrong. `curl $(cix config show \| grep url)/health` should return `{"status":"ok"}`. |
| `project not found` | Run `cix init` in the project root first. |
| Watcher not reindexing | `cix watch status`; check `~/.cix/logs/watcher.log`; restart with `cix watch stop && cix watch <path>`. |
| Search returns nothing | Lower the floor: `cix search "query" --min-score 0.25` (default is 0.4). See [`doc/SEARCH_ALGORITHM.md`](../doc/SEARCH_ALGORITHM.md). |

## License

MIT
