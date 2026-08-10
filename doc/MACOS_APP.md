# cix for macOS

`cix.app` is a drag-to-install menu bar app for Apple Silicon that runs a local
cix server — the server itself, the `cix` CLI and a Metal-accelerated
`llama-server` for embeddings.

The app sits in the menu bar and shows whether the server is running and what
its embedding provider is doing, with start/stop and a link to the dashboard.
There is no Dock icon and no window.

It keeps itself up to date, checks its downloads, and never asks for an
administrator password to do it.

The download is small — about 4 MB — because the server is not inside it. The
app fetches that part on first launch and keeps it in `~/.cix/runtime/`, which
is what lets it update the server without restarting itself, and roll back
automatically if a new one will not start.

The server it installs is the same build the Docker images are cut from, with
the same version number, and it is released on its own schedule. A new cix
server reaches your Mac without waiting for a new version of this app.

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

The app holds one executable:

```
cix.app/Contents/
  Info.plist
  MacOS/
    cix-launcher     the menu bar app
  Resources/
    cix.icns         app icon
    cixTemplate.png  menu-bar glyph (and @2x)
```

Everything it runs lives outside it, under your home directory:

```
~/.cix/runtime/
  0.12.8/      cix-server  cix  llama/  runtime.json
  0.12.7/      the version this one replaced, kept for rollback
  current ->   0.12.8
```

Those are *server* versions — the same ones on Docker Hub. The app has its own,
smaller version, and the two have nothing to do with each other.

`llama/` sits next to `cix-server` because `cix-server` looks for
`llama-server` at `<dir of cix-server>/llama`, so keeping them siblings means
`CIX_LLAMA_BIN_DIR` never has to be set. They are downloaded, checked and
installed as one thing — a llama version is part of a server release, not
something tracked separately.

`current` is a symlink, and that is the whole trick: updating the server means
extracting the new one beside the old and renaming the symlink, which is atomic
and instantly reversible. Nothing has to quit, and the version that was working
five seconds ago is still on disk.

To see what is installed:

```bash
/Applications/cix.app/Contents/MacOS/cix-launcher -report
```

It asks each binary for its own version rather than reading the manifest, which
is what catches a runtime that is not what it claims to be.

## First run

The very first launch asks for an email address, downloads the runtime (about
40 MB), then generates a password and an API key, starts the server, and shows
you the credentials. You will be asked to change the password when you first
sign in.

The download happens before anything is written. A setup that had created an
account and a background agent pointing at a server that was never downloaded
would look finished and be broken.

This step is not decoration. `cix-server` refuses to start against an empty
database unless it is told which admin account to create — it will not invent
one silently — so an app with no configuration could never start its own server.

What it writes:

| Path | Contents |
|---|---|
| `~/.cix/server.env` | port, data paths, API key, bootstrap credentials (mode 0600) |
| `~/.cix/runtime/` | the server, the CLI and llama-server; one directory per version |
| `~/.cix/data/` | SQLite database and the index |
| `~/.cix/launchd/run-cix-server.sh` | launchd entry point; sources `server.env` |
| `~/Library/LaunchAgents/com.cix.server.plist` | the launchd agent |
| `~/.cix/config.yaml` | a `local` server entry, so the `cix` CLI works |
| `~/.cix/launcher.json` | remembered answers, currently only the takeover choice |
| `~/.cix/logs/launcher.log` | the app's own log (mode 0600) |

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
● cix-server: Running (:21847)      ▸  Process: 78903
● Embeddings: llama.cpp (bundled)      Port: 21847
  Model: awhiteside/Co…bed-Q8_0-GGUF   Network: this Mac only
─────────────                          Model: awhiteside/CodeRankEmbed-Q8_0-GGUF
Stop Server                            Server 0.12.8
Open Dashboard                         Server 0.12.8 (llama b10238)
─────────────
Start at Login                ✓
Allow Network Access          ✓
Reset Password…
─────────────
Check for Updates…
─────────────
Quit (server keeps running)
```

The dot carries the state: green running, amber starting, red stopped, grey
unknown. Rows are truncated so the menu stays a predictable width instead of
being as wide as whichever model happens to be configured; the submenu on the
server row holds the full values and the things that do not fit — the process
id, the port, the network exposure, the untruncated model name and which server
is installed. The last two rows differ on purpose: the first is what the running
server reports over HTTP, the second is what is on disk — and only the second
still says anything when the server is not running, which is when it matters.

There are no tooltips anywhere in this app, deliberately. AppKit's have two
behaviours that cannot be changed through any API: once one of them has
appeared, every subsequent one shows with no delay at all, and they are placed
against the element rather than the pointer. A submenu gets native timing and
placement for free.

**Starting…** is its own state, distinct from Running and Stopped. A cold start
loads the embedding model and can take anywhere from 30 seconds to several
minutes, in silence — a server showing "Starting…" is working, not stuck.

### Start at Login

Whether launchd starts the server when you log in. Off after installation.

Toggling it reloads the launchd agent, which stops the server for a moment —
launchd reads a job's configuration only when the job is loaded, so there is no
way to change this in place. The app restarts the server if it was running, so
the interruption is a few seconds and nothing else.

This is `RunAtLoad` in the plist, and the menu reads it back from the file
rather than remembering it, so the checkbox cannot drift from what launchd will
actually do.

### Reset Password…

Generates a new temporary password for an account and signs out its other
sessions; the next sign-in forces a change. The server does not need to be
stopped — this opens the database directly, so holding the database file is the
authorisation.

The underlying command prints every account in the database when it does not
recognise an address, which is reasonable at a terminal and not something to put
on screen. The dialog says only "No account with that email address."; the full
output goes to `~/.cix/logs/launcher.log` (mode 0600).

### Check for Updates…

cix watches two release streams — `mac/v*` for the app, `server/v*` for the
server — when it starts and at most every 30 minutes after that, and only speaks
up when one of them has something. The menu item does the same check
immediately.

The two update independently, and the dialog says which one is happening,
because they feel completely different.

**The server** — with the CLI and llama-server — is downloaded from its release,
checked against `checksums.txt`, unpacked beside the version in use,
signature-verified and test-run, all before anything live is touched. Only then
is the running server stopped, the `current` symlink moved, and the server
started again. The app stays open throughout; the menu bar item shows what it is
doing.

If the new server does not come back, cix moves the symlink back and starts the
old one, without asking and without downloading anything. "Does not come back"
means the process exited — not that `/health` was slow, because a cold start
loads an embedding model and can legitimately take minutes.

**The app** is replaced whole, by a detached helper that waits for the launcher
to quit and then swaps the bundle. A process cannot overwrite its own signed
executable and survive, so this half does mean cix closes and reopens. The
server is *not* stopped for it: nothing a running server touches is inside the
bundle any more.

When both have something, the server goes first — it is the reversible one.

If the folder containing cix.app is not writable by you, the app half stops
before downloading anything and tells you to install the new version by hand. It
will not ask for an administrator password — an unsigned app requesting admin
rights to overwrite itself is exactly what malware looks like, and it is not a
habit worth teaching.

> The checksum proves the download arrived intact. It is not a trust anchor:
> `checksums.txt` travels the same path as the image, so anyone who can replace
> one can replace the other. Without a Developer ID signature there is nothing
> stronger available here — a detached signature over the checksums would be
> the next step.

### Allow Network Access

Off, the server binds to `127.0.0.1` and only this Mac can reach it. On, it
binds to every interface and any machine that can reach this Mac on its port can
too — useful for querying your index from a laptop or a phone on the same
network, and not something to leave on by accident. Turning it **on** asks for
confirmation; turning it off does not.

Accounts and API keys apply either way; this does not disable authentication. It
decides whether the login page and the API are reachable at all.

The setting is `CIX_BIND_ADDR` in `server.env`, and it is read once at process
start, so changing it restarts the server.

> The server's own default (and every container's) is to bind all interfaces.
> The app deliberately differs: a fresh desktop install starts loopback-only.
> An install that predates this setting keeps whatever it had — the app does not
> silently narrow a server you already rely on.

**Quit cix** quits the menu bar app only. The server is a launchd agent and
keeps running; use **Stop Server** first if you want it down.

**Open Dashboard** always targets the port in `server.env`, never the CLI's
configured server — that one may legitimately point at a remote cix.

### If you already use install-server.sh

Both use the same launchd label, `com.cix.server`, and `install-server.sh`
points it at a repo checkout. So on a machine that already runs cix from a
clone, the app finds an agent it did not create, pointing at a server it does
not own, holding the port it would use.

It asks once what to do, shows you the paths it found, and remembers the answer
in `~/.cix/launcher.json`:

- **Leave It Alone** — observe-only. Status, the provider row and the dashboard
  link keep working over HTTP; Start/Stop, autostart and the network toggle are
  disabled. The app is then useful alongside a development checkout instead of
  fighting it. No runtime is downloaded on this path — watching someone else's
  server needs no binaries of our own. Password reset does, because it opens the
  database directly, so it offers the download the first time you use it.
- **Take Over** — the app copies the port, API key and database paths out of
  the `.env` the old wrapper sourced, backs the old plist and wrapper up under
  `~/.cix/backup/`, and installs its own. Re-running `install-server.sh` will
  claim the agent back.

Only those four settings are migrated. The rest of an `install-server.sh` `.env`
— model tuning, GPU layers, tunnel credentials — belongs to that installation,
and importing it wholesale would apply settings you never chose for this app.

The first-run wizard never runs while a foreign agent is present: it would set
up a second server that cannot bind the port, against a second, empty database.

## Using the CLI

Put it on your `PATH` as a **symlink** into the runtime, so it keeps following
updates:

```bash
ln -sf ~/.cix/runtime/current/cix /usr/local/bin/cix
```

Point it at `current`, not at a version directory: `current` is what moves when
the runtime is updated, and old versions are eventually deleted.

The CLI ships with the server rather than inside the app because it speaks to a
specific server's API, and pinning the two together is the point.

### Forgotten password, from a terminal

The menu's **Reset Password…** does this for you. The same thing by hand:

```bash
~/.cix/runtime/current/cix-server -reset-password you@example.com
```

It prints a generated temporary password. Point it at the same `CIX_DATA_DIR` /
`CIX_SQLITE_PATH` the server uses, or it will not find the database:

```bash
set -a; source ~/.cix/server.env; set +a
~/.cix/runtime/current/cix-server -reset-password you@example.com
```

## Building it yourself

```bash
SERVER_VERSION=0.0.0-dev mac/scripts/build-runtime.sh
MAC_VERSION=0.1.0-dev    mac/scripts/build-app.sh
MAC_VERSION=0.1.0-dev    mac/scripts/make-dmg.sh
```

See [`mac/README.md`](../mac/README.md) for the build pipeline, the signing
order and why each step is the way it is.

## Uninstalling

```bash
launchctl bootout "gui/$(id -u)/com.cix.server"
rm -f ~/Library/LaunchAgents/com.cix.server.plist ~/.cix/launchd/run-cix-server.sh
rm -rf /Applications/cix.app
rm -f  /usr/local/bin/cix          # if you created the symlink
rm -rf ~/.cix                      # config, runtime, database and index data
```
