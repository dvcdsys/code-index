#!/usr/bin/env bash
# reset-password.sh — offline admin/user password recovery. Wraps
# `cix-server -reset-password`, targeting whichever deployment the
# installer set up:
#
#   - native install → runs the built binary with the same .env the
#     server uses
#   - Docker install → runs the binary inside the code-index container
#
#   ./server/scripts/reset-password.sh you@example.com
#
# Prompts for a new password (empty = generate a strong one and print it).
# The account is forced to change it on next login and all its sessions are
# revoked. The server may keep running — no restart needed.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
BINARY="$REPO_ROOT/server/dist/cix-darwin-arm64/cix-server"

if [[ $# -ne 1 || "$1" == "-h" || "$1" == "--help" ]]; then
    echo "usage: $0 <email>" >&2
    exit 1
fi
EMAIL="$1"

# reset <email> — feeds stdin (one line: the new password, or empty for
# auto-generate) to the right cix-server -reset-password invocation.
if [[ -x "$BINARY" ]]; then
    if [[ -f "$ENV_FILE" ]]; then
        set -a
        # shellcheck source=/dev/null
        source "$ENV_FILE"
        set +a
    fi
    reset() { "$BINARY" -reset-password "$EMAIL"; }
elif command -v docker >/dev/null 2>&1 \
        && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "code-index"; then
    reset() { docker exec -i code-index /cix-server -reset-password "$EMAIL"; }
else
    echo "ERROR: no cix-server found — neither a native build ($BINARY)" >&2
    echo "       nor a running 'code-index' Docker container. Run ./install-server.sh first." >&2
    exit 1
fi

read -r -p "New password for $EMAIL (empty = auto-generate): " -s PASSWORD; echo
if [[ -z "$PASSWORD" ]]; then
    printf '\n' | reset
else
    read -r -p "Repeat password: " -s CONFIRM; echo
    [[ "$PASSWORD" == "$CONFIRM" ]] || { echo "ERROR: passwords do not match" >&2; exit 1; }
    printf '%s\n' "$PASSWORD" | reset
fi
