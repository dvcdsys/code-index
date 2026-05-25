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

@test "CIX_NO_AUTOINSTALL: refuses to auto-install and fails cleanly" {
    # Reach the bootstrap block: no cix on a minimal PATH + an empty
    # CIX_PLUGIN_BIN_DIR. Skip if a system cix sits in a dir we can't drop.
    if command -v cix >/dev/null 2>&1; then
        local sys_cix; sys_cix=$(command -v cix)
        case "$sys_cix" in
            /usr/bin/*|/bin/*|/sbin/*|/usr/sbin/*)
                skip "system cix in $sys_cix — can't remove from minimal PATH" ;;
        esac
    fi
    local empty_bin; empty_bin="$(mktemp -d "${BATS_TMPDIR}/empty-bin-XXXXXX")"

    run env -i \
        PATH="/usr/bin:/bin" \
        HOME="$HOME" \
        CIX_PLUGIN_BIN_DIR="$empty_bin" \
        CIX_NO_AUTOINSTALL=1 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" status

    [ "$status" -eq 1 ]
    [[ "$output" == *"CIX_NO_AUTOINSTALL"* ]]
    [[ "$output" == *"refusing to auto-install"* ]]
    # Pinned version surfaced in the manual-install hint.
    [[ "$output" == *"cli/v"* ]]
    # Must NOT have attempted a network install.
    ! [[ "$output" == *"installing"* ]]

    rm -rf "$empty_bin"
}

@test "CIX_NO_AUTOINSTALL=0: treated as opt-in (gate does not short-circuit)" {
    # With the opt-out disabled (=0), the bootstrap block must proceed PAST
    # the CIX_NO_AUTOINSTALL gate. We provide a fake `curl` that fails
    # immediately, so the install attempt fails locally (no network) — which
    # proves the gate did not short-circuit before reaching the install.
    if command -v cix >/dev/null 2>&1; then
        local sys_cix; sys_cix=$(command -v cix)
        case "$sys_cix" in
            /usr/bin/*|/bin/*|/sbin/*|/usr/sbin/*)
                skip "system cix in $sys_cix — can't remove from minimal PATH" ;;
        esac
    fi
    local empty_bin fakebin
    empty_bin="$(mktemp -d "${BATS_TMPDIR}/empty-bin2-XXXXXX")"
    fakebin="$(mktemp -d "${BATS_TMPDIR}/fakebin-XXXXXX")"
    printf '#!/bin/sh\nexit 1\n' > "$fakebin/curl"
    chmod +x "$fakebin/curl"

    run env -i \
        PATH="$fakebin:/usr/bin:/bin" \
        HOME="$HOME" \
        CIX_PLUGIN_BIN_DIR="$empty_bin" \
        CIX_NO_AUTOINSTALL=0 \
        bash "$TEST_PLUGIN_ROOT/scripts/cix-wrapper.sh" status

    [ "$status" -eq 1 ]
    # The opt-out message must NOT appear — =0 means "do not opt out".
    ! [[ "$output" == *"refusing to auto-install"* ]]
    # It reached the install path and failed at our fake curl.
    [[ "$output" == *"install failed"* ]]

    rm -rf "$empty_bin" "$fakebin"
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
