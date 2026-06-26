# Dashboard, authentication & authorization

The dashboard ships embedded in the server binary at `/dashboard`. No extra
service to run, no nginx config, no separate static-files volume.

## Pages

| Page | Audience | What it does |
|------|----------|--------------|
| **Home** | everyone | Live status strip (server version, current embedding model, sidecar Ready/Loading), update-available banner when a newer `server/v*` release is published on GitHub, module shortcuts. |
| **Projects** | everyone | List indexed projects with stats (file count, languages, symbols, vector count, sqlite/chroma sizes), per-project **Reindex** button + live indexing indicator, copy reindex commands. Cards turn red with a **Stale model** badge when the runtime embedding model differs from the model the project was indexed with (see [Drift indicator](#drift-indicator)). |
| **Workspaces** | everyone | Group multiple repositories into a named workspace and search them as one corpus. The in-dashboard add-repo flow streams clone + index progress live; pick the org/account first, then the repo. Status tracking: `pending` → `cloning` → `indexing` → `indexed` / `failed`. Hybrid BM25 + dense search across the whole group. See [`../workspaces.md`](../workspaces.md). |
| **Search** | everyone | Five modes: semantic, symbols, references, definitions, files. Same engine the CLI uses. |
| **API Keys** | everyone | Mint long-lived `cix_*` keys (256-bit entropy, GitHub-class), copy them once, revoke at any time. Keys inherit the issuing user's role. |
| **GitHub Tokens** | admin | Store personal access tokens used by external (cloned) projects + workspaces. Tokens are AES-256-GCM encrypted at rest; the plaintext is returned once on creation and never again. Scopes are **derived from GitHub** at storage time (not user-declared), so the dashboard shows the PAT's true capabilities. |
| **Users** | admin | Invite teammates, set role (admin / user), reset password (forces change on next login), disable account. |
| **Groups** | admin | Manage *view-groups* — named user sets used to share projects and workspaces with specific people. Add/remove members, grant shares from the project or workspace detail page. |
| **Managed Tunnels** | admin | Enable a Cloudflare Tunnel or ngrok tunnel to give the server a public origin for GitHub webhook ingress from behind NAT. Configure provider, mode (quick / named), and credentials; agent binary auto-installs on demand; live status + restart + round-trip test. |
| **Settings** | everyone | Theme, default editor, change own password. |
| **Server** | admin | Runtime config — embedding model, `n_ctx`, `n_gpu_layers`, `n_threads`, batch size, queue concurrency. **Save & Restart** drains in-flight embeddings, restarts the sidecar, polls until ready. Source pill on each field shows whether the live value comes from the DB override, env bootstrap, or the recommended fallback. |

## Authentication

Two paths share the same identity model:

- **Cookie session** (browser) — `cix_session` HttpOnly cookie, 14-day rolling
  TTL, `sha256(token)` stored in DB. The raw token never leaves the browser.
- **Bearer API key** (CLI / agents / CI) — `Authorization: Bearer cix_<43-char-base64url>`
  header. 256 bits of entropy, hex-`sha256`-stored, scoped to the issuing
  user's role.

## Authorization model

Two roles: **`admin`** and **`user`**. On top of roles, every resource has
explicit visibility:

- **Local projects** (indexed via `cix init` on a developer's machine) belong
  to the user who ran the init and are private to that user. Project identity
  is per-machine — the same path on two different machines never collides.
- **External projects** (cloned by the server from GitHub) are *ownerless* and
  admin-administered. They become visible to others only through a
  **view-group share**.
- **Workspaces** are owned by their creator; sharing works the same way as
  projects.
- **View-groups** are admin-managed named user sets. Grant a group a share on a
  project or workspace from its detail page; every group member then sees it as
  if they owned it (read-only). Admins always see everything.

Every endpoint enforces this model server-side — the dashboard hides controls
the caller isn't allowed to use, and the CLI surfaces a 404 (not a 403) when
probing a resource the caller has no business knowing exists. Full hardening
posture: [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md).

## Drift indicator

When you change the runtime embedding model (Server → Embedding model → Save &
Restart), every project indexed with the previous model becomes stale —
vectors are no longer comparable to fresh queries. The dashboard surfaces this
with red borders + `Stale model` badges on project cards, and a banner on the
project detail page with a copy-to-clipboard `cix reindex --full <path>`
command. After running the reindex, the drift signal clears automatically.

## Disabled-embeddings mode

Set `CIX_EMBEDDINGS_ENABLED=false` to bring the server up without the
llama-server sidecar — auth, dashboard, project metadata, and symbol / file
searches all keep working; only semantic search and indexing are disabled. The
Server page renders a warning banner and disables the relevant inputs.
