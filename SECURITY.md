# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| latest  | yes       |

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report privately via GitHub: go to **Security → Report a vulnerability** in this repository.

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (optional)

You will receive a response within 7 days.

## Threat model

`code-index` is designed for **self-hosted, private network use**. It is not hardened for exposure to the public internet without a reverse proxy.

Key assumptions:
- The API server runs inside a trusted network (LAN or localhost)
- The `API_KEY` is the sole authentication mechanism
- All indexed code is assumed non-sensitive from the server's perspective — the server stores embeddings and code chunks, not secrets

## Known risks and mitigations

### API key exposure

The `API_KEY` is a bearer token sent in every request. If transmitted over plain HTTP on a public network, it can be intercepted.

**Mitigation:** Put a TLS-terminating reverse proxy (Nginx, Caddy, Traefik) in front of the server before exposing it outside a trusted network.

### Code chunk storage

The server stores raw code chunks in ChromaDB and SQLite. Anyone with access to `~/.cix/data/` can read indexed source code.

**Mitigation:** Restrict filesystem permissions on `~/.cix/data/` to the owning user.

### No rate limiting

The API has no built-in rate limiting. A client with a valid API key can exhaust memory by triggering large indexing jobs.

**Mitigation:** Run behind a reverse proxy with rate limiting, or restrict API key access to trusted clients only.

### Docker socket / host mounts

The container mounts `~/.cix/data/` from the host. A compromised container could write to that directory.

**Mitigation:** Use a named Docker volume instead of a bind mount if the threat model requires stricter isolation (see `portainer-stack.yml`).

## Branch protection

The `main` branch is protected:
- Direct pushes are blocked — all changes require a pull request
- At least **1 approval** from a contributor is required before merging

## Secure deployment checklist

- [ ] `API_KEY` is randomly generated (≥32 hex bytes) — `setup.sh` does this automatically
- [ ] Server is not directly exposed on a public IP without TLS
- [ ] `~/.cix/data/` is readable only by the owning user (`chmod 700`)
- [ ] Docker container runs as a non-root user (default in the provided `Dockerfile`)
- [ ] Firewall restricts port `21847` to trusted IPs if not behind a reverse proxy
