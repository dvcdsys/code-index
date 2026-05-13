#!/usr/bin/env bats
# Tests for session-start.sh

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

# ─── Functional ───────────────────────────────────────────────────────────────

@test "indexed project: writes cache=1 and emits reminder" {
    export MOCK_CIX_EXIT=0
    run_hook session-start.sh "sess-A" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]
    [[ "$output" == *"SessionStart"* ]]
    [[ "$output" == *"semantic code index"* ]]
    [ "$(read_cache 'sess-A' "$TEST_PROJECT_DIR")" = "1" ]
}

@test "non-indexed project: writes cache=0 and stays silent" {
    export MOCK_CIX_EXIT=1
    run_hook session-start.sh "sess-B" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [ "$(read_cache 'sess-B' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "missing session_id: silent, no cache written" {
    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-start.sh" <<<"{}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
    # No cache files for any session.
    [ -z "$(ls "$TEST_CACHE_DIR"/cix-aware-* 2>/dev/null)" ]
}

@test "cix CLI not on PATH: writes cache=0, silent" {
    # Use /tmp as cache dir so the guard accepts it without TMPDIR set.
    # Use a PATH that excludes both our mock and any system-installed cix.
    local tmp_cache="/tmp/cix-test-noCix-$$"
    mkdir -p "$tmp_cache"

    # Skip if system has cix in /usr/bin or /bin (we can't avoid it then).
    if command -v cix >/dev/null 2>&1; then
        local sys_cix
        sys_cix=$(command -v cix)
        if [[ "$sys_cix" == /usr/bin/* || "$sys_cix" == /bin/* || "$sys_cix" == /sbin/* || "$sys_cix" == /usr/sbin/* ]]; then
            rm -rf "$tmp_cache"
            skip "system cix is in $sys_cix — can't reliably remove from PATH for this test"
        fi
    fi

    run env -i \
        PATH="/usr/bin:/bin" \
        HOME="$HOME" \
        CLAUDE_PLUGIN_DATA="$tmp_cache" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-start.sh" <<<"{\"session_id\":\"sess-noCix\"}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
    # Cache file must contain "0" — script gracefully degraded.
    local hash
    hash=$(printf '%s' "$TEST_PROJECT_DIR" | shasum -a 256 | cut -c1-8)
    [ "$(cat "$tmp_cache/cix-aware-sess-noCix-$hash" 2>/dev/null)" = "0" ]

    rm -rf "$tmp_cache"
}

@test "cix status timeout: kills child after 2s, writes cache=0" {
    export MOCK_CIX_EXIT=0
    export MOCK_CIX_DELAY=10   # mock would hang for 10s; script must kill at 2s

    local start_ms end_ms
    start_ms=$(perl -MTime::HiRes=time -E 'say int(time() * 1000)')
    run_hook session-start.sh "sess-timeout" "$TEST_PROJECT_DIR"
    end_ms=$(perl -MTime::HiRes=time -E 'say int(time() * 1000)')

    local elapsed=$((end_ms - start_ms))
    [ "$status" -eq 0 ]
    # Should be ≥ ~2000ms (timeout fires) and well under 10000ms (no hang).
    [ "$elapsed" -lt 5000 ]
    [ "$(read_cache 'sess-timeout' "$TEST_PROJECT_DIR")" = "0" ]
}

@test "cache file uses session_id-DIR_HASH key" {
    export MOCK_CIX_EXIT=0
    local hash
    hash=$(compute_hash "$TEST_PROJECT_DIR")

    run_hook session-start.sh "sess-key" "$TEST_PROJECT_DIR"

    [ -f "$TEST_CACHE_DIR/cix-aware-sess-key-$hash" ]
}

# ─── Security: 30-day GC ──────────────────────────────────────────────────────

@test "GC: deletes cix-aware-* and cix-grep-count-* older than 30 days" {
    # Create old cix files — must use cross-platform date manipulation.
    # macOS `touch -t YYYYMMDDhhmm` works; Linux too.
    if [[ "$(uname -s)" == "Darwin" ]]; then
        local old_ts
        old_ts=$(date -v-32d +%Y%m%d%H%M)
    else
        local old_ts
        old_ts=$(date -d "32 days ago" +%Y%m%d%H%M)
    fi

    touch -t "$old_ts" "$TEST_CACHE_DIR/cix-aware-old-aaaa1111"
    touch -t "$old_ts" "$TEST_CACHE_DIR/cix-grep-count-old-bbbb2222"
    touch "$TEST_CACHE_DIR/cix-aware-new-cccc3333"  # recent, must be kept

    export MOCK_CIX_EXIT=0
    run_hook session-start.sh "gc-test" "$TEST_PROJECT_DIR"

    [ ! -f "$TEST_CACHE_DIR/cix-aware-old-aaaa1111" ]
    [ ! -f "$TEST_CACHE_DIR/cix-grep-count-old-bbbb2222" ]
    [ -f "$TEST_CACHE_DIR/cix-aware-new-cccc3333" ]
}

@test "GC: never touches non-cix files even if they're old" {
    if [[ "$(uname -s)" == "Darwin" ]]; then
        local old_ts; old_ts=$(date -v-60d +%Y%m%d%H%M)
    else
        local old_ts; old_ts=$(date -d "60 days ago" +%Y%m%d%H%M)
    fi

    touch -t "$old_ts" "$TEST_CACHE_DIR/important-user-data.json"
    touch -t "$old_ts" "$TEST_CACHE_DIR/random-old-file"
    mkdir "$TEST_CACHE_DIR/some-subdir"
    touch -t "$old_ts" "$TEST_CACHE_DIR/some-subdir/inner.txt"

    export MOCK_CIX_EXIT=0
    run_hook session-start.sh "gc-test" "$TEST_PROJECT_DIR"

    [ -f "$TEST_CACHE_DIR/important-user-data.json" ]
    [ -f "$TEST_CACHE_DIR/random-old-file" ]
    [ -d "$TEST_CACHE_DIR/some-subdir" ]
    [ -f "$TEST_CACHE_DIR/some-subdir/inner.txt" ]
}

# ─── Custom cache dirs (no whitelist — accept any reachable directory) ────────
# We do not whitelist parent paths. Safety comes from -type f + exact
# -name pattern, not from where the cache dir lives.

@test "accepts custom CLAUDE_PLUGIN_DATA in non-standard location" {
    # Simulate a corp / XDG-style cache dir under /opt or /var.
    local custom_dir
    custom_dir="$(mktemp -d "${BATS_TMPDIR}/custom-cache-XXXXXX")"
    export MOCK_CIX_EXIT=0

    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$custom_dir" \
        CLAUDE_PROJECT_DIR="$TEST_PROJECT_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-start.sh" <<<"{\"session_id\":\"custom\"}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"hookSpecificOutput"* ]]

    rm -rf "$custom_dir"
}

@test "GC never deletes files outside the cix-aware-* / cix-grep-count-* prefixes — even in same dir" {
    # Even if cache dir contains thousands of unrelated files, none of
    # them should be touched. Strictness enforced by `-name` pattern in
    # the find call.
    local many_unrelated_files=(
        "config.yaml"
        "credentials.json"
        ".bashrc"
        "important_log_file"
        "some-other-plugin-data"
        "AWARE-MISTAKEN-CASE"           # different case
        "cix-other-pattern"             # different prefix
        "cix"                           # too short to match cix-aware-* / cix-grep-count-*
        "X-cix-aware-fake-aaaa"         # prefixed with junk before cix-
    )

    if [[ "$(uname -s)" == "Darwin" ]]; then
        local old_ts; old_ts=$(date -v-90d +%Y%m%d%H%M)
    else
        local old_ts; old_ts=$(date -d "90 days ago" +%Y%m%d%H%M)
    fi

    for f in "${many_unrelated_files[@]}"; do
        touch -t "$old_ts" "$TEST_CACHE_DIR/$f"
    done

    export MOCK_CIX_EXIT=0
    run_hook session-start.sh "non-destructive-test" "$TEST_PROJECT_DIR"

    # Every unrelated file should still be there, regardless of age.
    for f in "${many_unrelated_files[@]}"; do
        [ -f "$TEST_CACHE_DIR/$f" ] || {
            echo "FAILED: $f was unexpectedly deleted" >&2
            return 1
        }
    done
}
