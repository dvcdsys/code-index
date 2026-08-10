#!/bin/bash
# swap.sh — replace a running .app with a staged copy, then reopen it.
#
# Embedded into cix-launcher and written to a temp file at update time. It has
# to be a separate process for one reason: the application being replaced is the
# one running the update, and a process cannot outlive the deletion of its own
# bundle to reopen it.
#
#   $1  pid of the launcher to wait for
#   $2  live bundle    (/Applications/cix.app)
#   $3  staged bundle  (/Applications/.cix.app.new)
#   $4  log file
#
# Deliberately /bin/bash, not /usr/bin/env bash: this runs detached, after the
# app is gone, with whatever environment launchd or the shell left behind. The
# system bash is the one path guaranteed to exist.
set -uo pipefail

LAUNCHER_PID="${1:?pid required}"
LIVE="${2:?live bundle required}"
STAGED="${3:?staged bundle required}"
LOG="${4:-/dev/null}"

log() { printf '%s  swap: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG"; }

log "waiting for launcher pid $LAUNCHER_PID to exit"
# Bounded: a launcher that never exits must not leave a script polling forever,
# and swapping the bundle out from under a running process is how you get a
# half-updated app.
for _ in $(seq 1 120); do
    kill -0 "$LAUNCHER_PID" 2>/dev/null || break
    sleep 0.5
done
if kill -0 "$LAUNCHER_PID" 2>/dev/null; then
    log "launcher still running after 60s — aborting, nothing was changed"
    exit 1
fi

if [ ! -d "$STAGED" ]; then
    log "staged bundle missing at $STAGED — aborting"
    exit 1
fi

OLD="${LIVE}.old"
rm -rf "$OLD"

# Move the live app aside rather than deleting it. If the second move fails —
# a full disk, a permissions change between the preflight and now — there is
# still a complete application on disk to put back, which "rm -rf then copy"
# would not leave.
if [ -d "$LIVE" ]; then
    if ! mv "$LIVE" "$OLD"; then
        log "could not move $LIVE aside — aborting, the installed app is untouched"
        exit 1
    fi
fi

if ! mv "$STAGED" "$LIVE"; then
    log "could not move the staged bundle into place; restoring the previous app"
    mv "$OLD" "$LIVE" || log "RESTORE FAILED — the previous app is at $OLD"
    exit 1
fi

rm -rf "$OLD"
log "replaced $LIVE"

# Reopen. -n would start a second instance; the old one is gone, so a plain
# open is what puts the user back where they were.
open "$LIVE" || log "could not reopen $LIVE"
log "done"
