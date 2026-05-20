#!/usr/bin/env bats
# Tests for cix-wrapper.sh — the CLI bootstrap wrapper.

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

@test "wrapper finds and execs system cix" {
    # Mock cix on PATH; wrapper should exec it.
    export MOCK_CIX_EXIT=0

    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" --version

    [ "$status" -eq 0 ]
    # Mock was invoked.
    [ "$(mock_cix_call_count)" -ge 1 ]
}

@test "wrapper passes args through to cix" {
    export MOCK_CIX_EXIT=0
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "auth middleware" --in ./api

    [ "$status" -eq 0 ]
    # Inspect the log.
    grep -F 'cix search auth middleware --in ./api' "$TEST_LOG_FILE"
}

@test "wrapper propagates cix's exit code" {
    export MOCK_CIX_EXIT=42
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" status

    [ "$status" -eq 42 ]
}

@test "CIX_MAX_OUTPUT_LINES unset: full output passes through" {
    export MOCK_CIX_EXIT=0 MOCK_CIX_STDOUT_LINES=20
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "x"

    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 20 ]
}

@test "CIX_MAX_OUTPUT_LINES set below output: truncates and appends notice" {
    export MOCK_CIX_EXIT=0 MOCK_CIX_STDOUT_LINES=20
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        CIX_MAX_OUTPUT_LINES=5 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "x"

    [ "$status" -eq 0 ]
    # 5 content lines + 1 notice line.
    [ "${#lines[@]}" -eq 6 ]
    [[ "${lines[0]}" == "line 1" ]]
    [[ "${lines[4]}" == "line 5" ]]
    [[ "${lines[5]}" == *"truncated to 5 of 20 lines"* ]]
    [[ "${lines[5]}" == *"CIX_MAX_OUTPUT_LINES"* ]]
}

@test "CIX_MAX_OUTPUT_LINES set above output: no truncation, no notice" {
    export MOCK_CIX_EXIT=0 MOCK_CIX_STDOUT_LINES=8
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        CIX_MAX_OUTPUT_LINES=500 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "x"

    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 8 ]
    [[ "${lines[7]}" == "line 8" ]]
    # No truncation notice anywhere.
    ! [[ "$output" == *"truncated"* ]]
}

@test "CIX_MAX_OUTPUT_LINES invalid (non-numeric): treated as unset" {
    export MOCK_CIX_EXIT=0 MOCK_CIX_STDOUT_LINES=12
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        CIX_MAX_OUTPUT_LINES=abc \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "x"

    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 12 ]
    ! [[ "$output" == *"truncated"* ]]
}

@test "CIX_MAX_OUTPUT_LINES=0: treated as unset (no cap)" {
    export MOCK_CIX_EXIT=0 MOCK_CIX_STDOUT_LINES=7
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        CIX_MAX_OUTPUT_LINES=0 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" search "x"

    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 7 ]
    ! [[ "$output" == *"truncated"* ]]
}

@test "CIX_MAX_OUTPUT_LINES set: still propagates cix exit code" {
    export MOCK_CIX_EXIT=42 MOCK_CIX_STDOUT_LINES=20
    run env \
        PATH="$TEST_MOCK_BIN:/usr/bin:/bin" \
        CIX_MAX_OUTPUT_LINES=5 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" status

    [ "$status" -eq 42 ]
}

@test "self-recursion guard: removes own dir from PATH before lookup" {
    # Simulate plugin's bin/ on PATH before any system cix.
    # The wrapper itself sits in scripts/, but the plugin's bin/cix is a
    # symlink to it. We construct a fake bin dir with a symlink and put
    # it FIRST on PATH; the wrapper must detect and skip itself, falling
    # through to our mock.
    local fake_bin
    fake_bin="$(mktemp -d "${BATS_TMPDIR}/fake-plugin-bin-XXXXXX")"
    ln -s "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" "$fake_bin/cix"

    export MOCK_CIX_EXIT=0
    run env \
        PATH="$fake_bin:$TEST_MOCK_BIN:/usr/bin:/bin" \
        bash "$fake_bin/cix" --version

    # Despite invoking via the symlink, the wrapper should reach our mock.
    [ "$status" -eq 0 ]
    [ "$(mock_cix_call_count)" -ge 1 ]

    rm -rf "$fake_bin"
}
