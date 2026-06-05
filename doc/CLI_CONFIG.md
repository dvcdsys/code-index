# `cix` CLI configuration — reference

Comprehensive reference for everything the `cix` CLI lets you configure.
For a quick tour see [`cli/README.md`](../cli/README.md#run-against-a-server).

## File location

`~/.cix/config.yaml` — created on first `cix config set …` /
`cix config init`. The CLI seeds an implicit `default` server pointing at
`http://localhost:21847` if no file exists yet, but does not materialise
that to disk until the user writes something.

## Precedence

```
CLI flag (--server / --api-url / --api-key)
        ↓
Environment variable (CIX_SERVER / CIX_API_URL / CIX_API_KEY)
        ↓
~/.cix/config.yaml
        ↓
Built-in default (struct-tag default:"…")
```

Env overrides never write back to disk. The 3 env vars are the entire
env surface — knobs like `watcher.debounce_ms` are persistent
preferences and have no env binding.

## Commands

| Command                                | Purpose |
|----------------------------------------|---------|
| `cix config show`                      | Human-readable dump of the current configuration |
| `cix config keys`                      | List every settable key with default, env, description |
| `cix config set <key> <value>`         | Set one key — supports scalars + comma-separated lists |
| `cix config unset server.<name>[.key\|.header.<Name>]` | Remove a server entry, clear its key, or drop one custom header |
| `cix config edit`                      | Interactive TUI form (huh) for the whole file |
| `cix config init`                      | First-run wizard — same form, pre-seeded for fresh installs |
| `cix config path`                      | Print the file path (useful in scripts) |

## Keys

### Server selection

| Key                       | Type   | Default                      | Description |
|---------------------------|--------|------------------------------|-------------|
| `servers`                 | list   | `[{default → localhost}]`    | Managed via `cix config set server.<name>.url|key` |
| `default_server`          | string | `default`                    | Active alias when `--server`/`CIX_SERVER` are unset |
| `server.<name>.url`       | string | —                            | URL of a named server (creates the entry on first set) |
| `server.<name>.key`       | string | —                            | API key for the named server (sensitive — never printed) |
| `server.<name>.header.<Name>` | string | —                       | Custom HTTP header sent on every request (sensitive — never printed; values support `${ENV}` expansion). See [Custom request headers](#custom-request-headers-auth-behind-a-reverse-proxy) |
| `api.url` / `api.key`     | string | —                            | Legacy aliases — operate on the default server |

### File watcher

| Key                            | Type     | Default                                                                | Validation |
|--------------------------------|----------|------------------------------------------------------------------------|------------|
| `watcher.enabled`              | bool     | `true`                                                                 | — |
| `watcher.debounce_ms`          | int      | `5000`                                                                 | 100 — 60000 |
| `watcher.sync_interval_mins`   | int      | `5`                                                                    | ≥ 1 |
| `watcher.exclude`              | []string | `node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store` | comma-separated; REPLACE semantics on set |

### Indexing

| Key                                    | Type | Default | Validation | Description |
|----------------------------------------|------|---------|------------|-------------|
| `indexing.batch_size`                  | int  | `20`    | ≥ 1        | Per-batch file count for the upload pipeline |
| `indexing.streaming_idle_timeout_sec`  | int  | `30`    | ≥ 0        | Max silence on streaming `/index/files` before giving up; 0 disables |

### Projects

| Key                        | Type        | Description |
|----------------------------|-------------|-------------|
| `projects`                 | list        | Managed via `cix init` / dashboard — not editable via `config set` |

## Custom request headers (auth behind a reverse proxy)

When cix is self-hosted behind an authenticating reverse proxy / Zero-Trust
gateway (Cloudflare Access, oauth2-proxy, Authelia, an SSO/mTLS-terminating
load balancer, a corporate proxy), the proxy decides whether a request even
reaches cix. The browser dashboard passes it via interactive SSO, but the CLI
and AI-agent tooling send only the cix Bearer and would be bounced at the edge
(302 to login / 403) off-VPN.

Per-server **custom headers** let the CLI attach whatever the proxy needs on
**every** request (including the `/health` probe) — e.g. a Cloudflare Access
service token — *in addition* to the cix Bearer. The proxy validates and
strips those headers at the edge, so the cix origin still only sees the Bearer
— **cix needs no knowledge of the proxy**. Opt-in: with no headers configured,
behavior is unchanged.

```yaml
servers:
  - name: default
    url: https://cix.example.com
    key: cix_xxx
    headers:                                    # attached to every request
      CF-Access-Client-Id: "<client-id>.access"
      CF-Access-Client-Secret: "${CIX_CF_ACCESS_SECRET}"   # ${ENV} expansion
```

Set / remove them from the command line:

```bash
cix config set server.default.header.CF-Access-Client-Id "<client-id>.access"
cix config set server.default.header.CF-Access-Client-Secret '${CIX_CF_ACCESS_SECRET}'
cix config unset server.default.header.CF-Access-Client-Id
```

Notes:

- **`${ENV}` expansion** happens at request time into a throwaway copy — the
  config file keeps the literal `${CIX_CF_ACCESS_SECRET}`, so no plaintext
  secret is persisted. Use `${VAR}` / `$VAR`; quote the value in your shell so
  it isn't expanded before cix sees it.
- **Unset variable = hard error.** If a header references a variable that is
  not set (a typo or a forgotten `export`), the command fails with a message
  naming the variable instead of silently sending an empty header that bounces
  at the proxy. A variable that is *set but empty* (`export X=`) is honored as
  an intentional empty value.
- **Literal `$`.** Expansion treats `$NAME` / `${NAME}` as references; to put a
  literal `$` in a value, write `$$` (e.g. a token `pa$$word` is sent as
  `pa$word`).
- **Header-name case.** Names are canonicalized on the wire (Go's
  `http.Header`), so `CF-Access-Client-Id` is sent as `Cf-Access-Client-Id`.
  HTTP header names are case-insensitive (RFC 7230) and Cloudflare Access et al.
  honor that, so this is normally invisible.
- **Never printed.** Header values are sensitive: `cix config show` and
  `cix config edit` (TUI) surface only the **count** (`headers=N` / `headers N`),
  never names or values. Edit them via `cix config set` or by hand in
  `~/.cix/config.yaml` — the TUI does not edit individual headers.
- **Safety.** cix-managed headers (`Authorization`, `Content-Type`, `Accept`)
  always win, so a custom header can never clobber authentication. Header names
  must be valid HTTP tokens and values may not contain CR/LF.
- For human users a short-lived per-user proxy login is preferable; service
  tokens suit machine/agent clients. Both just need the client to send headers.

## Env vars

| Variable        | Overrides                       | Notes |
|-----------------|---------------------------------|-------|
| `CIX_SERVER`    | `default_server`                | Used only when `--server` is empty |
| `CIX_API_URL`   | resolved server's `url`         | Local override; never persisted |
| `CIX_API_KEY`   | resolved server's `key`         | Designed for `secrets.CIX_API_KEY` in CI |

## Implementation notes

- **Single source of truth**: every key, default, validation rule, and
  description lives on the corresponding Go struct field as a tag
  (`yaml`, `key`, `default`, `validate`, `env`, `desc`, `sensitive`).
  All five surfaces — load, save, show, set, TUI — read from this
  schema via reflection.
- **Loader**: [`knadh/koanf v2`](https://github.com/knadh/koanf) layers
  defaults (from tags) and the YAML file. Legacy lowercase keys
  (`debouncems`, `excludepatterns`, `cachettl`, `autowatch`, `batchsize`)
  are normalised in-place pre-parse so old files keep loading. The
  `api:` block is migrated into the multi-server `servers:` list and
  cleared on the next save.
- **Validation**: [`go-playground/validator/v10`](https://github.com/go-playground/validator)
  validates the whole `Config` after every mutation via `cix config set`
  and on TUI form submit. `Load()` itself does NOT validate — a
  malformed value in an on-disk file must not brick the CLI; bad values
  surface the next time the user tries to change something.
- **TUI**: [`charmbracelet/huh`](https://github.com/charmbracelet/huh)
  builds the paged form. The Charm stack (`huh` + `bubbletea` +
  `lipgloss`) is the visual layer for any future TUI screens too.
- **Sensitive fields**: `ServerEntry.Key` and `ServerEntry.Headers` carry
  `sensitive:"true"`. Renderers print `(set)` / `(not set)` for the key and a
  header **count** only, never a value. CodeQL's `go/clear-text-logging`
  heuristic flags reads of `*Key`/`*Secret` into named variables, so the tag is
  read off `reflect.StructField` and the value goes through
  `reflect.Value.IsZero()` only.
- **Custom headers**: `ServerEntry.Headers` is attached to every request by
  `client.applyCustomHeaders` (called before the cix-managed headers so those
  win). `${ENV}` expansion + validation happen in `getClient`
  (`cli/cmd/root.go`) into a local copy that is never written back — mirroring
  the `--api-url`/`--api-key` override pattern.
