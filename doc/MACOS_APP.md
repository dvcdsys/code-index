# cix for macOS

`cix.app` packages the cix server, the `cix` CLI and a Metal-accelerated
`llama-server` into a single drag-to-install application for Apple Silicon.

The app sits in the menu bar and shows whether the server is running and what
its embedding provider is doing, with start/stop and a link to the dashboard.
There is no Dock icon and no window.

> **Not in this release yet:** the autostart toggle, password reset from the
> menu, and self-update. Autostart can be set up with `install-server.sh`, and
> password reset is a command, described below.

## Requirements

- macOS 13 (Ventura) or later
- Apple Silicon (M1 or newer)

There is no Intel build. Upstream llama.cpp publishes a single macOS release
asset, `macos-arm64`; pairing an x86_64 server binary with an arm64
`llama-server` produces a bundle that installs cleanly and then fails at the
first embedding, so `mac/scripts/build-app.sh` refuses to build one.

## Install

1. Download `cix-<version>-arm64.dmg` from the
   [releases page](https://github.com/dvcdsys/code-index/releases) and open it.
2. Drag **cix.app** onto **Applications**.
3. Open it from Applications — not from the mounted disk image.

Verify the download first if you like:

```bash
shasum -a 256 -c checksums.txt
```

### The first launch is blocked, and that is expected

macOS will refuse to open the app the first time, reporting that it "cannot be
verified" or "is damaged". Neither is true. cix is open source and is signed
**ad-hoc** rather than with a paid Apple Developer certificate, so it has no
Gatekeeper trust.

To allow it:

1. **System Settings → Privacy & Security**
2. Scroll down to **Security**. There is a message about cix being blocked.
3. Click **Open Anyway** and confirm.

This is once per installed version.

On macOS 15 and later, right-clicking the app and choosing **Open** no longer
works as a shortcut for this — the System Settings route is the only one.

### Why not Homebrew?

`brew install --cask` is not an option for this app. Homebrew ends support for
casks that fail Gatekeeper on 2026-09-01 and is removing the `--no-quarantine`
flag that used to work around it. Distributing through a cask would mean paying
for a Developer ID, which this project does not do.

### Open the app from /Applications, not from the disk image

If a quarantined app is opened from anywhere other than `/Applications`,
Gatekeeper runs it from a randomised read-only copy ("App Translocation"). It
appears to work, but the bundle path is temporary, so anything written into it —
or any background job pointing at it — breaks when the copy is discarded. The
launcher detects this and asks you to move the app rather than half-working.

## What is inside

```
cix.app/Contents/
  Info.plist
  MacOS/
    cix-launcher     the app itself
    cix-server       indexing + search server
    cix              command-line client
    llama/           Metal-accelerated llama-server + its libraries
  Resources/
    cix.icns         app icon
    cixTemplate.png  menu-bar glyph (and @2x)
```

Everything executable lives in `Contents/MacOS/`, including `llama/`. That is
not a style choice: `codesign --verify --strict` rejects executable code under
`Resources/`, and `cix-server` looks for `llama-server` at
`<dir of cix-server>/llama`, so keeping them siblings means `CIX_LLAMA_BIN_DIR`
never has to be set.

The versions of all three components are recorded in `Info.plist` under the
`CIXServerVersion`, `CIXCLIVersion` and `CIXLlamaVersion` keys, and each binary
also reports its own:

```bash
/Applications/cix.app/Contents/MacOS/cix-launcher -report
```

## First run

The very first launch asks for an email address, then generates a password and
an API key, starts the server, and shows you the credentials. You will be asked
to change the password when you first sign in.

This step is not decoration. `cix-server` refuses to start against an empty
database unless it is told which admin account to create — it will not invent
one silently — so an app with no configuration could never start its own server.

What it writes:

| Path | Contents |
|---|---|
| `~/.cix/server.env` | port, data paths, API key, bootstrap credentials (mode 0600) |
| `~/.cix/data/` | SQLite database and the index |
| `~/.cix/launchd/run-cix-server.sh` | launchd entry point; sources `server.env` |
| `~/Library/LaunchAgents/com.cix.server.plist` | the launchd agent |
| `~/.cix/config.yaml` | a `local` server entry, so the `cix` CLI works |

`server.env` is the single source of truth for the server process — the plist
carries no configuration. To change a setting, edit that file and use
**Stop Server** then **Start Server**.

The CLI's default server is left alone if you already had one. Installing an app
should not silently repoint a `cix` command that was talking to a remote server.

macOS will show a notification saying `run-cix-server.sh` can run in the
background, with a link to Login Items & Extensions. That is expected: it is how
macOS announces any newly registered background agent.

## The menu

```
cix-server: Running (:21847)
Embeddings: llama.cpp (bundled) — ready
Model: awhiteside/CodeRankEmbed-Q8_0-GGUF
─────────────
Stop Server
Open Dashboard
─────────────
Quit cix
```

**Starting…** is its own state, distinct from Running and Stopped. A cold start
loads the embedding model and can take anywhere from 30 seconds to several
minutes, in silence — a server showing "Starting…" is working, not stuck.

**Quit cix** quits the menu bar app only. The server is a launchd agent and
keeps running; use **Stop Server** first if you want it down.

**Open Dashboard** always targets the port in `server.env`, never the CLI's
configured server — that one may legitimately point at a remote cix.

### If you already use install-server.sh

Both use the same launchd label, `com.cix.server`. When the app finds an agent
it did not create, it does not touch it: status, the provider row and the
dashboard link keep working over HTTP, and Start/Stop are disabled and labelled
*managed externally*. The app is then useful alongside a development checkout
instead of fighting it.

## Using the CLI

Put it on your `PATH` as a **symlink**, so it keeps pointing at the current
bundle after an update:

```bash
ln -sf /Applications/cix.app/Contents/MacOS/cix /usr/local/bin/cix
```

### Forgotten password

`cix-server` can reset a password offline, without the server being stopped:

```bash
/Applications/cix.app/Contents/MacOS/cix-server -reset-password you@example.com
```

It prints a generated temporary password. Point it at the same `CIX_DATA_DIR` /
`CIX_SQLITE_PATH` the server uses, or it will not find the database:

```bash
set -a; source ~/.cix/server.env; set +a
/Applications/cix.app/Contents/MacOS/cix-server -reset-password you@example.com
```

## Building it yourself

```bash
MAC_VERSION=0.1.0-dev mac/scripts/build-app.sh
MAC_VERSION=0.1.0-dev mac/scripts/make-dmg.sh
```

See [`mac/README.md`](../mac/README.md) for the build pipeline, the signing
order and why each step is the way it is.

## Uninstalling

```bash
launchctl bootout "gui/$(id -u)/com.cix.server"
rm -f ~/Library/LaunchAgents/com.cix.server.plist ~/.cix/launchd/run-cix-server.sh
rm -rf /Applications/cix.app
rm -f  /usr/local/bin/cix          # if you created the symlink
rm -rf ~/.cix                      # config, database and index data
```
