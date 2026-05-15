# GitHub Webhooks for Workspaces

The webhook path is the production answer to "how does my workspace
re-index when a teammate pushes?". This doc covers the lifecycle,
modes, HMAC validation, and how to register manually when auto-register
isn't an option.

Webhooks are a **workspaces feature** — they're only meaningful for
repos the cix-server itself clones via the Workspaces page. A local
project registered with `cix init` uses the file watcher
(`cix watch`), not webhooks.

## 1. Modes

Each `git_repos` row carries a `webhook_mode` enum:

| Mode | When set | Behaviour |
|---|---|---|
| `off` | Default for repos added without a token, or when the operator opts out. | Server ignores incoming webhook deliveries (returns 200 `{"status":"ignored"}` after HMAC check) — there is no re-index trigger. |
| `manual` | Default when a repo *has* a token but the operator unchecked "auto-register". | Server stores a secret + URL and shows them once on add-repo. Operator pastes them into GitHub by hand. |
| `auto` | Set when the operator checks "Auto-register webhook" *and* the PAT carries `admin:repo_hook`. | Server calls GitHub's hooks API on the operator's behalf during add-repo, persists the hook id, and de-registers the hook on delete. |

`auto` is preferred — it makes onboarding a repo a one-form action.
`manual` exists for operators whose PATs intentionally lack
`admin:repo_hook` (audit, principle-of-least-privilege).
`off` is for repos where the operator wants explicit-only re-index
(e.g. via the dashboard's Reindex button).

## 2. Delivery endpoint

```
POST /api/v1/webhooks/github/<path_hash>
```

- **Public in the auth sense** — no Bearer token or session cookie.
  Every body is HMAC-SHA256-validated against the per-row
  `webhook_secret` stored on the matching `git_repos` row. The header
  GitHub sends is `X-Hub-Signature-256: sha256=<hex>`.
- Validation lives in `server/internal/httpapi/webhooks.go`
  (`validHMAC`). HMACs are compared with `hmac.Equal` (constant-time)
  to prevent timing-side-channels on the secret.
- The secret is shown to the operator **exactly once** on add-repo and
  on the dashboard's **Project → Webhook info** action. There is no
  retrieval-after-the-fact path; rotating the secret means recreating
  the `git_repos` row.

Handled events:

| Event | Behaviour |
|---|---|
| `push` (tracked branch) | Enqueue a `clone_repo` job — `dedupe_key` collapses bursts of pushes (force-pushes, branch races) into one job. |
| `push` (other branch / delete) | 200 `{"status":"ignored"}`. The workspace tracks one branch per repo. |
| `ping` | 200 `{"status":"ping"}`. GitHub sends this on add; use it to confirm setup. |
| anything else | 200 `{"status":"ignored"}`, logged for audit. |

Any HMAC mismatch returns 401 with no body, regardless of event.

## 3. Public URL requirement

GitHub will not deliver to a localhost or RFC1918 address. The server
exposes webhook URLs based on `CIX_PUBLIC_URL` — set this to the
externally-reachable origin of the server (e.g.
`https://cix.example.com`). If unset, the dashboard hides the URL and
prints a hint instead of a 404 trap.

For self-hosted deployments without a static public IP, the simplest
no-cost answer is a **Cloudflare Tunnel** — see
[`WORKSPACES.md`](WORKSPACES.md#cloudflare-tunnel-recommended-for-self-hosted)
for the full recipe (`cloudflared tunnel create`, DNS routing,
`cloudflared tunnel run`).

## 4. Auto-register flow

When `webhook_mode=auto` and the PAT scope check passes:

1. Operator submits the add-repo form. The server clones the repo
   (`clone_repo` job) and starts indexing.
2. In parallel, the server calls `POST /repos/{owner}/{repo}/hooks`
   on GitHub via `server/internal/githubapi/`. The hook payload sets
   `events: ["push"]`, `content_type: json`, and embeds the
   server-generated `webhook_secret`.
3. GitHub responds with the hook id. The id is stored on the
   `git_repos` row so a later DELETE can call
   `DELETE /repos/{owner}/{repo}/hooks/{id}` cleanly.
4. The response payload includes `auto_registered: true` and the
   webhook URL becomes immediately ready for delivery.

Failure modes (all non-fatal — the response still succeeds with
`auto_registered: false` and an operator-facing note):

- PAT missing `admin:repo_hook`
- PAT lacks access to the target repo (private repo on someone else's
  org)
- Network error reaching api.github.com
- Repo already has a webhook pointing at this server's URL — server
  reuses the existing hook id rather than creating a duplicate

The operator sees the reason on the dashboard and can switch to
manual mode or rotate the PAT.

## 5. Manual register flow

If `webhook_mode=manual`, the dashboard shows the URL + secret after
add-repo and on the project detail page. Paste them into GitHub:

1. Repo → **Settings → Webhooks → Add webhook**.
2. **Payload URL** — the value from the dashboard.
3. **Content type** — `application/json`.
4. **Secret** — the value from the dashboard.
5. **Which events?** — **Just the push event**.
6. **Active** — ✓.

GitHub sends a `ping` immediately. cix returns 200 and GitHub's
webhook page marks the delivery green. After that, every `push` to the
tracked branch triggers a `clone_repo` job.

For automation, the same registration can be done with `gh`:

```bash
gh api -X POST \
  repos/<OWNER>/<REPO>/hooks \
  -f name=web \
  -F active=true \
  -f events[]=push \
  -f config[url]="$WEBHOOK_URL" \
  -f config[content_type]=json \
  -f config[secret]="$WEBHOOK_SECRET" \
  -f config[insecure_ssl]=0
```

## 6. Startup audit for stale URLs

When `CIX_PUBLIC_URL` changes (host migration, tunnel rebuild), every
`auto`-registered webhook in GitHub now points at the old origin.
On boot the server runs a one-shot audit
(`server/internal/workspaces/`, commit `9dac327`):

- For each `git_repos` row with `webhook_mode=auto` and a stored hook
  id, fetch the hook config from GitHub.
- Compare `config.url` to the canonical URL the server *would* now
  build from `CIX_PUBLIC_URL`.
- On mismatch: log a `WARN` line naming the repo and the stale URL.
  The server **does not auto-update** the hook — silently rewriting
  webhook URLs on every PAT-bearing repo at boot is too aggressive.
  The operator runs **Project → Reregister webhook** from the
  dashboard to fix each repo intentionally.

This is also why rotating `CIX_PUBLIC_URL` should be paired with a
"reregister all" sweep in the dashboard — there's no automatic
follow-up.

## 7. What gets re-indexed on a push

Each accepted `push` enqueues a `clone_repo` job, which:

1. Fetches into the existing clone directory (`git fetch` + reset to
   the tracked branch's new HEAD — no re-clone unless the local dir
   is missing).
2. Chains an `index_repo` job that runs the standard 3-phase
   indexer (begin → files → finish) against the new HEAD.
3. The indexer uses SHA-256 file hashes, so only changed files are
   re-embedded. A typical 5-file PR finishes in seconds.

The `dedupe_key` on the job table collapses bursts — five rapid
force-pushes only run the pipeline once. If something *is* in flight
when a new push arrives, the new push joins the same dedupe key and
re-runs once on completion.

## 8. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| 401 from cix on every delivery | Secret in GitHub doesn't match what cix stored. | Click **Webhook info** in the dashboard, paste the canonical value into GitHub. |
| 404 from cix | URL points at a stale `path_hash` (repo was deleted then re-added). | Run **Project → Reregister webhook**. |
| 200 `{"status":"ignored"}` and no re-index | Push was to a non-tracked branch, or `webhook_mode=off`. | Confirm the workspace's tracked branch; flip mode to `manual`/`auto`. |
| Auto-register failed with "missing scope" | PAT lacks `admin:repo_hook`. | Either grant the scope or switch the repo to `manual` and register by hand. |
| Audit logged `stale URL detected` on boot | `CIX_PUBLIC_URL` changed. | Run **Reregister webhook** on each affected project. |

## 9. Related files

- `server/internal/httpapi/webhooks.go` — delivery endpoint + HMAC check
- `server/internal/githubapi/` — GitHub REST client for hook CRUD
- `server/internal/workspaces/` — webhook lifecycle + startup audit
- [`WORKSPACES.md`](WORKSPACES.md) — operator guide (encryption keys, Cloudflare tunnel)
- [`../workspaces.md`](../workspaces.md) — user-facing workspace guide
