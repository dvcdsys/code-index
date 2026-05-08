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
    [[ "$output" == *"PreToolUse"* ]]
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
