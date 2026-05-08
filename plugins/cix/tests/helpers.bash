# Shared helpers for cix plugin bats tests.
#
# Each .bats file should source this in setup():
#   load 'helpers'
#
# Provides:
#   setup_test_env  — creates an isolated CACHE_DIR + mock cix on PATH
#   teardown_test_env — cleans up
#   run_hook        — invokes a hook script with mock stdin + env
#   make_cache      — writes a cache file ("0" or "1") for (session, dir)
#   read_cache      — reads back a cache file
#   compute_hash    — same DIR_HASH algorithm as the scripts use
#   call_count_log  — file path where the mock cix logs each invocation

# ── Globals set per-test ──────────────────────────────────────────────────────
# These are populated by setup_test_env(), used by other helpers.
TEST_PLUGIN_ROOT=""
TEST_CACHE_DIR=""
TEST_MOCK_BIN=""
TEST_LOG_FILE=""
TEST_PROJECT_DIR=""

setup_test_env() {
    # Plugin root = repo's plugins/cix directory, so scripts find their
    # siblings via $CLAUDE_PLUGIN_ROOT.
    TEST_PLUGIN_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"

    # Per-test scratch dir for cache files. Living under BATS_TMPDIR keeps
    # everything isolated from real plugin data.
    TEST_CACHE_DIR="$(mktemp -d "${BATS_TMPDIR}/cix-test-XXXXXX")"

    # Per-test scratch project dir.
    TEST_PROJECT_DIR="$(mktemp -d "${BATS_TMPDIR}/cix-proj-XXXXXX")"

    # Per-test cix invocation log.
    TEST_LOG_FILE="$(mktemp "${BATS_TMPDIR}/cix-log-XXXXXX")"

    # Mock cix binary on PATH.
    TEST_MOCK_BIN="$BATS_TEST_DIRNAME/mocks/bin"
    chmod +x "$TEST_MOCK_BIN/cix" 2>/dev/null || true

    # Default mock behavior: exit 0 ("project IS indexed").
    export MOCK_CIX_EXIT=0
    export MOCK_CIX_DELAY=0
    export MOCK_CIX_LOG_FILE="$TEST_LOG_FILE"
}

teardown_test_env() {
    [ -n "$TEST_CACHE_DIR" ] && [ -d "$TEST_CACHE_DIR" ] && rm -rf "$TEST_CACHE_DIR"
    [ -n "$TEST_PROJECT_DIR" ] && [ -d "$TEST_PROJECT_DIR" ] && rm -rf "$TEST_PROJECT_DIR"
    [ -n "$TEST_LOG_FILE" ] && [ -f "$TEST_LOG_FILE" ] && rm -f "$TEST_LOG_FILE"
    unset MOCK_CIX_EXIT MOCK_CIX_DELAY MOCK_CIX_LOG_FILE
    unset TEST_PLUGIN_ROOT TEST_CACHE_DIR TEST_MOCK_BIN TEST_LOG_FILE TEST_PROJECT_DIR
}

# Run a hook script with controlled env. Usage:
#   run_hook session-start.sh "$session_id" "$project_dir"
# Reads stdout/stderr into bats's $output and $status as usual.
run_hook() {
    local script="$1"
    local session_id="${2:-test-session}"
    local project_dir="${3:-$TEST_PROJECT_DIR}"

    # PATH includes our mock so any unqualified `cix` call hits the mock.
    # We deliberately do NOT pre-empt CLAUDE_PLUGIN_ROOT/bin/cix here —
    # the wrapper has its own resolution logic that prefers it; for hook
    # tests we want the mock to win.
    run env \
        PATH="$TEST_MOCK_BIN:$PATH" \
        CLAUDE_PLUGIN_DATA="$TEST_CACHE_DIR" \
        CLAUDE_PROJECT_DIR="$project_dir" \
        bash "$TEST_PLUGIN_ROOT/scripts/$script" <<<"{\"session_id\":\"$session_id\"}"
}

# Compute the same DIR_HASH the scripts compute.
compute_hash() {
    printf '%s' "$1" | shasum -a 256 2>/dev/null | cut -c1-8
}

# Pre-populate a cache verdict for a (session, dir) pair.
# Usage: make_cache "$SID" "$DIR" "1"
make_cache() {
    local session_id="$1" project_dir="$2" verdict="$3"
    local hash
    hash=$(compute_hash "$project_dir")
    printf '%s' "$verdict" > "$TEST_CACHE_DIR/cix-aware-$session_id-$hash"
}

# Read back the cache verdict for a (session, dir) pair. Empty if absent.
read_cache() {
    local session_id="$1" project_dir="$2"
    local hash
    hash=$(compute_hash "$project_dir")
    cat "$TEST_CACHE_DIR/cix-aware-$session_id-$hash" 2>/dev/null || echo ""
}

# Read the current backoff counter for (session, dir).
read_counter() {
    local session_id="$1" project_dir="$2"
    local hash
    hash=$(compute_hash "$project_dir")
    cat "$TEST_CACHE_DIR/cix-grep-count-$session_id-$hash" 2>/dev/null || echo "0"
}

# How many times was mock cix invoked in this test?
mock_cix_call_count() {
    [ -f "$TEST_LOG_FILE" ] || { echo "0"; return; }
    wc -l < "$TEST_LOG_FILE" | tr -d ' '
}
