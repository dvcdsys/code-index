# win/ — the cix Windows build pipeline

> **Status: experimental.** Built and tested on one Windows 11 machine only; the
> release workflows (`release-windows.yml`, the `windows-runtime` job) have never
> been run from CI. See [`doc/WINDOWS_APP.md`](../doc/WINDOWS_APP.md).

Everything needed to turn the repo into a per-user Windows installer for the cix
tray app. User-facing installation and usage documentation lives in
[`doc/WINDOWS_APP.md`](../doc/WINDOWS_APP.md); this file is about the build.

```
win/
  Resources/cix.ico          application icon (derived from the mac iconset)
  installer/cix.iss          Inno Setup script
  scripts/build-app.sh       build the tray launcher, stage it, compile the installer
  scripts/build-runtime.sh   package cix-server + cix + llama/ (CUDA) as a zip
  scripts/verify-runtime.ps1 prove a runtime zip works on a real Windows box
  scripts/make-ico.py        regenerate cix.ico from mac/Resources/cix.iconset
  dist/                      build output (gitignored)
```

## Two artefacts, two release streams

As on macOS, the app and the server are separate things released separately:

| Asset | Stream | Contents | Size |
|---|---|---|---|
| `cix-<ver>-x64-setup.exe` | `win/v*` | the tray app — `cix-launcher.exe` and its icon | ~5 MB |
| `cix-runtime-<ver>-windows-amd64.zip` | `server/v*` | `cix-server.exe`, `cix.exe`, `llama/` (CUDA) | ~500 MB |

The runtime **is** the server, so it carries the server's version and ships from the
server's tag, in the same workflow run that publishes the Docker images
(`release-server.yml`, job `windows-runtime`). The app releases from
`release-windows.yml`.

The app installs the runtime into `~\.cix\runtime\<server version>\` and points a
`current` directory junction at it; `cli/launcher/runtime_windows.go` documents the
design.

## How it differs from the macOS app

The launcher is one Go package, `cli/launcher`. Everything that does not touch the
OS — the panel's behaviour, first-run, the poller, the updater's release and
rollback logic, the runtime layout — is shared. What differs is behind a small set
of seams, one file per platform:

| Concern | macOS | Windows |
|---|---|---|
| Shell: icon + panel window | `panel_darwin.m` (AppKit, WKWebView) | `shell_windows.go` (Win32 tray, DWM popup, WebView2) |
| Server supervision | `launchd_darwin.go` (launchd agent) | `service_windows.go` (Task Scheduler task) |
| Runtime archive / `current` | `runtime_darwin.go` (tar.gz, symlink) | `runtime_windows.go` (zip, junction) |
| Self-update | `update_darwin.go` (DMG, swap script) | `update_windows.go` (silent installer) |
| Fallback dialogs | `dialog_darwin.go` (osascript) | `dialog_windows.go` (MessageBox) |
| Foreign install | `takeover_darwin.go` | `takeover_windows.go` |

The panel itself is the same `panel.html` on both; it picks its message bridge from
`window.cixPlatform`, which the Windows shell sets before the page runs.

No cgo on Windows: the launcher is pure Go (`x/sys/windows` and a pure-Go WebView2
wrapper), so it cross-compiles from any host.

## Build

```bash
SERVER_VERSION=0.0.0-dev win/scripts/build-runtime.sh   # the runtime zip
WIN_VERSION=0.1.0       win/scripts/build-app.sh        # app; installer if Inno Setup is found
```

`build-app.sh` runs anywhere; the installer step needs Windows and Inno Setup
(`winget install JRSoftware.InnoSetup`). On any other host it stages the files in
`win/dist/stage/` and says so.

| Variable | Used by | Meaning |
|---|---|---|
| `WIN_VERSION` | app | the app's version, from the `win/vX.Y.Z` tag |
| `SERVER_VERSION` | runtime | the runtime's version (default: nearest `server/v*` tag) |
| `OUT_DIR` | both | build directory (default `win/dist`) |

## Testing a runtime install without a release

`CIX_RUNTIME_TARBALL` points the app at a local archive instead of GitHub (the name
is shared with macOS; on Windows it takes the zip):

```powershell
$env:CIX_RUNTIME_TARBALL = "C:\path\to\cix-runtime-0.0.0-dev-windows-amd64.zip"
& "$env:LOCALAPPDATA\Programs\cix\cix-launcher.exe"
```

The install directory is named from the archive's filename, so it lands in
`~\.cix\runtime\0.0.0-dev\`. Update and rollback need a release to update *to*: point
`CIX_UPDATE_BASE_URL` at a local stand-in for the GitHub API (it overrides the base
URL for both streams). The updater filters out versions containing `-` or `+`, so
test versions have to be plain semver.

## Testing the UI and the update path from a machine with no screen

An SSH session lives in session 0, which has no desktop, so the GUI has to be
started in the user's interactive session — through a scheduled task registered
with `-LogonType Interactive`. Things that are not obvious until they bite:

- **Looking at it.** Neither `PrintWindow` (WebView2 composes through
  DirectComposition, so it returns black) nor a DPI-unaware `CopyFromScreen` shows
  the panel. A DPI-**aware** `Graphics.CopyFromScreen` from another interactive
  task does; crop to the bottom-right corner.
- **Focus is part of the behaviour.** The panel hides when it loses activation,
  and a dialog that loses its panel is answered "no". Starting a console program
  (a `.cmd`, `powershell.exe`) to poke the app steals the foreground and dismisses
  the very dialog being tested. Use a windowless sender (`pythonw`).
- **Driving the real handlers.** Start the launcher with `CIX_TEST_CONTROL=1`; then
  `cix-launcher.exe -send <action>` runs a panel action in it (`toggle-server`,
  `toggle-autostart`, `check-updates`, ...), and a JSON object answers a dialog:
  `-send "{\"action\":\"dialog-result\",\"ok\":true}"`. Without that variable in the
  *receiving* process the message is ignored.
- **A scheduled task refuses a second instance.** A stale `cmd.exe` that never
  exited keeps the task "Running", and every later start is silently dropped.

The update paths are tested against a local stand-in for the GitHub API
(`CIX_UPDATE_BASE_URL`): a tiny HTTP server that answers
`/repos/dvcdsys/code-index/releases` with a hand-written listing and serves the
assets beside it. Three scenarios cover what matters — a runtime that installs and
runs (swap, restart, the old version kept), a runtime that passes the install-time
`-v` check and then dies (automatic rollback, the failed version pruned), and a new
installer (silent upgrade, relaunch). The app update needs a launcher stamped with
a real version (`-X main.version=0.1.0`); an unstamped build never offers itself an
update.

`cli/launcher/e2e_windows_test.go` drives the scheduled task, the process table and
Ctrl+Break against a real installation; it is off unless `CIX_WIN_E2E=1`, and must
run in the interactive session because `AttachConsole` cannot cross sessions.

## Two Windows 11 behaviours worth knowing

**Redirection Guard.** Windows 11 treats a junction created by a non-administrator
as *untrusted*. A process that has opted in gets "the path cannot be traversed
because it contains an untrusted mount point" for any path through it. Setup
programs opt in, their children inherit it, and so does an OpenSSH session — which
means the launcher the installer relaunches after an update cannot see
`~\.cix\runtime\current`. The launcher therefore resolves the junction once and
uses the real versioned path (`runtimeLiveDir`); the scheduled task and the user's
`PATH` go through the junction, and neither is an opted-in process.

**Ctrl+Break kills everything on the console.** Stopping the server means
delivering Ctrl+Break to its console, and the event goes to *every* process
attached to it, the sender included. `SetConsoleCtrlHandler(NULL, TRUE)` ignores
Ctrl+C but not Ctrl+Break, and a `signal.Notify` handler did not save the launcher
either. So the launcher starts a throwaway copy of itself (`-break <pid>`) to take
the hit.

## Why a junction, why a scheduled task

Both are choices of the lowest-privilege tool that does the job. Symbolic links on
Windows need Developer Mode or elevation; junctions need neither. A Windows Service
needs elevation to install; a per-user scheduled task does not. An unsigned
installer asking for an administrator password is exactly what malware looks like,
and it is not a habit worth teaching — the same reasoning the macOS app applies to
overwriting itself.

## No code signing

As on macOS the app is not signed with a paid certificate, so SmartScreen warns on
first run and `checksums.txt` is the only integrity check. If a certificate is ever
bought, sign `cix-launcher.exe` before the installer is compiled and the installer
itself afterwards (Inno's `SignTool=` does both).
