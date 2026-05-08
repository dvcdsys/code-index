#!/usr/bin/env bats
# Tests for session-end.sh — focus on security: no cross-session damage,
# no damage to non-cix files.

load 'helpers'

setup() { setup_test_env; }
teardown() { teardown_test_env; }

# ─── Functional ───────────────────────────────────────────────────────────────

@test "removes single-dir session's cache files" {
    make_cache "sess-1" "$TEST_PROJECT_DIR" "1"
    local hash; hash=$(compute_hash "$TEST_PROJECT_DIR")
    touch "$TEST_CACHE_DIR/cix-grep-count-sess-1-$hash"

    run_hook session-end.sh "sess-1" "$TEST_PROJECT_DIR"

    [ "$status" -eq 0 ]
    [ ! -f "$TEST_CACHE_DIR/cix-aware-sess-1-$hash" ]
    [ ! -f "$TEST_CACHE_DIR/cix-grep-count-sess-1-$hash" ]
}

@test "removes multi-dir session's files (glob across dir hashes)" {
    local DIR_A="${BATS_TMPDIR}/proj-A-$$"
    local DIR_B="${BATS_TMPDIR}/proj-B-$$"
    local DIR_C="${BATS_TMPDIR}/proj-C-$$"
    mkdir -p "$DIR_A" "$DIR_B" "$DIR_C"

    make_cache "sess-multi" "$DIR_A" "1"
    make_cache "sess-multi" "$DIR_B" "0"
    make_cache "sess-multi" "$DIR_C" "1"

    touch "$TEST_CACHE_DIR/cix-grep-count-sess-multi-$(compute_hash "$DIR_A")"
    touch "$TEST_CACHE_DIR/cix-grep-count-sess-multi-$(compute_hash "$DIR_C")"

    run_hook session-end.sh "sess-multi" "$DIR_A"

    # All session files gone (across all 3 dirs)
    [ -z "$(ls "$TEST_CACHE_DIR"/cix-aware-sess-multi-* 2>/dev/null)" ]
    [ -z "$(ls "$TEST_CACHE_DIR"/cix-grep-count-sess-multi-* 2>/dev/null)" ]

    rm -rf "$DIR_A" "$DIR_B" "$DIR_C"
}

@test "missing session_id: no-op (silent exit 0)" {
    make_cache "any-sess" "$TEST_PROJECT_DIR" "1"

    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-end.sh" <<<"{}"

    [ "$status" -eq 0 ]
    [ -z "$output" ]
    # Cache must be UNTOUCHED (we didn't know whose to delete).
    [ "$(read_cache 'any-sess' "$TEST_PROJECT_DIR")" = "1" ]
}

# ─── Security: scope of deletion ──────────────────────────────────────────────

@test "SECURITY: never deletes other sessions' files" {
    local hash; hash=$(compute_hash "$TEST_PROJECT_DIR")

    # OUR session
    touch "$TEST_CACHE_DIR/cix-aware-our-sess-$hash"
    touch "$TEST_CACHE_DIR/cix-grep-count-our-sess-$hash"

    # OTHER sessions — must NOT be touched
    touch "$TEST_CACHE_DIR/cix-aware-other-1-aaaa1111"
    touch "$TEST_CACHE_DIR/cix-aware-other-2-bbbb2222"
    touch "$TEST_CACHE_DIR/cix-grep-count-other-1-aaaa1111"

    run_hook session-end.sh "our-sess" "$TEST_PROJECT_DIR"

    # Ours gone
    [ ! -f "$TEST_CACHE_DIR/cix-aware-our-sess-$hash" ]
    [ ! -f "$TEST_CACHE_DIR/cix-grep-count-our-sess-$hash" ]

    # Others preserved
    [ -f "$TEST_CACHE_DIR/cix-aware-other-1-aaaa1111" ]
    [ -f "$TEST_CACHE_DIR/cix-aware-other-2-bbbb2222" ]
    [ -f "$TEST_CACHE_DIR/cix-grep-count-other-1-aaaa1111" ]
}

@test "SECURITY: never deletes non-cix files even if name overlaps" {
    local hash; hash=$(compute_hash "$TEST_PROJECT_DIR")

    touch "$TEST_CACHE_DIR/cix-aware-sess-1-$hash"

    # Files that look LIKE plugin files but aren't (different prefix)
    touch "$TEST_CACHE_DIR/important-data.json"
    touch "$TEST_CACHE_DIR/random-file"
    touch "$TEST_CACHE_DIR/cix-something-else-$hash"   # different shape
    touch "$TEST_CACHE_DIR/aware-sess-1-$hash"          # missing cix- prefix
    mkdir "$TEST_CACHE_DIR/some-subdir"
    touch "$TEST_CACHE_DIR/some-subdir/inner.txt"

    run_hook session-end.sh "sess-1" "$TEST_PROJECT_DIR"

    [ ! -f "$TEST_CACHE_DIR/cix-aware-sess-1-$hash" ]    # ours gone

    # All non-matching preserved
    [ -f "$TEST_CACHE_DIR/important-data.json" ]
    [ -f "$TEST_CACHE_DIR/random-file" ]
    [ -f "$TEST_CACHE_DIR/cix-something-else-$hash" ]
    [ -f "$TEST_CACHE_DIR/aware-sess-1-$hash" ]
    [ -d "$TEST_CACHE_DIR/some-subdir" ]
    [ -f "$TEST_CACHE_DIR/some-subdir/inner.txt" ]
}

# ─── Security: works on custom cache dirs without harming other files ────────

@test "SECURITY: in a non-standard cache dir, only matching files are deleted" {
    # User has a custom CLAUDE_PLUGIN_DATA. The dir contains all sorts
    # of unrelated files. session-end must never touch any of them.
    local custom_dir
    custom_dir="$(mktemp -d "${BATS_TMPDIR}/corp-cache-XXXXXX")"

    # OUR session's files — should be deleted
    local hash; hash=$(compute_hash "$TEST_PROJECT_DIR")
    touch "$custom_dir/cix-aware-our-sess-$hash"
    touch "$custom_dir/cix-grep-count-our-sess-$hash"

    # Non-matching files — must survive
    local non_matching=(
        "kubeconfig.yaml"
        ".env"
        "secrets.json"
        "deploy.sh"
        "important_user_data.bin"
        "cix"                            # too short
        "cix-other-prefix"               # different shape
        "X-cix-aware-fake-aaaa"          # has cix-aware mid-name, but not at start
    )
    for f in "${non_matching[@]}"; do
        touch "$custom_dir/$f"
    done

    # Subdir + nested file — must not be touched (no recursion)
    mkdir "$custom_dir/secret-subdir"
    touch "$custom_dir/secret-subdir/important.txt"

    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$custom_dir" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-end.sh" <<<"{\"session_id\":\"our-sess\"}"

    [ "$status" -eq 0 ]
    # Ours gone
    [ ! -f "$custom_dir/cix-aware-our-sess-$hash" ]
    [ ! -f "$custom_dir/cix-grep-count-our-sess-$hash" ]
    # Non-matching survived
    for f in "${non_matching[@]}"; do
        [ -f "$custom_dir/$f" ] || {
            echo "FAILED: $f was unexpectedly deleted" >&2
            return 1
        }
    done
    # Subdir untouched
    [ -d "$custom_dir/secret-subdir" ]
    [ -f "$custom_dir/secret-subdir/important.txt" ]

    rm -rf "$custom_dir"
}

# ─── Security: shell-injection resistance ─────────────────────────────────────

@test "SECURITY: session_id with shell metacharacters does not inject commands" {
    # Create a canary file that an injection might delete.
    local CANARY="/tmp/cix-bats-canary-$$"
    touch "$CANARY"
    [ -f "$CANARY" ]

    # Inject attempt: session_id contains `; rm -rf $CANARY`.
    local EVIL='abc"; rm -f /tmp/cix-bats-canary-'"$$"'; echo "'

    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        bash "$TEST_PLUGIN_ROOT/scripts/session-end.sh" <<<"{\"session_id\":\"$EVIL\"}"

    # Canary MUST survive — bash variable quoting blocks the injection.
    [ -f "$CANARY" ]
    rm -f "$CANARY"
}
