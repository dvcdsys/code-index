#!/usr/bin/env bats
# Tests for post-compact.sh

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

@test "cache=1: re-injects reminder" {
    make_cache "sess-pc" "$TEST_PROJECT_DIR" "1"
    run_hook post-compact.sh "sess-pc" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]
    [[ "$output" == *"PostCompact"* ]]
    [[ "$output" == *"Post-compact reminder"* ]]
}

@test "cache=0: silent" {
    make_cache "sess-0" "$TEST_PROJECT_DIR" "0"
    run_hook post-compact.sh "sess-0" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "cache absent: silent" {
    run_hook post-compact.sh "sess-abs" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "missing session_id: silent" {
    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/post-compact.sh" <<<"{}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "uses correct cache file for current project" {
    local DIR_A="${BATS_TMPDIR}/proj-A-$$"
    local DIR_B="${BATS_TMPDIR}/proj-B-$$"
    mkdir -p "$DIR_A" "$DIR_B"

    make_cache "sess-multi" "$DIR_A" "1"
    make_cache "sess-multi" "$DIR_B" "0"

    # In dir A → reminder
    run_hook post-compact.sh "sess-multi" "$DIR_A"
    [[ "$output" == *"hookSpecificOutput"* ]]

    # In dir B → silent
    run_hook post-compact.sh "sess-multi" "$DIR_B"
    [ -z "$output" ]

    rm -rf "$DIR_A" "$DIR_B"
}

@test "does NOT call cix CLI (cache-only)" {
    make_cache "sess-nc" "$TEST_PROJECT_DIR" "1"

    run_hook post-compact.sh "sess-nc" "$TEST_PROJECT_DIR"
    run_hook post-compact.sh "sess-nc" "$TEST_PROJECT_DIR"

    [ "$(mock_cix_call_count)" -eq 0 ]
}
