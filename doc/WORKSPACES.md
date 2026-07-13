# Workspaces — operator guide

The workspaces feature lets cix index a group of GitHub repositories
together and serve cross-project semantic search against the union.
This document covers everything an operator needs to enable, configure,
and troubleshoot the feature in production.

For the user-facing workflow (when to reach for workspace search, the
agent trust rules, query patterns), see [`../workspaces.md`](../workspaces.md).
For the search algorithm, see [`SEARCH_ALGORITHM.md`](SEARCH_ALGORITHM.md).
For the webhook lifecycle, see [`WEBHOOKS.md`](WEBHOOKS.md). For the
polling alternative (non-admin repos), see [`POLLING.md`](POLLING.md).

> **No feature flag.** Workspaces + GitHub-repo support are part of
> every release. The previous `CIX_WORKSPACES_ENABLED` gate was removed
> in the 0.4.x line — the only failure mode that still surfaces as a
> 503 from `/api/v1/workspaces/*` is when the encryption layer needed
> for `github_tokens` fails to wire (see "Encryption key resolution"
> below).

## Schema

A workspace is a *membership* layer over per-project indexes. Three
tables underpin it (post-`e433fee` refactor; the older
`workspace_repos` table no longer exists):

| Table | Role |
|---|---|
| `workspaces` | The workspace itself (id, name, description). |
| `git_repos` | Clone metadata, 1:1 with `projects` — github_url, branch, token_id, webhook_secret, webhook_id, webhook_mode, auto_webhook, last_sha. Only populated for repos cix cloned (workspace adds); local `cix init` projects don't have a row here. |
| `workspace_projects` | Many-to-many junction. A project (cloned or local) can belong to multiple workspaces; deleting a workspace doesn't drop the underlying project. |

Migrations live in `server/internal/db/migrations.go`. The split-out
migration is `19226aa` (crash-safe + `schema_migrations` versioning)
and the table rename in `e433fee`.

## Quick start

1. **Set the encryption key** so `github_tokens` rows can be sealed:
   ```
   CIX_SECRET_KEY=<hex- or base64-encoded 32-byte key>  # see "Encryption"
   ```
   Or skip and let the server auto-generate a keyfile under
   `<CIX_SECRETS_DATA_DIR>/.secret_key` on first boot — fine for a
   single-host dev setup, **back it up** before redeploying.
2. **Open the dashboard** at `https://<host>/dashboard` and sign in.
3. **Add a GitHub PAT** under **GitHub Tokens → Add token** if you need
   to clone private repos. The plaintext value is encrypted before it
   hits SQLite and is never returned in any subsequent response.
4. **Create a workspace** under **Workspaces → New workspace**.
5. **Attach a repository:** workspace detail → Add repo. Fill in URL,
   branch, optional token, and choose **Auto-register webhook** if
   your PAT carries `admin:repo_hook`. Otherwise check **I'll set it
   up myself** and copy the displayed URL + secret into GitHub.
6. The server clones the repo into `<CIX_REPOS_DIR>/<path_hash>/`
   and runs the existing indexer pipeline against it. Status transitions
   visible on the workspace detail page: `created → indexing → indexed`.

> **Linking an already-indexed project (no clone).** Steps 4–5 clone a
> *new* GitHub repo. To instead group projects that are **already
> indexed** — local `cix init` projects, or repos cloned earlier — link
> them without a second clone via the dashboard's **Link existing
> project** button or the CLI: `cix ws "<name>" add <project>` (plus
> `cix ws create` / `remove` / `rename` / `delete` for the rest). Full
> verb list: [`CLI_REFERENCE.md`](CLI_REFERENCE.md#workspaces-cross-repo).

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `CIX_SECRET_KEY` | (auto-generate) | 32-byte AES key encoding GitHub tokens. Hex or base64. |
| `CIX_SECRET_KEYFILE` | unset | Alternative — path to a 0600-perm key file. |
| `CIX_SECRETS_DATA_DIR` | `dirname(CIX_SQLITE_PATH)` | Where the auto-generated keyfile lives. |
| `CIX_REPOS_DIR` | `<sqlite parent>/repos` | Where cloned repos live. |
| `CIX_WORKSPACES_DATA_DIR` | unset | Legacy alias for `CIX_REPOS_DIR`, honoured when the new name is unset. Prefer `CIX_REPOS_DIR`. |
| `CIX_WORKER_CONCURRENCY` | `2` | Parallel job workers. Clone+index is mostly IO-bound. |
| `CIX_PUBLIC_URL` | unset | Externally-reachable URL used to build webhook delivery URLs. |

### Encryption key resolution

Resolution order:

1. `CIX_SECRET_KEY` (hex or base64 32-byte value)
2. `CIX_SECRET_KEYFILE` (path; file must be `0600`)
3. `<CIX_SECRETS_DATA_DIR>/.secret_key` — auto-generated on first
   boot if neither of the above resolve. The server **refuses to
   start** if `github_tokens` is non-empty and the resolved key
   cannot decrypt the first row — protects against accidental key
   rotation that would silently brick all tokens.

For production, supply `CIX_SECRET_KEY` explicitly or mount a keyfile
via `CIX_SECRET_KEYFILE`. The auto-generated keyfile is a single-host
convenience for dev.

## Access model

Workspaces and their attached repos live in cix's ownership +
view-group access model (introduced in `e275c4a`, server v0.6.0). Two
levels matter for workspace operators:

- **Workspaces have an owner** (`owner_user_id`). The owner and any
  admin can edit, share, attach repos, or delete. Other users see the
  workspace only if it is **shared to a view-group** they belong to,
  and then only read-only (search, list members).
- **External projects** (those with a `git_repos` peer — i.e. every
  workspace-attached repo) are **ownerless** and **admin-administered**.
  Add-repo, delete-repo, rotating PATs, and reregistering webhooks are
  admin-only. Non-admins can read a shared external project via a
  view-group, but cannot administer it.

To grant a teammate access to a workspace's search results:

1. Admin creates a view-group under **Dashboard → Groups**.
2. Adds the user as a member.
3. Shares the workspace (and/or the external projects inside it) to
   that group.

Local projects registered with `cix init` keep the per-user ownership
model and are private to their creator — they are not eligible for
view-group sharing. See `docs/AUTH_REVIEW.md` (local-only) for the
full access matrix.

## Webhooks

GitHub deliveries hit `POST /api/v1/webhooks/github/<path_hash>`.
The endpoint is **public** in the auth sense (no Bearer/session check)
but every delivery is HMAC-SHA256-validated against the per-row
`webhook_secret` stored on the matching `git_repos` row. The secret is
shown exactly once on add-repo and on **Project → Webhook info**.

Supported events:

| Event | Behaviour |
|---|---|
| `push` (tracked branch) | Enqueues `clone_repo` job — dedupe collapses bursts. |
| `push` (other branch / delete) | 200 `{"status":"ignored"}`. |
| `ping` | 200 `{"status":"ping"}`. Use to confirm setup. |
| anything else | 200 `{"status":"ignored"}`, logged for audit. |

### Cloudflare tunnel (recommended for self-hosted)

Webhooks require a public URL. The simplest no-cost option is a
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/).
On the cix-server host:

```bash
# One-time: install + log in
brew install cloudflared
cloudflared tunnel login

# Create a named tunnel
cloudflared tunnel create cix

# Route a hostname to the tunnel (replace cix.example.com with yours)
cloudflared tunnel route dns cix cix.example.com

# Run the tunnel — replace 21847 with your CIX_PORT
cloudflared tunnel --url http://localhost:21847 run cix
```

Then set `CIX_PUBLIC_URL=https://cix.example.com` and restart the server.
The dashboard's add-repo dialog and the webhook-info endpoint will
generate fully-qualified URLs that GitHub can reach.

For ad-hoc testing without DNS:

```bash
cloudflared tunnel --url http://localhost:21847
# prints a one-shot https://*.trycloudflare.com URL
```

Set `CIX_PUBLIC_URL` to whatever cloudflared prints and restart.
Single-process tunnels are torn down with the parent — not suitable for
production but perfect for the first end-to-end smoke test.

### Manual webhook setup

If `webhook_mode=manual` (default) the dashboard surfaces the URL + secret
after add-repo. Paste them into GitHub:

1. Repo → **Settings → Webhooks → Add webhook**
2. **Payload URL** = the value from the dashboard
3. **Content type** = `application/json`
4. **Secret** = the value from the dashboard
5. **Which events?** → **Just the push event**
6. **Active** ✓

GitHub will send a `ping` immediately — the cix server returns 200, and
GitHub's webhook page will mark the delivery green.

### Auto-register

When the PAT carries `admin:repo_hook` scope and `webhook_mode=auto`,
the server uses GitHub's hooks API on your behalf during add-repo and
persists the resulting hook id (used to de-register on delete). Failure
is non-fatal — the response includes `auto_registered: false` and an
operator-facing note explaining the specific reason (missing scope,
network error, etc.).

## Background workers

A single in-process worker pool drains a SQLite-backed queue (`jobs`
table). Concurrency is `CIX_WORKER_CONCURRENCY` (default 2). Job types
in PR2–PR3:

- `clone_repo` — clones (or fetches+resets on reuse) via go-git;
  registers `projects` row; chains `index_repo`.
- `index_repo` — runs the existing 3-phase indexer in-process against
  the clone directory; flips repo status to `indexed`.

Future PRs add `build_call_graph` and `compute_workspace_communities`.

### Inspecting the queue

`GET /api/v1/jobs` lists recent jobs with optional `status=` / `type=` /
`limit=` filters. Useful for diagnosing stuck repos.

## Troubleshooting

- **Status stuck at `indexing`** — check `GET /jobs?status=running` and
  the cix-server logs. Most common cause: PAT missing `repo` scope on
  a private repo, or network not reaching github.com.
- **Status stuck at `error`** — the underlying job's error message is
  surfaced on the project detail page. Common fixes: rotate the PAT,
  confirm the branch name, verify the runtime model is loaded
  (`GET /api/v1/admin/sidecar/status`).
- **Webhook deliveries returning 401** — the secret in GitHub doesn't
  match what cix stored. Click **Webhook info** in the dashboard to
  see the canonical value, paste again. Secrets rotate when the
  git_repos row is recreated.
- **Encryption key mismatch on startup** — operator-readable error in
  the boot log. Recover the prior `CIX_SECRET_KEY` from your secrets
  manager or wipe `github_tokens` manually before retrying.

## Shipped follow-ons (PR4 – PR8)

The original `PR4–PR7` placeholders have all landed on `develop`:

- **PR4** (`f244643`) — Intra-project call-graph extraction
  (`call_edges` table) + eval harness.
- **PR5** (`ec32744`) — Louvain community detection per workspace +
  workspace centroid embeddings in a dedicated chromem collection.
- **PR6** (`207bfaf`) — Two-stage workspace search endpoint
  (`POST /api/v1/workspaces/{id}/search`). Hybrid BM25 + dense ranking
  with project-level gating — see [`SEARCH_ALGORITHM.md`](SEARCH_ALGORITHM.md#3-workspace-hybrid-search).
- **PR7** (`e1aa785`) — CLI subcommand (`cix workspace …`,
  name-first grammar from PR8 / `5db28fd`) + `cix-workspace`
  Claude Code skill + dashboard search dialog.
- **PR8** (`5db28fd`) — Workspace discovery: dashboard expansion
  panels per project + name-first CLI grammar so an agent can do
  `cix ws "<workspace name>" search "<query>"` without juggling
  workspace ids.

Subsequent fixes calibrated the hybrid defaults (`96b487d`), added
the FTS5 chunk mirror across all projects (`f00e3d3`), and tightened
webhook validation + PAT handling (`903d48f`, `57e091d`).
