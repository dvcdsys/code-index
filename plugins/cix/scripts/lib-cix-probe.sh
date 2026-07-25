#!/usr/bin/env bash
# Shared cix-status probe helpers for the cix plugin hooks.
#
# Sourced (not executed) by session-start.sh, cwd-changed.sh, and
# grep-nudge.sh. Defines functions only — no side effects on source.
#
# The three-state verdict ("1" / "0" / "unknown") is the contract these
# hooks share via the cache file $CLAUDE_PLUGIN_DATA/cix-aware-<sid>-<hash>:
#   "1"        — project is indexed; nudges allowed
#   "0"        — definitively not indexed / not registered; stay silent
#   "unknown"  — couldn't determine (cix status timed out / server slow);
#                a later grep-nudge re-probes and upgrades this to 0 or 1
#
# Why "unknown" matters: previously a timeout at SessionStart wrote "0",
# which silenced the nudge for the ENTIRE session even if the cix server
# came up seconds later. "unknown" lets the next Grep re-probe and recover.

# cix_server_configured — exit 0 when this machine has any cix server
# configured (env override or a url in ~/.cix/config.yaml), nonzero
# otherwise. Used by session-start.sh to tell "no server at all" (worth a
# one-time setup hint) apart from "server exists, project not indexed"
# (stay silent). A heuristic on purpose: any `url:` key counts, whether a
# named server entry or a legacy api.url — false positives just suppress
# the hint, which is the safe direction.
cix_server_configured() {
    [ -n "${CIX_API_URL:-}" ] && return 0
    local cfg="${HOME:-}/.cix/config.yaml"
    [ -f "$cfg" ] && grep -qE 'url:[[:space:]]*[^[:space:]#]' "$cfg" && return 0
    return 1
}

# cix_resolve_bin — echo a usable cix binary path, or empty string.
# Prefers the plugin-bundled wrapper so behavior matches the slash commands.
cix_resolve_bin() {
    if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/cix" ]; then
        printf '%s' "${CLAUDE_PLUGIN_ROOT}/bin/cix"
    elif command -v cix >/dev/null 2>&1; then
        command -v cix
    fi
}

# cix_probe_verdict <cix_bin> <project_dir> [timeout_secs]
# Runs `cix status -p <project_dir>` under a timeout and echoes a verdict:
#   "1"        — exit 0 (indexed)
#   "0"        — clean nonzero exit (not indexed / not registered)
#   "unknown"  — had to be killed (timed out)
# Uses timeout(1) / gtimeout(1) when available (coreutils); otherwise a
# pure-bash poll loop (macOS without coreutils). The poll loop hard-kills
# with SIGKILL, matching the prior hook behavior.
cix_probe_verdict() {
    local cix_bin="$1" project_dir="$2" secs="${3:-2}"
    local rc=0

    if command -v timeout >/dev/null 2>&1; then
        timeout "$secs" "$cix_bin" status -p "$project_dir" >/dev/null 2>&1 || rc=$?
        [ "$rc" = "124" ] && { printf 'unknown'; return 0; }
    elif command -v gtimeout >/dev/null 2>&1; then
        gtimeout "$secs" "$cix_bin" status -p "$project_dir" >/dev/null 2>&1 || rc=$?
        [ "$rc" = "124" ] && { printf 'unknown'; return 0; }
    else
        # Pure-bash fallback: background the call and poll in 0.1s steps.
        local exit_file pid slept iters
        exit_file="$(mktemp 2>/dev/null || printf '/tmp/cix-probe-%s.exit' "$$")"
        (
            "$cix_bin" status -p "$project_dir" >/dev/null 2>&1
            echo "$?" > "$exit_file" 2>/dev/null
        ) &
        pid=$!
        slept=0
        iters=$((secs * 10))
        while kill -0 "$pid" 2>/dev/null && [ "$slept" -lt "$iters" ]; do
            sleep 0.1
            slept=$((slept + 1))
        done
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
            rm -f "$exit_file"
            printf 'unknown'
            return 0
        fi
        wait "$pid" 2>/dev/null || true
        rc=1
        [ -f "$exit_file" ] && rc=$(cat "$exit_file" 2>/dev/null || echo 1)
        rm -f "$exit_file"
        case "$rc" in ''|*[!0-9]*) rc=1 ;; esac
    fi

    if [ "$rc" = "0" ]; then
        printf '1'
    else
        printf '0'
    fi
}
