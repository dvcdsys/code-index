# Security & deployment notes

This document captures the operational requirements that the cix-server
codebase assumes but does not enforce on its own. Read it before exposing
the dashboard to users beyond a single trusted operator.

## Trusted-proxy posture for `X-Forwarded-For`

The server reads `X-Forwarded-For` (first hop) when present and uses the
result for two things:

1. **Audit metadata** — stored as `sessions.last_seen_ip` and
   `api_keys.last_used_ip`.
2. **Per-IP login rate limit key** — see "Login brute-force resistance"
   below. The per-(IP, email) key still binds independently of the IP
   source, so password guessing against a known account is rate-limited
   regardless; only the global per-IP sweep cap depends on the header
   being trustworthy.

This makes the trusted-proxy posture **load-bearing for security**, not
just for audit honesty. Two safe deployments:

- **Reverse proxy in front** (Cloudflare / Caddy / nginx / Traefik / ALB):
  configure the proxy to *replace* the inbound `X-Forwarded-For` with the
  real client IP, not append to it. Drop `X-Real-IP` if you don't need
  it. This is the recommended posture for any internet-exposed
  deployment.
- **Direct exposure on a trusted network** (LAN / VPN only): nothing
  forwards `X-Forwarded-For` for you, so an attacker who can reach the
  port can also forge the header. The per-(IP, email) cap still slows
  password guessing, but the global per-IP cap is bypassable. Acceptable
  on a trusted network, never on the open internet.

Example for nginx:

```nginx
location / {
  proxy_set_header X-Forwarded-For $remote_addr;  # replace, not append
  proxy_set_header Host $host;
  proxy_pass http://cix-server:21847;
}
```

## TLS

The session cookie's `Secure` attribute is set automatically when the
request arrives over TLS (`r.TLS != nil`). For any deployment beyond
`localhost`, terminate TLS in front of the server and ensure the server
sees TLS-marked requests so the cookie is not sent in cleartext.

If you front the server with a TLS-terminating proxy that downgrades to
plain HTTP for the upstream hop, the auto-detection will return false and
`Secure` will be omitted. Two fixes:

- Terminate TLS directly in cix-server (drop the proxy).
- Or configure the proxy to make the upstream hop look TLS-marked — the
  details vary; consult the proxy docs.

## Authenticating reverse proxy / Zero-Trust gateway

If you put an authenticating proxy in front of cix (Cloudflare Access,
oauth2-proxy, Authelia, an SSO/mTLS-terminating LB), the browser dashboard
passes it via interactive SSO, but the `cix` CLI and AI-agent tooling send only
the cix Bearer and get bounced at the edge (302/403) unless their source IP is
allow-listed. The CLI can attach **custom request headers** (e.g. a Cloudflare
Access service token) on every request to satisfy the edge layer in addition to
the cix Bearer — the proxy validates and strips them, so cix needs no knowledge
of the proxy. See
[`CLI_CONFIG.md` → Custom request headers](CLI_CONFIG.md#custom-request-headers-auth-behind-a-reverse-proxy).

## Login brute-force resistance

POST `/api/v1/auth/login` is rate-limited in process (`internal/httpapi/loginlimiter.go`):

- **5 failed attempts per (IP, email) per 15 minutes** — slows guessing
  against a known account. Cleared on a successful login so a user who
  fat-fingers their password a few times is not stuck.
- **60 attempts per IP per minute** — slows horizontal sweeps across many
  emails from a single source. Not cleared on a successful login.

This is a single-process limiter; multi-replica deployments do not share
state. If you scale out, put a shared throttle (Redis, your reverse proxy)
in front of `/api/v1/auth/login` or accept that the per-replica caps are
the floor.

## Request body size limits

A request-body middleware rejects oversize payloads up-front:

- **Default cap: 1 MiB** for every endpoint.
- **Indexing cap: 64 MiB** for `POST /api/v1/projects/{path}/index/files`,
  which legitimately receives JSON-encoded source from a batch of files.
  At default config (batch=20, max-file=512 KiB) a real payload is ~11 MiB;
  the cap also covers operator-tuned worst case (batch=50 × max-file=1 MiB
  ≈ 55 MiB) with headroom.

The cap fires on `Content-Length` (clean 413) and on chunked-transfer
overflow (the JSON decoder fails and the handler returns 422). If your
indexer batches need more than 64 MiB, raise `indexingMaxBodyBytes` in
`internal/httpapi/middleware.go` rather than asking operators to disable
the cap.

## Bootstrap admin

On a fresh database the server reads `CIX_BOOTSTRAP_ADMIN_EMAIL` and
`CIX_BOOTSTRAP_ADMIN_PASSWORD` and creates the first admin row, marked
`must_change_password=1` so the operator must change the password on
first login.

- Both env vars must be set together; setting only one is a fatal
  startup error.
- Once the users table is non-empty, the env vars are ignored. Rotating
  the bootstrap password by editing the env has no effect on a running
  installation — go through the dashboard or directly through SQLite.
- The bootstrap path is **not transactional**. If two server instances
  start simultaneously against the same fresh database, one of them will
  fail with a UNIQUE-constraint error from the duplicate email. This is
  intentional (better to fail loud than silently create two admins) but
  operationally surprising under HA-style deployments — start a single
  instance first, then scale out.

## Password policy

The server enforces only `len(password) >= 8`. There is no complexity
rule, no breached-password dictionary check, no rotation prompt.

For internet-exposed deployments, choose admin passwords accordingly: a
20+ character random passphrase from a password manager beats anything
the server could enforce. The rate limiter above caps the damage of
weak passwords at ~480 guesses per (IP, email) per day.

## No self-service password reset

A user who forgets their password cannot reset it themselves. Recovery
options (in order of preference):

1. Another admin issues `POST /api/v1/admin/users` with a new initial
   password and `must_change_password=1`, then disables the old account.
2. Direct SQLite access to clear `users.disabled_at` and reset
   `users.password_hash` (use bcrypt cost 12).

Plan for this when designating admins — keep at least two so an admin
reset never requires DB-level intervention.

## API key scoping

API keys inherit the full permissions of their owning user. A
non-admin user's key can do anything that user can (read their own
projects + projects/workspaces shared to a view-group they belong to,
write to projects they own); an admin's key can do anything an admin
can. There is no read-only scoping, no per-project scoping, no
expiry.

Roles in the system today are `admin` and `user`. For automated
callers (CI, scripts) that only need to read shared workspace search
results, create a dedicated `user`-role account, add it to the
relevant view-groups, and issue keys from that account. Rotate keys
via `DELETE /api/v1/api-keys/{id}` rather than reusing them.

## What the server does NOT do

If your threat model needs any of these, build them in front of cix-server
or accept the risk:

- **CSRF tokens.** Protection relies on the cookie's `SameSite=Strict` +
  `HttpOnly` attributes, which modern browsers honour. There is no
  separate token to validate.
- **CORS.** No `Access-Control-Allow-*` headers are emitted; same-origin
  is the assumption.
- **WAF / IDS.** No IP allowlisting, no anomaly detection. Use your
  reverse proxy or a host-level firewall.
- **Multi-tenant tenant isolation.** cix-server has a single shared
  user/role/group namespace (introduced in `e275c4a`, server v0.6.0).
  Within it: local projects are owned by their creator and private to
  the owner + admins; workspaces and external projects (admin-added
  GitHub repos) are admin-administered and shared to view-groups
  explicitly. Non-admins see only what they own or what is shared to a
  group they belong to. This is single-tenant-with-row-level-ACL, not
  true tenant separation — admins still see everything and pass
  through every ACL. If you need hard tenant boundaries, run separate
  cix-server instances per tenant.
