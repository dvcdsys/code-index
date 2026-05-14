#!/usr/bin/env bats
# Tests for grep-nudge.sh

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

@test "cache=1: emits nudge on first call" {
    make_cache "sess-1" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-1" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]
    [[ "$output" == *"PostToolUse"* ]]
    [[ "$output" == *"cix search"* ]]
}

@test "cache=0: silent, no counter change" {
    make_cache "sess-0" "$TEST_PROJECT_DIR" "0"

    for i in 1 2 3 4 5; do
        run_hook grep-nudge.sh "sess-0" "$TEST_PROJECT_DIR"
        [ "$status" -eq 0 ]
        [ -z "$output" ]
    done

    [ "$(read_counter 'sess-0' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "cache absent: silent, no counter change" {
    for i in 1 2 3; do
        run_hook grep-nudge.sh "sess-abs" "$TEST_PROJECT_DIR"
        [ "$status" -eq 0 ]
        [ -z "$output" ]
    done

    [ "$(read_counter 'sess-abs' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "exponential backoff: nudge fires on calls 1, 2, 4, 8, 16; silent otherwise" {
    make_cache "sess-bo" "$TEST_PROJECT_DIR" "1"

    local nudge_calls=()
    for i in $(seq 1 20); do
        run_hook grep-nudge.sh "sess-bo" "$TEST_PROJECT_DIR"
        if [[ "$output" == *"hookSpecificOutput"* ]]; then
            nudge_calls+=("$i")
        fi
    done

    # Expected: 1, 2, 4, 8, 16
    [ "${#nudge_calls[@]}" -eq 5 ]
    [ "${nudge_calls[0]}" = "1" ]
    [ "${nudge_calls[1]}" = "2" ]
    [ "${nudge_calls[2]}" = "4" ]
    [ "${nudge_calls[3]}" = "8" ]
    [ "${nudge_calls[4]}" = "16" ]
}

@test "counter persists across invocations" {
    make_cache "sess-ctr" "$TEST_PROJECT_DIR" "1"

    run_hook grep-nudge.sh "sess-ctr" "$TEST_PROJECT_DIR"
    [ "$(read_counter 'sess-ctr' "$TEST_PROJECT_DIR")" = "1" ]

    run_hook grep-nudge.sh "sess-ctr" "$TEST_PROJECT_DIR"
    [ "$(read_counter 'sess-ctr' "$TEST_PROJECT_DIR")" = "2" ]

    run_hook grep-nudge.sh "sess-ctr" "$TEST_PROJECT_DIR"
    [ "$(read_counter 'sess-ctr' "$TEST_PROJECT_DIR")" = "3" ]
}

@test "different projects: separate counters per (session, dir)" {
    local DIR_A="${BATS_TMPDIR}/proj-A-$$"
    local DIR_B="${BATS_TMPDIR}/proj-B-$$"
    mkdir -p "$DIR_A" "$DIR_B"

    make_cache "sess-pp" "$DIR_A" "1"
    make_cache "sess-pp" "$DIR_B" "1"

    # 5 calls in A, 2 calls in B
    for i in 1 2 3 4 5; do
        run_hook grep-nudge.sh "sess-pp" "$DIR_A"
    done
    for i in 1 2; do
        run_hook grep-nudge.sh "sess-pp" "$DIR_B"
    done

    [ "$(read_counter 'sess-pp' "$DIR_A")" = "5" ]
    [ "$(read_counter 'sess-pp' "$DIR_B")" = "2" ]

    rm -rf "$DIR_A" "$DIR_B"
}

@test "missing session_id: silent" {
    make_cache "any-sess" "$TEST_PROJECT_DIR" "1"

    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/grep-nudge.sh" <<<"{}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "does NOT call cix CLI (cache-only)" {
    make_cache "sess-nc" "$TEST_PROJECT_DIR" "1"

    run_hook grep-nudge.sh "sess-nc" "$TEST_PROJECT_DIR"
    run_hook grep-nudge.sh "sess-nc" "$TEST_PROJECT_DIR"

    # Mock cix should never have been invoked.
    [ "$(mock_cix_call_count)" -eq 0 ]
}

# ── Bash branch — grep-family detection in tool_input.command ────────────────
#
# These tests cover the matcher: "Bash" entry in hooks.json. The script only
# proceeds to the nudge for commands that contain a grep-family token; other
# Bash invocations stay silent AND must not bump the backoff counter.

@test "Bash + grep -rn: nudge fires" {
    make_cache "sess-bg" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-bg" "$TEST_PROJECT_DIR" "Bash" "grep -rn foo ."
    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]
    [[ "$output" == *"cix search"* ]]
}

@test "Bash + rg: nudge fires" {
    make_cache "sess-rg" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-rg" "$TEST_PROJECT_DIR" "Bash" "rg pattern src/"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + ripgrep: nudge fires" {
    make_cache "sess-rip" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-rip" "$TEST_PROJECT_DIR" "Bash" "ripgrep --json foo"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + egrep/fgrep: nudge fires" {
    make_cache "sess-eg" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-eg" "$TEST_PROJECT_DIR" "Bash" "egrep -i pat file"
    [[ "$output" == *"hookSpecificOutput"* ]]

    rm -f "$TEST_CACHE_DIR/cix-grep-count-sess-eg-"*
    run_hook grep-nudge.sh "sess-eg" "$TEST_PROJECT_DIR" "Bash" "fgrep pat file"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + chained grep (cd && grep): nudge fires" {
    make_cache "sess-cd" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-cd" "$TEST_PROJECT_DIR" "Bash" "cd x && grep foo y"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + piped grep (cat | grep | head): nudge fires" {
    make_cache "sess-pipe" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-pipe" "$TEST_PROJECT_DIR" "Bash" "cat f | grep bar | head"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + ls: silent, counter stays at 0" {
    make_cache "sess-ls" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-ls" "$TEST_PROJECT_DIR" "Bash" "ls -la"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [ "$(read_counter 'sess-ls' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + git status: silent" {
    make_cache "sess-gs" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-gs" "$TEST_PROJECT_DIR" "Bash" "git status"
    [ -z "$output" ]
    [ "$(read_counter 'sess-gs' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + make test: silent" {
    make_cache "sess-mk" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-mk" "$TEST_PROJECT_DIR" "Bash" "make test"
    [ -z "$output" ]
    [ "$(read_counter 'sess-mk' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + go test: silent" {
    make_cache "sess-go" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-go" "$TEST_PROJECT_DIR" "Bash" "go test ./..."
    [ -z "$output" ]
    [ "$(read_counter 'sess-go' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + grepl (substring, not standalone token): silent" {
    # `grepl` is a substring of `grep` but a different binary. Regex must
    # not match it. Same idea covers `egrep_helper`, `mygrep`, etc.
    make_cache "sess-sub" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-sub" "$TEST_PROJECT_DIR" "Bash" "grepl --help"
    [ -z "$output" ]
    [ "$(read_counter 'sess-sub' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + find -name: nudge fires" {
    make_cache "sess-fn" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-fn" "$TEST_PROJECT_DIR" "Bash" "find . -name '*.go'"
    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]
    [[ "$output" == *"cix search"* ]]
}

@test "Bash + find -type: nudge fires" {
    make_cache "sess-ft" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-ft" "$TEST_PROJECT_DIR" "Bash" "find src -type f"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + piped find (find | head): nudge fires" {
    make_cache "sess-fp" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-fp" "$TEST_PROJECT_DIR" "Bash" "find . -name '*.tsx' | head -20"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Bash + findme (substring, not standalone token): silent" {
    # `findme`, `findutils`, `mlfind`, etc. share a substring with `find`
    # but are different binaries. Regex must not match them.
    make_cache "sess-fsub" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-fsub" "$TEST_PROJECT_DIR" "Bash" "findme --help"
    [ -z "$output" ]
    [ "$(read_counter 'sess-fsub' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Bash + git grep: nudge fires (git grep is still grep)" {
    # `git grep` is a grep-family command — same intent as plain `grep`,
    # cix should still be suggested. The regex matches because `grep`
    # follows whitespace as a standalone token.
    make_cache "sess-gg" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-gg" "$TEST_PROJECT_DIR" "Bash" "git grep foo"
    [[ "$output" == *"hookSpecificOutput"* ]]
}

@test "Unknown tool_name (Read): silent" {
    make_cache "sess-rd" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-rd" "$TEST_PROJECT_DIR" "Read"
    [ -z "$output" ]
    [ "$(read_counter 'sess-rd' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "Glob tool: nudge fires (existing behavior preserved)" {
    make_cache "sess-gl" "$TEST_PROJECT_DIR" "1"
    run_hook grep-nudge.sh "sess-gl" "$TEST_PROJECT_DIR" "Glob"
    [[ "$output" == *"hookSpecificOutput"* ]]
}
