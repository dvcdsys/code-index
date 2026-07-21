#!/usr/bin/env bash
# install-server.sh — interactive installer for cix-server. Picks a
# deployment mode (native macOS / Docker CPU / Docker GPU), asks a few
# questions, and brings the server up.
#
# From a clone:
#   git clone https://github.com/dvcdsys/code-index && cd code-index
#   ./install-server.sh
#
# Or in one line (clones the repo for you, then continues inside it):
#   curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install-server.sh | bash
#
# Re-running is safe: an existing database and .env are detected and kept.
#
# Flags (all optional — without them the script prompts interactively):
#   --mode <m>           native | docker | docker-gpu
#   --email <addr>       admin email for the first account
#   --port <n>           HTTP port        (default: the .env value, else 21847)
#   --data-dir <path>    data directory, native mode    (default ~/.cix/data;
#                        Docker modes always use ~/.cix/data — set in compose)
#   --run-mode <m>       native mode only: launchd | manual
#   --dir <path>         where to clone when run outside a repo (default ./code-index)
#   --non-interactive    accept defaults, never prompt (requires --email on a
#                        fresh install; generates the admin password)
#   --uninstall          stop + remove the server (data and .env are kept)
#   --help               this text

set -euo pipefail

REPO_URL="https://github.com/dvcdsys/code-index"
PLIST_LABEL="com.cix.server"
PLIST_PATH="$HOME/Library/LaunchAgents/$PLIST_LABEL.plist"
LAUNCHER_DIR="$HOME/.cix/launchd"
LAUNCHER="$LAUNCHER_DIR/run-cix-server.sh"
LOG_DIR="$HOME/.cix/logs"

# ── ui helpers ────────────────────────────────────────────────────────────
# tput exits non-zero on capability-poor terminals (TERM=dumb, some CI) —
# `|| true` keeps set -e from killing the script before its first line.
BOLD="" DIM="" RED="" GREEN="" YELLOW="" RESET=""
if [[ -t 1 ]]; then
    BOLD=$(tput bold 2>/dev/null || true) DIM=$(tput dim 2>/dev/null || true)
    RED=$(tput setaf 1 2>/dev/null || true) GREEN=$(tput setaf 2 2>/dev/null || true)
    YELLOW=$(tput setaf 3 2>/dev/null || true) RESET=$(tput sgr0 2>/dev/null || true)
fi
say()  { printf '%s\n' "$1"; }
step() { printf '\n%s▸ %s%s\n' "$BOLD" "$1" "$RESET"; }
ok()   { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$1"; }
die()  { printf '%s✗ %s%s\n' "$RED" "$1" "$RESET" >&2; exit 1; }

# ask VAR "Question" "default" — prompt with default; honors NON_INTERACTIVE.
ask() {
    local __var="$1" __q="$2" __def="$3" __ans
    if [[ "$NON_INTERACTIVE" == true ]]; then
        printf -v "$__var" '%s' "$__def"
        return
    fi
    read -r -p "$__q [$__def]: " __ans
    printf -v "$__var" '%s' "${__ans:-$__def}"
}

# ask_choice VAR "Question" "default" opt1 "desc1" opt2 "desc2" … — numbered
# menu; the user answers with a number (or the option word), Enter picks the
# default. Re-asks on invalid input instead of dying.
ask_choice() {
    local __var="$1" __q="$2" __def="$3"; shift 3
    local __opts=() __descs=()
    while [[ $# -gt 0 ]]; do __opts+=("$1"); __descs+=("$2"); shift 2; done
    if [[ "$NON_INTERACTIVE" == true ]]; then
        printf -v "$__var" '%s' "$__def"
        return
    fi
    local __i __defnum=1
    say ""
    say "$__q"
    for __i in "${!__opts[@]}"; do
        [[ "${__opts[$__i]}" == "$__def" ]] && __defnum=$((__i + 1))
        printf '  %s%d) %-11s%s %s\n' "$BOLD" $((__i + 1)) "${__opts[$__i]}" "$RESET" "${__descs[$__i]}"
    done
    local __ans
    while true; do
        read -r -p "Choice [$__defnum]: " __ans
        __ans="${__ans:-$__defnum}"
        if [[ "$__ans" =~ ^[0-9]+$ ]] && (( __ans >= 1 && __ans <= ${#__opts[@]} )); then
            printf -v "$__var" '%s' "${__opts[$((__ans - 1))]}"
            return
        fi
        for __i in "${!__opts[@]}"; do
            if [[ "$__ans" == "${__opts[$__i]}" ]]; then
                printf -v "$__var" '%s' "${__opts[$__i]}"
                return
            fi
        done
        warn "enter a number 1-${#__opts[@]} (or press Enter for the default)"
    done
}

# ask_yn "Question" default(y|n) — yes/no prompt; returns 0 for yes.
# NON_INTERACTIVE takes the default.
ask_yn() {
    local __q="$1" __def="$2" __ans __hint="[Y/n]"
    [[ "$__def" == "n" ]] && __hint="[y/N]"
    if [[ "$NON_INTERACTIVE" == true ]]; then
        [[ "$__def" == "y" ]]
        return
    fi
    read -r -p "$__q $__hint: " __ans
    __ans="${__ans:-$__def}"
    [[ "$__ans" == "y" || "$__ans" == "Y" || "$__ans" == "yes" ]]
}

gen_secret() { # gen_secret <len> — unambiguous alphanumerics
    # || true: head(1) closing the pipe SIGPIPEs tr, which pipefail would
    # otherwise turn into a fatal exit under set -e.
    LC_ALL=C tr -dc 'a-km-np-zA-HJ-NP-Z2-9' </dev/urandom | head -c "$1" || true
}

# wait_health <port> — poll /health for up to ~5 minutes.
wait_health() {
    local url="http://localhost:$1/health"
    say "${DIM}First boot downloads the embedding model (~600 MB) before serving — this can take a few minutes.${RESET}"
    for _ in $(seq 1 100); do
        if curl -fsS -m 2 "$url" >/dev/null 2>&1; then
            ok "server is healthy at $url"
            return 0
        fi
        sleep 3
    done
    warn "server not answering yet — it may still be downloading the model."
    return 1
}

# ── flags ─────────────────────────────────────────────────────────────────
ORIG_ARGS=("$@")
NON_INTERACTIVE=false
UNINSTALL=false
SKIP_UPDATE=false
ARG_MODE="" ARG_EMAIL="" ARG_PORT="" ARG_DATA_DIR="" ARG_RUN_MODE="" ARG_CLONE_DIR=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-update) SKIP_UPDATE=true; shift ;;
        --mode)      ARG_MODE="$2"; shift 2 ;;
        --email)     ARG_EMAIL="$2"; shift 2 ;;
        --port)      ARG_PORT="$2"; shift 2 ;;
        --data-dir)  ARG_DATA_DIR="$2"; shift 2 ;;
        --run-mode)  ARG_RUN_MODE="$2"; shift 2 ;;
        --dir)       ARG_CLONE_DIR="$2"; shift 2 ;;
        --non-interactive) NON_INTERACTIVE=true; shift ;;
        --uninstall) UNINSTALL=true; shift ;;
        --help|-h)   sed -n '2,27p' "${BASH_SOURCE[0]:-$0}" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown flag: $1 (see --help)" ;;
    esac
done

# curl | bash leaves stdin on the pipe — rewire prompts to the terminal.
if [[ "$NON_INTERACTIVE" == false && ! -t 0 ]]; then
    if [[ -r /dev/tty ]]; then
        exec </dev/tty
    else
        die "no terminal available for prompts — pass --non-interactive (with --email on a fresh install)"
    fi
fi

# ── bootstrap: running outside a clone? clone, then delegate ──────────────
SCRIPT_PATH="${BASH_SOURCE[0]:-}"
IN_REPO=false
if [[ -n "$SCRIPT_PATH" && -f "$SCRIPT_PATH" ]]; then
    REPO_ROOT="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
    [[ -f "$REPO_ROOT/server/Makefile" ]] && IN_REPO=true
fi

if [[ "$IN_REPO" == false ]]; then
    step "cix-server installer"
    say "Not inside a code-index clone — cloning first."
    command -v git >/dev/null || die "git is required: install it and re-run"
    ask CLONE_DIR "Where should the repo live?" "${ARG_CLONE_DIR:-$PWD/code-index}"
    CLONE_DIR="${CLONE_DIR/#\~/$HOME}"
    if [[ -f "$CLONE_DIR/server/Makefile" ]]; then
        ok "existing clone found at $CLONE_DIR — reusing it"
    else
        [[ -e "$CLONE_DIR" ]] && die "$CLONE_DIR exists but is not a code-index clone"
        git clone "$REPO_URL" "$CLONE_DIR"
    fi
    ARGS=()
    [[ -n "$ARG_MODE"     ]] && ARGS+=(--mode "$ARG_MODE")
    [[ -n "$ARG_EMAIL"    ]] && ARGS+=(--email "$ARG_EMAIL")
    [[ -n "$ARG_PORT"     ]] && ARGS+=(--port "$ARG_PORT")
    [[ -n "$ARG_DATA_DIR" ]] && ARGS+=(--data-dir "$ARG_DATA_DIR")
    [[ -n "$ARG_RUN_MODE" ]] && ARGS+=(--run-mode "$ARG_RUN_MODE")
    [[ "$NON_INTERACTIVE" == true ]] && ARGS+=(--non-interactive)
    [[ "$UNINSTALL" == true ]] && ARGS+=(--uninstall)
    exec bash "$CLONE_DIR/install-server.sh" ${ARGS[@]+"${ARGS[@]}"}
fi

BUNDLE_DIR="$REPO_ROOT/server/dist/cix-darwin-arm64"
ENV_FILE="$REPO_ROOT/.env"

# env_get <KEY> — value of the last KEY= line in .env (quotes and stray
# whitespace stripped), empty when the key or the file is absent.
# || true: an absent key is normal here, but pipefail would turn grep's
# exit 1 into a fatal error under set -e.
env_get() {
    [[ -f "$ENV_FILE" ]] || return 0
    grep -E "^$1=" "$ENV_FILE" | tail -1 | cut -d= -f2- | tr -d "'\"" | tr -d '[:space:]' || true
}

# ── clone freshness: don't install from a stale checkout ──────────────────
# A user may clone, get distracted, and come back weeks later — meanwhile
# the repo (and this very script) moved on. Offer a fast-forward update and
# restart the installer so the NEW script runs. Every failure path degrades
# to "install from what's here": offline, detached HEAD, forks, diverged
# branches and dirty trees must never block an install.
if [[ "$SKIP_UPDATE" == false && "$UNINSTALL" == false ]] \
    && git -C "$REPO_ROOT" rev-parse --abbrev-ref '@{u}' >/dev/null 2>&1; then
    step "Checking for updates"
    if GIT_TERMINAL_PROMPT=0 git -C "$REPO_ROOT" fetch --quiet 2>/dev/null; then
        BEHIND=$(git -C "$REPO_ROOT" rev-list --count 'HEAD..@{u}' 2>/dev/null || echo 0)
        if [[ "$BEHIND" -gt 0 ]]; then
            warn "this clone is $BEHIND commit(s) behind $(git -C "$REPO_ROOT" rev-parse --abbrev-ref '@{u}')"
            if [[ "$NON_INTERACTIVE" == true ]]; then
                warn "continuing with the current checkout (interactive runs offer to update)"
            elif [[ -n "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=no)" ]]; then
                warn "working tree has local changes — not updating automatically; run 'git pull' yourself if you want the latest"
            elif ask_yn "Update now (git pull --ff-only) and restart the installer?" y; then
                if git -C "$REPO_ROOT" pull --ff-only --quiet; then
                    ok "updated — restarting the installer"
                    exec bash "$REPO_ROOT/install-server.sh" --skip-update ${ORIG_ARGS[@]+"${ORIG_ARGS[@]}"}
                else
                    warn "fast-forward pull failed (diverged history?) — continuing with the current checkout"
                fi
            fi
        else
            ok "clone is up to date"
        fi
    else
        warn "could not reach the git remote (offline?) — continuing with the current checkout"
    fi
fi

# docker compose v2 plugin vs legacy binary.
compose() {
    if docker compose version >/dev/null 2>&1; then
        docker compose "$@"
    elif command -v docker-compose >/dev/null; then
        docker-compose "$@"
    else
        die "docker compose not found — install Docker (with the compose plugin) and re-run"
    fi
}

# image_created <ref> — YYYY-MM-DD a local image was built, empty when the
# image is not on this host. Used to date the image we fall back to when a
# pull fails, so "reusing the local image" is never a silent claim.
image_created() {
    local __c
    __c=$(docker image inspect "$1" --format '{{.Created}}' 2>/dev/null || true)
    printf '%s' "${__c%%T*}"
}

# ── uninstall ─────────────────────────────────────────────────────────────
if [[ "$UNINSTALL" == true ]]; then
    step "Uninstalling cix-server"
    removed=false
    if [[ -f "$PLIST_PATH" ]]; then
        if launchctl bootout "gui/$(id -u)/$PLIST_LABEL" 2>/dev/null; then
            ok "launchd agent stopped"
        else
            warn "launchd agent was not running"
        fi
        rm -f "$PLIST_PATH" "$LAUNCHER"
        ok "removed $PLIST_PATH"
        removed=true
    fi
    if command -v docker >/dev/null && docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "code-index"; then
        (cd "$REPO_ROOT" && compose down) && ok "docker container removed (named model volume kept)"
        removed=true
    fi
    [[ "$removed" == false ]] && warn "nothing to uninstall (no launchd agent, no code-index container)"
    say ""
    say "Kept: your data directory and $ENV_FILE."
    say "To wipe all indexed data too:  rm -rf ~/.cix/data   (irreversible)"
    exit 0
fi

# ── mode ──────────────────────────────────────────────────────────────────
step "cix-server installer"

OS="$(uname -s)" ARCH="$(uname -m)"
HAS_NVIDIA=false
command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi >/dev/null 2>&1 && HAS_NVIDIA=true

MODE_DEFAULT="docker"
if [[ "$OS" == "Darwin" && "$ARCH" == "arm64" ]]; then
    MODE_DEFAULT="native"
elif [[ "$HAS_NVIDIA" == true ]]; then
    MODE_DEFAULT="docker-gpu"
fi

if [[ -n "$ARG_MODE" ]]; then
    MODE="$ARG_MODE"
else
    ask_choice MODE "How do you want to run the server?" "$MODE_DEFAULT" \
        native     "— build + run directly on this Mac (Apple Silicon, full Metal GPU)" \
        docker     "— Docker container, CPU embeddings (any OS)" \
        docker-gpu "— Docker container, NVIDIA CUDA embeddings"
fi
case "$MODE" in
    native)
        [[ "$OS" == "Darwin" ]]   || die "native mode supports macOS only — pick docker/docker-gpu on $OS"
        [[ "$ARCH" == "arm64" ]]  || die "native mode supports Apple Silicon only (the bundled llama-server is arm64) — pick docker on Intel Macs"
        ;;
    docker) ;;
    docker-gpu)
        [[ "$HAS_NVIDIA" == true ]] || warn "nvidia-smi not working — docker-gpu needs an NVIDIA GPU + driver ≥ 525; continuing anyway"
        ;;
    *) die "mode must be native, docker, or docker-gpu" ;;
esac

# ── prerequisites ─────────────────────────────────────────────────────────
step "Checking prerequisites ($MODE)"
command -v curl >/dev/null || die "curl is required"

if [[ "$MODE" == "native" ]]; then
    command -v go >/dev/null || die "Go is required: brew install go   (or https://go.dev/dl)"
    GO_VER=$(go env GOVERSION | sed 's/^go//')
    GO_MAJOR=${GO_VER%%.*}; GO_MINOR=$(echo "$GO_VER" | cut -d. -f2)
    if (( GO_MAJOR < 1 || (GO_MAJOR == 1 && GO_MINOR < 25) )); then
        die "Go 1.25+ required, found go$GO_VER: brew upgrade go"
    fi
    ok "Go $GO_VER"
    xcode-select -p >/dev/null 2>&1 || die "Xcode Command Line Tools required: xcode-select --install"
    ok "Xcode Command Line Tools"
    command -v npm >/dev/null || die "Node.js is required (builds the dashboard): brew install node   (or https://nodejs.org)"
    ok "Node.js $(node --version)"
else
    command -v docker >/dev/null || die "Docker is required: https://docs.docker.com/get-docker/"
    docker info >/dev/null 2>&1   || die "Docker daemon is not running — start Docker and re-run"
    ok "Docker $(docker --version | sed 's/Docker version //;s/,.*//')"
    compose version >/dev/null 2>&1 || die "docker compose not found — install the compose plugin"
    ok "docker compose"
    if [[ "$MODE" == "docker-gpu" ]]; then
        if docker info 2>/dev/null | grep -qi nvidia; then
            ok "NVIDIA container runtime"
        else
            warn "NVIDIA Container Toolkit not detected in 'docker info' — install nvidia-container-toolkit or the GPU won't be visible"
        fi
    fi
fi

# ── questions ─────────────────────────────────────────────────────────────
step "A few questions (Enter accepts the default)"

FRESH=true
if [[ "$MODE" == "native" ]]; then
    ask DATA_DIR "Data directory" "${ARG_DATA_DIR:-$HOME/.cix/data}"
    DATA_DIR="${DATA_DIR/#\~/$HOME}"
else
    # docker-compose.yml binds ${HOME}/.cix/data:/data — fixed by the file.
    DATA_DIR="$HOME/.cix/data"
    say "Data directory: $DATA_DIR ${DIM}(set in docker-compose.yml — edit the bind there to change)${RESET}"
fi
SQLITE_PATH="$DATA_DIR/sqlite/projects.db"
if [[ -f "$SQLITE_PATH" ]]; then
    FRESH=false
    ok "existing installation detected ($SQLITE_PATH)"
    say "${DIM}Accounts and indexes are kept, so the admin email/password questions are skipped —${RESET}"
    say "${DIM}your existing login keeps working. Forgot it? ./server/scripts/reset-password.sh <email>${RESET}"
    say "${DIM}For a truly fresh start, move the data away first:  mv ~/.cix/data ~/.cix/data.bak${RESET}"
fi

# Which key pins the port depends on the mode: native runs the server
# itself (CIX_PORT is its listen port), while the Docker modes only map a
# host port (the container always listens on 21847).
PORT_KEY="PORT"
[[ "$MODE" == "native" ]] && PORT_KEY="CIX_PORT"
ENV_PORT="$(env_get "$PORT_KEY")"
if [[ -n "$ENV_PORT" && ! "$ENV_PORT" =~ ^[0-9]+$ ]]; then
    warn "$ENV_FILE pins a non-numeric $PORT_KEY ($ENV_PORT) — it will be replaced"
    ENV_PORT=""
fi
[[ -n "$ENV_PORT" ]] && say "Current port in $ENV_FILE: ${BOLD}$ENV_PORT${RESET} ${DIM}(answer differently to change it)${RESET}"

# An existing .env supplies the default, so Enter is a no-op on a re-run;
# an explicit --port still wins. A different answer is written back to the
# kept .env by sync_env_port — otherwise the question would be decorative
# and the server would come up on the old port.
ask PORT "HTTP port" "${ARG_PORT:-${ENV_PORT:-21847}}"
[[ "$PORT" =~ ^[0-9]+$ ]] || die "port must be a number"
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    if [[ "$FRESH" == false && ( -z "$ENV_PORT" || "$PORT" == "$ENV_PORT" ) ]]; then
        warn "port $PORT is in use — assuming it's the running cix-server; it will be restarted"
    else
        # Fresh install, or a re-run moving to a DIFFERENT port: whatever is
        # listening there is not the cix-server we are about to (re)start.
        die "port $PORT is already in use (lsof -i :$PORT) — pick another port or stop that process"
    fi
fi

ADMIN_EMAIL="" ADMIN_PASSWORD="" PASSWORD_GENERATED=false
if [[ "$FRESH" == true ]]; then
    if [[ "$NON_INTERACTIVE" == true && -z "$ARG_EMAIL" ]]; then
        die "--non-interactive on a fresh install requires --email"
    fi
    ask ADMIN_EMAIL "Admin email (first dashboard account)" "${ARG_EMAIL:-$(git config user.email 2>/dev/null || echo admin@example.com)}"
    [[ "$ADMIN_EMAIL" == *@* && ! "$ADMIN_EMAIL" =~ [\'\"\\\$\`[:space:]\#] ]] || die "that doesn't look like an email address"

    if [[ "$NON_INTERACTIVE" == true ]]; then
        ADMIN_PASSWORD="$(gen_secret 20)"; PASSWORD_GENERATED=true
    else
        read -r -p "Admin password — leave empty to auto-generate a strong one []: " -s ADMIN_PASSWORD; echo
        if [[ -z "$ADMIN_PASSWORD" ]]; then
            ADMIN_PASSWORD="$(gen_secret 20)"; PASSWORD_GENERATED=true
        else
            (( ${#ADMIN_PASSWORD} >= 8 )) || die "password must be at least 8 characters"
            # .env is parsed by three different tools (bash source, xargs,
            # docker compose) with three different quoting dialects — keep
            # the TEMPORARY password trivially parseable instead of trying
            # to escape for all of them. The real password is set at first
            # login and has no such restriction.
            if [[ "$ADMIN_PASSWORD" =~ [\'\"\\\$\`[:space:]\#] ]]; then
                die "the temporary password must not contain quotes, backslashes, \$, backticks, # or spaces — it lives in .env briefly; you'll set your real password (no restrictions) at first login"
            fi
            read -r -p "Repeat password: " -s CONFIRM; echo
            [[ "$ADMIN_PASSWORD" == "$CONFIRM" ]] || die "passwords do not match"
        fi
    fi
    say "${DIM}Whatever the source, this password is temporary — the dashboard forces a change on first login.${RESET}"
fi

RUN_MODE="launchd"
if [[ "$MODE" == "native" ]]; then
    if [[ -n "$ARG_RUN_MODE" ]]; then
        RUN_MODE="$ARG_RUN_MODE"
    else
        ask_choice RUN_MODE "How should the server run?" "launchd" \
            launchd "— in the background, starts automatically at login" \
            manual  "— in the foreground, you start it yourself with 'make run'"
    fi
    [[ "$RUN_MODE" == "launchd" || "$RUN_MODE" == "manual" ]] || die "run mode must be 'launchd' or 'manual'"
fi

# ── .env ──────────────────────────────────────────────────────────────────
write_env() {
    API_KEY="cix_$(gen_secret 48)"
    {
        echo "# Generated by install-server.sh ($MODE) on $(date '+%Y-%m-%d %H:%M'). Safe to edit."
        echo "# Full reference: doc/CONFIG_REFERENCE.md"
        echo ""
        echo "# Imported as the 'env-bootstrap' API key for the admin on first boot."
        echo "CIX_API_KEY=$API_KEY"
        if [[ "$FRESH" == true ]]; then
            echo ""
            echo "# First-boot admin seed. Ignored once the database has users —"
            echo "# you may delete both lines after your first login."
            echo "CIX_BOOTSTRAP_ADMIN_EMAIL=$ADMIN_EMAIL"
            echo "CIX_BOOTSTRAP_ADMIN_PASSWORD=$ADMIN_PASSWORD"
        fi
        echo ""
        if [[ "$MODE" == "native" ]]; then
            echo "CIX_PORT=$PORT"
            echo "CIX_SQLITE_PATH=$SQLITE_PATH"
            echo "CIX_CHROMA_PERSIST_DIR=$DATA_DIR/chroma"
            echo "CIX_GGUF_CACHE_DIR=$DATA_DIR/models"
            echo ""
            echo "# On macOS the server defaults to offloading all layers to Metal;"
            echo "# uncomment to force CPU-only embedding:"
            echo "# CIX_N_GPU_LAYERS=0"
        else
            echo "# Host port mapping used by docker-compose.yml (container stays on 21847)."
            echo "PORT=$PORT"
        fi
    } > "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    ok "wrote $ENV_FILE (mode 600)"
}

# sync_env_port — make a kept .env agree with the answered port. Without
# this the port question is decorative on a re-run: .env wins, so the
# server (re)starts on the old port while wait_health polls the new one for
# five minutes and every URL in the summary points at nothing.
sync_env_port() {
    if ! grep -qE "^$PORT_KEY=" "$ENV_FILE"; then
        {
            echo ""
            if [[ "$MODE" == "native" ]]; then
                echo "# HTTP port, added by install-server.sh on $(date '+%Y-%m-%d %H:%M')."
            else
                echo "# Host port mapping used by docker-compose.yml (container stays on 21847)."
            fi
            echo "$PORT_KEY=$PORT"
        } >> "$ENV_FILE"
        ok "added $PORT_KEY=$PORT to the existing .env"
    elif [[ "$ENV_PORT" != "$PORT" ]]; then
        sed -i.bak "s|^$PORT_KEY=.*|$PORT_KEY=$PORT|" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
        ok "updated $PORT_KEY in $ENV_FILE: ${ENV_PORT:-unset} → $PORT"
    fi

    # A .env left over from a NATIVE install still pins the server's own
    # listen port. Reused for Docker, the container would listen on that
    # port while compose maps host $PORT → 21847 — nothing answers, and the
    # summary URL lies exactly like the bug this function fixes.
    if [[ "$MODE" != "native" ]]; then
        local inner
        inner="$(env_get CIX_PORT)"
        if [[ -n "$inner" && "$inner" != "21847" ]]; then
            warn "$ENV_FILE sets CIX_PORT=$inner (left over from a native install) — the container would listen there while compose maps host $PORT → 21847. Delete that line before starting."
        fi
    fi
}

# ensure_env — write .env if absent; otherwise keep it, but never drop the
# admin answers: a fresh DB + a pre-existing .env without the seed vars
# (typical on dev machines) would make the server refuse to boot.
ensure_env() {
    if [[ ! -f "$ENV_FILE" ]]; then
        write_env
        return
    fi
    ok "keeping existing $ENV_FILE (delete it and re-run to regenerate)"
    sync_env_port
    # An API key is optional for the server itself, but on first boot it is
    # imported as the admin's 'env-bootstrap' key — which is what lets the
    # CLI connect right after install. Guarantee one exists (and replace
    # the .env.example placeholder if it was copied verbatim).
    if grep -q '^CIX_API_KEY=cix_<' "$ENV_FILE"; then
        local fresh_key
        fresh_key="cix_$(gen_secret 48)"
        sed -i.bak "s|^CIX_API_KEY=cix_<.*|CIX_API_KEY=$fresh_key|" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
        ok "replaced the placeholder CIX_API_KEY with a generated one"
    elif ! grep -q '^CIX_API_KEY=' "$ENV_FILE"; then
        {
            echo ""
            echo "# Imported as the 'env-bootstrap' API key for the admin on first boot."
            echo "CIX_API_KEY=cix_$(gen_secret 48)"
        } >> "$ENV_FILE"
        ok "added a generated CIX_API_KEY to the existing .env"
    fi
    if [[ "$FRESH" == true ]] && ! grep -q '^CIX_BOOTSTRAP_ADMIN_EMAIL=' "$ENV_FILE"; then
        {
            echo ""
            echo "# First-boot admin seed added by install-server.sh on $(date '+%Y-%m-%d %H:%M')."
            echo "# Ignored once the database has users — you may delete both lines after"
            echo "# your first login."
            echo "CIX_BOOTSTRAP_ADMIN_EMAIL=$ADMIN_EMAIL"
            echo "CIX_BOOTSTRAP_ADMIN_PASSWORD=$ADMIN_PASSWORD"
        } >> "$ENV_FILE"
        ok "added the first-boot admin seed to the existing .env"
    fi
}

# ── install per mode ──────────────────────────────────────────────────────
# SERVER_STATE drives the honesty of the final summary: running | starting
# (came up slow / still downloading the model) | not_started (manual mode).
SERVER_STATE=not_started
LOGS_CMD=""
if [[ "$MODE" == "native" ]]; then
    step "Building cix-server + dashboard, fetching Metal llama-server (first run takes a few minutes)"
    make -C "$REPO_ROOT/server" bundle
    ok "built $BUNDLE_DIR"

    step "Writing configuration"
    ensure_env
    mkdir -p "$DATA_DIR" "$LOG_DIR"

    if [[ "$RUN_MODE" == "manual" ]]; then
        SERVER_STATE=not_started
        LOGS_CMD="(server logs print to the terminal it runs in)"
    else
        step "Installing launchd agent ($PLIST_LABEL)"
        mkdir -p "$LAUNCHER_DIR" "$(dirname "$PLIST_PATH")"

        # The launcher keeps .env as the single source of truth — the plist
        # never duplicates configuration, so editing .env + restarting is
        # enough.
        cat > "$LAUNCHER" <<EOF
#!/usr/bin/env bash
# Generated by install-server.sh — launchd entry point for cix-server.
set -euo pipefail
set -a
source "$ENV_FILE"
set +a
export CIX_LLAMA_BIN_DIR="$BUNDLE_DIR/llama"
exec "$BUNDLE_DIR/cix-server"
EOF
        chmod 755 "$LAUNCHER"

        cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$PLIST_LABEL</string>
  <key>ProgramArguments</key>
  <array><string>$LAUNCHER</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$LOG_DIR/cix-server.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/cix-server.err</string>
</dict></plist>
EOF

        # Restart cleanly if a previous agent exists; bootstrap is the
        # modern load verb (falls back to load for older macOS).
        launchctl bootout "gui/$(id -u)/$PLIST_LABEL" 2>/dev/null || true
        launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null \
            || launchctl load "$PLIST_PATH"
        ok "agent loaded — starts automatically at login, respawns on crash"

        step "Waiting for the server to come up"
        LOGS_CMD="tail -f $LOG_DIR/cix-server.err"
        if wait_health "$PORT"; then
            SERVER_STATE=running
        else
            SERVER_STATE=starting
            warn "Watch progress with:  $LOGS_CMD"
        fi
    fi
else
    COMPOSE_ARGS=()
    IMAGE_UID=65532
    IMAGE_REF="dvcdsys/code-index:latest"
    if [[ "$MODE" == "docker-gpu" ]]; then
        COMPOSE_ARGS=(-f "$REPO_ROOT/docker-compose.cuda.yml")
        IMAGE_UID=1001
        IMAGE_REF="dvcdsys/code-index:cu128"
    fi

    step "Writing configuration"
    ensure_env
    mkdir -p "$DATA_DIR"

    # The container writes /data as a non-root uid; a Linux bind mount
    # owned by someone else fails on first write. macOS Docker Desktop
    # maps ownership transparently — no action needed there.
    if [[ "$OS" == "Linux" ]]; then
        DIR_UID=$(stat -c %u "$DATA_DIR" 2>/dev/null || echo "?")
        if [[ "$DIR_UID" != "$IMAGE_UID" ]]; then
            warn "the image writes $DATA_DIR as uid $IMAGE_UID, but the directory is owned by uid $DIR_UID."
            warn "If the container fails to start, run:  sudo chown -R $IMAGE_UID:$IMAGE_UID $DATA_DIR"
        fi
    fi

    # The literals above are only for messages; ask compose what it will
    # actually run so a tag change in the compose file can't make us lie.
    # Older compose builds lack `config --images` — then the literal stands.
    IMAGE_FROM_COMPOSE=$( (cd "$REPO_ROOT" && compose ${COMPOSE_ARGS[@]+"${COMPOSE_ARGS[@]}"} config --images 2>/dev/null | head -1) || true )
    [[ -n "$IMAGE_FROM_COMPOSE" ]] && IMAGE_REF="$IMAGE_FROM_COMPOSE"

    # `up -d` only pulls when the image is MISSING locally, so without an
    # explicit pull any host that ever pulled this tag installs whatever it
    # happens to have — silently, health check and all.
    step "Pulling the image"
    if (cd "$REPO_ROOT" && compose ${COMPOSE_ARGS[@]+"${COMPOSE_ARGS[@]}"} pull); then
        IMAGE_BUILT=$(image_created "$IMAGE_REF")
        ok "$IMAGE_REF is current${IMAGE_BUILT:+ (built $IMAGE_BUILT)}"
    else
        # Offline, or the registry is down. Starting from the local image is
        # the right degradation — the rest of this script degrades the same
        # way — but it must be visible, and dated, not a silent stale run.
        IMAGE_BUILT=$(image_created "$IMAGE_REF")
        [[ -n "$IMAGE_BUILT" ]] \
            || die "could not pull $IMAGE_REF and no local copy exists — check the network (or Docker Hub) and re-run"
        warn "could not pull $IMAGE_REF — starting from the local image, built $IMAGE_BUILT"
        warn "That may be several releases behind. Re-run the installer once the network is back to upgrade."
    fi

    step "Starting the container"
    (cd "$REPO_ROOT" && compose ${COMPOSE_ARGS[@]+"${COMPOSE_ARGS[@]}"} up -d)
    ok "container started"

    step "Waiting for the server to come up"
    LOGS_CMD="docker logs -f code-index"
    if wait_health "$PORT"; then
        SERVER_STATE=running
    else
        SERVER_STATE=starting
        warn "Watch progress with:  $LOGS_CMD"
    fi
fi

# ── cix CLI: install + connect ────────────────────────────────────────────
# On a FRESH first boot the server imports CIX_API_KEY from .env as the
# admin's 'env-bootstrap' API key — so the CLI can be wired up right here,
# no dashboard round-trip needed. On an existing installation we can't know
# a valid key, so we only hint.
CLI_CONFIGURED=false
if [[ "$NON_INTERACTIVE" == false ]]; then
    step "cix CLI"
    if ! command -v cix >/dev/null 2>&1; then
        if ask_yn "Install the cix CLI (recommended — it's what you and your agents run)?" y; then
            bash "$REPO_ROOT/install.sh"
        fi
    else
        ok "cix CLI already installed ($(command -v cix))"
        # Same version detection as install.sh: dev builds print no vX.Y.Z
        # token and are left alone (that's a developer, not an end user).
        CLI_CUR=$(cix --version 2>/dev/null | head -1 \
            | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*' | head -1 || true)
        if [[ -n "$CLI_CUR" ]]; then
            CLI_LATEST=$(curl -fsSL --max-time 10 "https://api.github.com/repos/dvcdsys/code-index/releases?per_page=30" 2>/dev/null \
                | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/' \
                | grep '^cli/v' | head -1 || true)
            CLI_LATEST="${CLI_LATEST#cli/}"
            if [[ -n "$CLI_LATEST" && "$CLI_CUR" != "$CLI_LATEST" ]]; then
                if ask_yn "cix $CLI_CUR is installed, $CLI_LATEST is available — upgrade now?" y; then
                    bash "$REPO_ROOT/install.sh"
                fi
            elif [[ -n "$CLI_LATEST" ]]; then
                ok "cix CLI is up to date ($CLI_CUR)"
            fi
        fi
    fi
    if command -v cix >/dev/null 2>&1 && [[ "$FRESH" == true ]]; then
        if ask_yn "Connect the CLI to this server (saved as server alias 'local')?" y; then
            # Snapshot BEFORE the first `cix config set` — it bootstraps a
            # config file with a stub `default_server: default` entry, which
            # would otherwise fool the "respect an existing default" check.
            HAD_CLI_CONFIG=false
            [[ -f "$HOME/.cix/config.yaml" ]] && HAD_CLI_CONFIG=true
            API_KEY_VALUE=$(grep '^CIX_API_KEY=' "$ENV_FILE" | tail -1 | cut -d= -f2- | tr -d "'\"")
            cix config set server.local.url "http://localhost:$PORT"
            cix config set server.local.key "$API_KEY_VALUE"
            # On a first-ever CLI config there is nothing to compete with —
            # claim the default silently. If a REAL default already exists
            # (e.g. a corporate server), switching it is the user's call.
            if [[ "$HAD_CLI_CONFIG" == false ]] || ! grep -qs '^default_server:' "$HOME/.cix/config.yaml"; then
                cix config set default_server local
                ok "CLI connected — 'local' is now the default server"
            elif ask_yn "Your CLI already has a default server — make 'local' the new default?" n; then
                cix config set default_server local
                ok "CLI connected — 'local' is now the default server"
            else
                ok "CLI connected as alias 'local' (kept your existing default; use --server local to target this one)"
            fi
            CLI_CONFIGURED=true
        fi
    fi
fi

# ── summary ───────────────────────────────────────────────────────────────
step "Install complete 🟢"
say ""
case "$SERVER_STATE" in
    running)
        say "  Server:      ${GREEN}running${RESET}"
        say "  Dashboard:   ${BOLD}http://localhost:$PORT/dashboard${RESET}"
        ;;
    starting)
        say "  Server:      ${YELLOW}NOT ready yet${RESET} — still starting (first boot downloads the embedding model)"
        say "               watch:  $LOGS_CMD"
        say "               ready when this returns ok:  curl http://localhost:$PORT/health"
        say "  Dashboard:   ${BOLD}http://localhost:$PORT/dashboard${RESET}   ${DIM}(once the server is ready)${RESET}"
        ;;
    not_started)
        say "  Server:      ${YELLOW}NOT running${RESET} — manual mode never starts it for you"
        say "               start it with:  ${BOLD}cd server && make run${RESET}"
        say "  Dashboard:   ${BOLD}http://localhost:$PORT/dashboard${RESET}   ${DIM}(after you start the server)${RESET}"
        ;;
esac
if [[ "$FRESH" == true ]]; then
    say "  Admin login: $ADMIN_EMAIL"
    if [[ "$PASSWORD_GENERATED" == true ]]; then
        say "  Temporary password: ${BOLD}$ADMIN_PASSWORD${RESET}"
    else
        say "  Temporary password: (the one you typed)"
    fi
    say "  ${DIM}You will be asked to change it on first login.${RESET}"
fi
say ""
say "  Next steps:"
N=1
if [[ "$SERVER_STATE" == "not_started" ]]; then
    say "    $N. Start the server (foreground):  ${BOLD}cd server && make run${RESET}"
    N=$((N + 1))
elif [[ "$SERVER_STATE" == "starting" ]]; then
    say "    $N. Wait until the server is ready (watch: $LOGS_CMD)."
    N=$((N + 1))
fi
say "    $N. Sign in to the dashboard and change the password when prompted."
N=$((N + 1))
if [[ "$CLI_CONFIGURED" == true ]]; then
    say "    $N. In a repo you want indexed:  cix init   (the CLI is already connected)"
else
    say "    $N. Dashboard → API Keys → New key, then connect the CLI:"
    say "         curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install.sh | bash"
    say "    $((N + 1)). In a repo you want indexed:  cix init"
fi
say ""
if [[ "$MODE" == "native" && "$RUN_MODE" == "launchd" ]]; then
    say "  Manage the server:"
    say "    logs       tail -f $LOG_DIR/cix-server.err"
    say "    restart    launchctl kickstart -k gui/$(id -u)/$PLIST_LABEL"
    say "    stop       launchctl bootout gui/$(id -u)/$PLIST_LABEL"
    say "    uninstall  ./install-server.sh --uninstall"
    say "    upgrade    git pull && ./install-server.sh   (data and settings are kept)"
elif [[ "$MODE" != "native" ]]; then
    CF=""
    [[ "$MODE" == "docker-gpu" ]] && CF=" -f docker-compose.cuda.yml"
    say "  Manage the server:"
    say "    logs       docker logs -f code-index"
    say "    restart    docker compose$CF restart"
    say "    stop       docker compose$CF down          (data survives)"
    say "    uninstall  ./install-server.sh --uninstall"
    say "    upgrade    git pull && docker compose$CF pull && docker compose$CF up -d"
fi
say "  Forgot the admin password later?   ./server/scripts/reset-password.sh <email>"
say ""
