# Git Polling Sync

Polling is the alternative to [webhooks](WEBHOOKS.md) for keeping a
server-cloned git project in sync. Use it for repos where you **cannot
install a webhook** — typically because you are not an admin of the
repository.

Where a webhook is push-driven (GitHub calls the server when someone
pushes), polling is pull-driven: the server periodically fetches the
remote and re-indexes only when the branch HEAD has moved.

Polling, like webhooks, is a **server-cloned repo** feature. A local
project registered with `cix init` uses the file watcher (`cix watch`),
not polling.

## 1. Webhook XOR polling

A `git_repos` row syncs via **either** webhook **or** polling, never
both. Polling can only be enabled when `webhook_mode = 'disabled'`.
Attempting to enable polling on a repo whose `webhook_mode` is `manual`
or `auto` is rejected with HTTP 422.

When you add a repo with `webhook_mode: auto` but the PAT lacks
`admin:repo_hook` (or you are not a repo admin), auto-registration
fails. In that case the server **automatically falls back to polling**:
it flips `webhook_mode` to `disabled`, enables polling at the default
interval, and notes this in the add-repo response (`auto_register_note`).

## 2. PAT and rate limits

Polling reuses the existing clone/fetch pipeline, so it authenticates
the `git fetch` with the repo's stored PAT (`token_id`) exactly as the
webhook-triggered path does — sent as HTTP basic-auth
`x-access-token`. Authenticated fetches keep polling within GitHub's
rate limits. Polling makes **no GitHub REST API calls**; it is a git
fetch over HTTPS, and an unchanged remote costs only a ref-negotiation
round-trip (no pack download, no re-index).

Set a `token_id` on any private repo you poll. Public repos can be
polled without a PAT but are subject to lower unauthenticated limits.

## 3. Cadence — measured from the end of the last index run

Each polling repo has a `next_poll_at` timestamp. The next poll is
scheduled `interval` seconds **after the previous cycle finishes**
(no-change fetch, successful index, or terminal failure) — not on a
fixed wall-clock cadence. This prevents a slow index run from stacking
up overlapping polls.

The effective interval is:

```
poll_interval_seconds (per repo, if set)
  └─ else CIX_DEFAULT_POLL_INTERVAL
       └─ clamped up to CIX_MIN_POLL_INTERVAL (floor)
```

`next_poll_at` is exposed on the `GitRepo` payload
(`GET /api/v1/projects/{hash}/git-repo`) so you can see when a repo is
next due.

## 4. One shared queue, bounded workers

A single background scheduler
(`server/internal/pollscheduler`) drives polling for **all** polling
repos. Every tick (`CIX_POLL_SCHEDULER_TICK`, default 30s) it asks the
DB which repos are due and enqueues a `clone_repo` job for each into the
shared job queue (`server/internal/jobs`).

That queue is bounded by `CIX_WORKER_CONCURRENCY` (default 2) and
deduplicates by repo, so a fleet of repos coming due at the same moment
can never spike indexing concurrency — the work simply queues and drains
at the configured worker count. The `clone_repo → index_repo` pipeline
is reused verbatim, including incremental `tree.Diff` reindex: an
unchanged remote skips indexing entirely, and a changed remote
re-indexes only the changed files. A full reindex happens only in the
same edge cases as the webhook path (first index, missing diff base,
embedding-model drift, or an explicit `?full=true` reindex).

## 5. Configuration

| Env var | Default | Meaning |
|---|---|---|
| `CIX_DEFAULT_POLL_INTERVAL` | `5m` | Interval for polling repos without a per-repo override. |
| `CIX_MIN_POLL_INTERVAL` | `60s` | Floor applied to every effective interval. |
| `CIX_POLL_SCHEDULER_TICK` | `30s` | How often the scheduler scans for due repos. |
| `CIX_WORKER_CONCURRENCY` | `2` | Shared worker count that bounds concurrent clone+index work. |

## 6. API

```
# Enable polling at create time (requires webhook_mode=disabled)
POST /api/v1/git-repos
{
  "github_url": "https://github.com/owner/repo",
  "branch": "main",
  "token_id": "<stored PAT id>",
  "webhook_mode": "disabled",
  "polling_enabled": true,
  "poll_interval_seconds": 300
}

# Toggle polling / change interval on an existing repo
PATCH /api/v1/projects/{hash}/git-repo
{ "polling_enabled": true, "poll_interval_seconds": 300 }
```

`poll_interval_seconds` is optional (omit or `0` → server default).
Enabling polling while `webhook_mode != 'disabled'` returns 422.
