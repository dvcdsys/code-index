#!/usr/bin/env bats
# Regression tests for the cix-workspace skill + cix-workspace-investigator
# sub-agent bundled inside the plugin.
#
# Pins:
#   * Both files exist at their expected plugin-relative paths so marketplace
#     install picks them up automatically.
#   * The skill has well-formed frontmatter with the right `name`.
#   * The skill is explicitly NOT auto-triggered (no `when_to_use:` block).
#     We surface auto-trigger heuristics deliberately for the single-repo
#     `cix` skill; the workspace skill is manual-only.
#   * The sub-agent frontmatter declares `name: cix-workspace-investigator`,
#     a non-empty description, and a read-only tool list (no Write/Edit).

PLUGIN_ROOT() {
    cd "$BATS_TEST_DIRNAME/.." && pwd
}

# --- skill ------------------------------------------------------------------

@test "skills/cix-workspace/SKILL.md exists" {
    local root
    root=$(PLUGIN_ROOT)
    [ -f "$root/skills/cix-workspace/SKILL.md" ]
}

@test "cix-workspace skill: frontmatter declares name=cix-workspace" {
    local root name
    root=$(PLUGIN_ROOT)
    name=$(awk '/^---$/{c++;next} c==1 && /^name:/{print $2; exit}' \
        "$root/skills/cix-workspace/SKILL.md")
    [ "$name" = "cix-workspace" ]
}

@test "cix-workspace skill: user-invocable=true" {
    local root inv
    root=$(PLUGIN_ROOT)
    inv=$(awk '/^---$/{c++;next} c==1 && /^user-invocable:/{print $2; exit}' \
        "$root/skills/cix-workspace/SKILL.md")
    [ "$inv" = "true" ]
}

@test "cix-workspace skill: manual-only (no when_to_use auto-trigger block)" {
    local root has_when_to_use
    root=$(PLUGIN_ROOT)
    has_when_to_use=$(awk '/^---$/{c++;next} c==1 && /^when_to_use:/{print "yes"; exit}' \
        "$root/skills/cix-workspace/SKILL.md")
    # Empty = absent = manual-only. "yes" would mean we accidentally re-added auto-trigger.
    [ -z "$has_when_to_use" ]
}

# --- sub-agent --------------------------------------------------------------

@test "agents/cix-workspace-investigator.md exists" {
    local root
    root=$(PLUGIN_ROOT)
    [ -f "$root/agents/cix-workspace-investigator.md" ]
}

@test "investigator sub-agent: frontmatter declares name=cix-workspace-investigator" {
    local root name
    root=$(PLUGIN_ROOT)
    name=$(awk '/^---$/{c++;next} c==1 && /^name:/{print $2; exit}' \
        "$root/agents/cix-workspace-investigator.md")
    [ "$name" = "cix-workspace-investigator" ]
}

@test "investigator sub-agent: declares a non-empty description" {
    local root desc
    root=$(PLUGIN_ROOT)
    # Description lives on the same line as the `description:` key (possibly
    # multi-line in YAML, but the prefix is enough to confirm presence).
    desc=$(awk '/^---$/{c++;next} c==1 && /^description:/{sub(/^description:[[:space:]]*/, ""); print; exit}' \
        "$root/agents/cix-workspace-investigator.md")
    [ -n "$desc" ]
}

@test "investigator sub-agent: tools list is read-only (no Write or Edit)" {
    local root tools
    root=$(PLUGIN_ROOT)
    tools=$(awk '/^---$/{c++;next} c==1 && /^tools:/{sub(/^tools:[[:space:]]*/, ""); print; exit}' \
        "$root/agents/cix-workspace-investigator.md")
    # Hard rule: investigator must never write or edit files.
    [[ "$tools" != *"Write"* ]]
    [[ "$tools" != *"Edit"* ]]
}

@test "investigator sub-agent: tools list includes Bash + Read + Grep" {
    local root tools
    root=$(PLUGIN_ROOT)
    tools=$(awk '/^---$/{c++;next} c==1 && /^tools:/{sub(/^tools:[[:space:]]*/, ""); print; exit}' \
        "$root/agents/cix-workspace-investigator.md")
    [[ "$tools" == *"Bash"* ]]
    [[ "$tools" == *"Read"* ]]
    [[ "$tools" == *"Grep"* ]]
}

# --- manifest ---------------------------------------------------------------

@test "plugin manifest declares version 0.2.0+ (workspace bundle shipped)" {
    local root version major minor
    root=$(PLUGIN_ROOT)
    version=$(jq -r '.version' "$root/.claude-plugin/plugin.json")
    major=$(echo "$version" | cut -d. -f1)
    minor=$(echo "$version" | cut -d. -f2)
    # Workspace bundle landed in v0.2.0. Anything older means the
    # manifest wasn't bumped.
    if [ "$major" -gt 0 ]; then
        true
    else
        [ "$minor" -ge 2 ]
    fi
}
