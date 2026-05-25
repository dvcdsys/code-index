#!/usr/bin/env bats
# Regression tests for hooks/hooks.json.
#
# Pins the matcher list so a future edit that drops Bash (or Grep) from the
# PostToolUse hooks fails CI immediately — that's the exact bug that produced
# this test file: the original matcher was "Grep|Glob" only, so the nudge
# never fired for `grep`/`rg` invoked through the Bash tool.

load 'helpers'

HOOKS_JSON_PATH() {
    cd "$BATS_TEST_DIRNAME/.." && pwd
}

@test "hooks.json: valid JSON" {
    local root
    root=$(HOOKS_JSON_PATH)
    run jq -e . "$root/hooks/hooks.json"
    [ "$status" -eq 0 ]
}

@test "hooks.json: PostToolUse matcher list covers Bash" {
    local root has_bash
    root=$(HOOKS_JSON_PATH)
    has_bash=$(jq -r '
        [.hooks.PostToolUse[].matcher]
        | join("|")
        | split("|")
        | any(. == "Bash")
    ' "$root/hooks/hooks.json")
    [ "$has_bash" = "true" ]
}

@test "hooks.json: PostToolUse matcher list still covers Grep" {
    local root has_grep
    root=$(HOOKS_JSON_PATH)
    has_grep=$(jq -r '
        [.hooks.PostToolUse[].matcher]
        | join("|")
        | split("|")
        | any(. == "Grep")
    ' "$root/hooks/hooks.json")
    [ "$has_grep" = "true" ]
}

@test "hooks.json: PostToolUse matcher list still covers Glob" {
    local root has_glob
    root=$(HOOKS_JSON_PATH)
    has_glob=$(jq -r '
        [.hooks.PostToolUse[].matcher]
        | join("|")
        | split("|")
        | any(. == "Glob")
    ' "$root/hooks/hooks.json")
    [ "$has_glob" = "true" ]
}

@test "hooks.json: every PostToolUse entry points at grep-nudge.sh" {
    local root all_match
    root=$(HOOKS_JSON_PATH)
    all_match=$(jq -r '
        [.hooks.PostToolUse[].hooks[].command]
        | all(endswith("/scripts/grep-nudge.sh"))
    ' "$root/hooks/hooks.json")
    [ "$all_match" = "true" ]
}
