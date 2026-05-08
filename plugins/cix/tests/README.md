# Plugin tests

Hook script tests for the cix Claude Code plugin. Uses
[bats-core](https://bats-core.readthedocs.io/) with mocked `cix` binary,
isolated `$CLAUDE_PLUGIN_DATA`, and a per-test scratch project directory.

## Run locally

```bash
# Install bats + jq + shellcheck
brew install bats-core jq shellcheck      # macOS
sudo apt-get install bats jq shellcheck    # Debian / Ubuntu

# From repo root:
bats plugins/cix/tests/*.bats

# Or pick one suite:
bats plugins/cix/tests/session-end.bats

# TAP-formatted output (what CI uses):
bats --tap plugins/cix/tests/*.bats
```

Each test runs in an isolated `$BATS_TMPDIR` scratch directory and
cleans up after itself — no state leaks between tests.

## What's covered

| Suite | Focus |
|---|---|
| `session-start.bats` | cix-status flow, cache write, 30-day GC, **non-matching files preserved** |
| `cwd-changed.bats` | First-cd evaluation, no-op on cached dir, multi-dir state |
| `grep-nudge.bats` | Exponential backoff (1, 2, 4, 8, 16), per-(session, dir) counters |
| `post-compact.bats` | Re-injection only when cache="1" |
| `session-end.bats` | **Security:** deletion never leaks to other sessions, non-cix files, or subdirs — even with custom `$CLAUDE_PLUGIN_DATA` |
| `cix-wrapper.bats` | System-cix passthrough, exit code propagation, self-recursion guard |

## Security tests (the most important ones)

Bash scripts that call `find -delete` get extra scrutiny. Safety comes
from **what** we delete (strict `-name` patterns + `-type f` +
`-maxdepth 1`), not **where** the cache dir lives. The plugin
deliberately does not whitelist parent paths, so users with custom
`$CLAUDE_PLUGIN_DATA` (corporate setups, XDG-style layouts) are
supported.

`session-end.bats` and `session-start.bats` suites contain explicit
adversarial cases:

- Other sessions' cache files → must NOT be touched
- Files with confusable names (`cix-other-pattern`,
  `X-cix-aware-fake-...`, `cix` alone) → must NOT be touched
- Random files (`config.yaml`, `.env`, `secrets.json`) in cache dir
  → must NOT be touched
- Subdirectories in cache dir + nested files → must NOT be touched
  (only `-maxdepth 1`)
- 30-day GC → must spare files outside the `cix-aware-*` /
  `cix-grep-count-*` prefixes, even if they're old
- `session_id` containing shell metacharacters → must NOT trigger
  command injection (canary file survives)
- Custom non-standard `$CLAUDE_PLUGIN_DATA` → script proceeds without
  refusing, deletes only matching files

If any of these fail in CI, the offending change cannot land.

## Mocks

`tests/mocks/bin/cix` is a fake `cix` CLI controlled via env vars:

- `MOCK_CIX_EXIT` — exit code (default 0)
- `MOCK_CIX_DELAY` — sleep before exit (for timeout tests)
- `MOCK_CIX_LOG_FILE` — append every invocation here so tests can
  assert "was the script called with the right args?"

`helpers.bash` puts the mock first on `$PATH` for every hook invocation,
so unqualified `cix` calls inside the hook scripts hit the mock.

## Adding a new test

1. Pick (or create) the right `.bats` file.
2. Use `setup() { setup_test_env; }` and `teardown() { teardown_test_env; }`.
3. Use `run_hook <script> <session_id> <project_dir>` — it returns bats's
   `$status` and `$output` for assertions.
4. Use `make_cache <sess> <dir> <verdict>`, `read_cache`, `read_counter`,
   `compute_hash`, `mock_cix_call_count` — see `helpers.bash`.

## Running shellcheck locally

```bash
shellcheck --severity=warning plugins/cix/scripts/*.sh
```

CI gates on shellcheck warnings, not just errors — keep the scripts
clean of unquoted variables, unsafe globs, and word-splitting risks.
