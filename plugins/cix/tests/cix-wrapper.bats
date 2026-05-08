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
