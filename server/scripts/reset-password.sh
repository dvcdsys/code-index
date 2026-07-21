#!/usr/bin/env bash
# reset-password.sh — offline admin/user password recovery. Wraps
# `cix-server -reset-password`, targeting whichever deployment the
# installer set up:
#
#   - native install → runs the built binary with the same .env the
#     server uses
#   - Docker install → runs the binary inside this clone's container
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

compose() {
    if docker compose version >/dev/null 2>&1; then
        docker compose "$@"
    elif command -v docker-compose >/dev/null 2>&1; then
        docker-compose "$@"
    else
        return 1
    fi
}

# container_running <name-or-id> — "true" only for a container that exists
# and is up. Asking docker inspect directly beats `docker ps | grep -q`,
# which under `pipefail` can report a MATCH as a failure when grep exits
# first and docker takes a SIGPIPE.
container_running() {
    docker inspect "$1" --format '{{.State.Running}}' 2>/dev/null || true
}

# docker_container — prints "<name> <origin>" for the running cix container
# to exec into. The compose files do not pin container_name (a fixed name
# collides with any other cix on the host), so ask compose for the real
# <project>-<service>-1 name; compose matches on its own project labels,
# which also covers containers created by older, container_name-pinned
# versions of these files.
#
# origin is `compose` for that lookup and `legacy` for the fixed-name last
# resort — the caller must not treat them alike. A container matched by the
# fixed name alone is one this clone cannot claim, and on a host that runs a
# cix of its own (our Portainer box) it is somebody else's server.
docker_container() {
    local id name
    id=$( (cd "$REPO_ROOT" && compose ps -q 2>/dev/null || true) )
    id="${id%%$'\n'*}"   # first line only; `| head` + pipefail = SIGPIPE
    if [[ -n "$id" && "$(container_running "$id")" == "true" ]]; then
        name=$(docker inspect "$id" --format '{{.Name}}' 2>/dev/null | sed 's|^/||')
        if [[ -n "$name" ]]; then
            printf '%s compose' "$name"
            return 0
        fi
    fi
    if [[ "$(container_running code-index)" == "true" ]]; then
        printf 'code-index legacy'
        return 0
    fi
    return 1
}

# confirm_foreign <name> — consent gate for the legacy fallback. Read from
# the terminal, never from stdin: stdin may be carrying a piped password, and
# eating that line would answer this prompt with it. No terminal (CI, an
# agent, a cron job) means no consent — abort rather than reset a password in
# a server that may not be ours.
confirm_foreign() {
    local answer=""
    printf 'Container %s is NOT part of this clone (%s) — it may belong to\n' "$1" "$REPO_ROOT" >&2
    printf 'another cix installation on this host.\n' >&2
    printf 'Reset the password inside it anyway? [y/N] ' >&2
    # 2>/dev/null FIRST: redirections are applied left to right, so with
    # `</dev/tty 2>/dev/null` the "no such device" complaint is printed
    # before stderr is silenced.
    read -r answer 2>/dev/null </dev/tty || true
    case "$answer" in
        [Yy]|[Yy][Ee][Ss]) return 0 ;;
        *) echo "Aborted — nothing was changed." >&2; return 1 ;;
    esac
}

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
elif command -v docker >/dev/null 2>&1 && RESOLVED=$(docker_container); then
    CONTAINER="${RESOLVED% *}"       # container names never contain a space
    [[ "${RESOLVED##* }" == legacy ]] && { confirm_foreign "$CONTAINER" || exit 1; }
    echo "Resetting in Docker container: $CONTAINER"
    reset() { docker exec -i "$CONTAINER" /cix-server -reset-password "$EMAIL"; }
else
    echo "ERROR: no cix-server found — neither a native build ($BINARY)" >&2
    echo "       nor a running cix Docker container. Run ./install-server.sh first." >&2
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
