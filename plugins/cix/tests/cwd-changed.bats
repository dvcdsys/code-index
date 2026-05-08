#!/usr/bin/env bats
# Tests for cwd-changed.sh

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

@test "first cd into new dir: runs cix status, writes cache" {
    export MOCK_CIX_EXIT=0
    local NEW_DIR="${BATS_TMPDIR}/new-proj-$$"
    mkdir -p "$NEW_DIR"

    run_hook cwd-changed.sh "sess-1" "$NEW_DIR"

    [ "$status" -eq 0 ]
    [ "$(read_cache 'sess-1' "$NEW_DIR")" = "1" ]
    # cix WAS called
    [ "$(mock_cix_call_count)" -ge 1 ]

    rm -rf "$NEW_DIR"
}

@test "cd back to already-evaluated dir: no-op (cache unchanged, cix not called)" {
    # Pre-populate cache
    make_cache "sess-2" "$TEST_PROJECT_DIR" "1"
    local before
    before=$(stat -f %m "$TEST_CACHE_DIR/cix-aware-sess-2-$(compute_hash "$TEST_PROJECT_DIR")" 2>/dev/null \
             || stat -c %Y "$TEST_CACHE_DIR/cix-aware-sess-2-$(compute_hash "$TEST_PROJECT_DIR")")

    sleep 1
    run_hook cwd-changed.sh "sess-2" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [ "$(mock_cix_call_count)" -eq 0 ]   # cix NOT called — short-circuited
    [ "$(read_cache 'sess-2' "$TEST_PROJECT_DIR")" = "1" ]   # cache unchanged
}

@test "different dirs in same session get separate cache entries" {
    export MOCK_CIX_EXIT=0
    local DIR_A="${BATS_TMPDIR}/proj-A-$$"
    local DIR_B="${BATS_TMPDIR}/proj-B-$$"
    mkdir -p "$DIR_A" "$DIR_B"

    run_hook cwd-changed.sh "sess-multi" "$DIR_A"
    [ "$(read_cache 'sess-multi' "$DIR_A")" = "1" ]

    export MOCK_CIX_EXIT=1   # second dir is "not indexed"
    run_hook cwd-changed.sh "sess-multi" "$DIR_B"
    [ "$(read_cache 'sess-multi' "$DIR_B")" = "0" ]

    # Both files exist with different hashes
    [ -f "$TEST_CACHE_DIR/cix-aware-sess-multi-$(compute_hash "$DIR_A")" ]
    [ -f "$TEST_CACHE_DIR/cix-aware-sess-multi-$(compute_hash "$DIR_B")" ]

    rm -rf "$DIR_A" "$DIR_B"
}

@test "silent on cd (no reminder injection)" {
    export MOCK_CIX_EXIT=0
    local NEW_DIR="${BATS_TMPDIR}/silent-proj-$$"
    mkdir -p "$NEW_DIR"

    run_hook cwd-changed.sh "sess-silent" "$NEW_DIR"

    [ "$status" -eq 0 ]
    # No JSON output — CwdChanged is silent by design.
    [ -z "$output" ]

    rm -rf "$NEW_DIR"
}

@test "missing session_id: silent, no cache written" {
    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/cwd-changed.sh" <<<"{}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [ -z "$(ls "$TEST_CACHE_DIR"/cix-aware-* 2>/dev/null)" ]
}

@test "cix status timeout: writes cache=0 within ~2s" {
    export MOCK_CIX_DELAY=10

    local start_ms end_ms
    start_ms=$(perl -MTime::HiRes=time -E 'say int(time() * 1000)')
    run_hook cwd-changed.sh "sess-to" "$TEST_PROJECT_DIR"
    end_ms=$(perl -MTime::HiRes=time -E 'say int(time() * 1000)')

    [ "$status" -eq 0 ]
    [ "$((end_ms - start_ms))" -lt 5000 ]
    [ "$(read_cache 'sess-to' "$TEST_PROJECT_DIR")" = "0" ]
}
