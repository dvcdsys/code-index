# cix for macOS

`cix.app` packages the cix server, the `cix` CLI and a Metal-accelerated
`llama-server` into a single drag-to-install application for Apple Silicon.

> **Scope of the current release.** The packaging pipeline ships first: the app
> bundles and signs correctly, and every component reports its own version. The
> menu-bar interface — start/stop, provider status, dashboard link, password
> reset, autostart and self-update — lands in subsequent `mac/v*` releases.
> Until then the bundled binaries are run directly, as described below.

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
    AppIcon.icns  menubar.png  menubar@2x.png
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

## Running the server from the current release

The server refuses to start against an empty database unless it is told which
admin account to create — it will not invent one silently. On first run:

```bash
export CIX_DATA_DIR="$HOME/.cix/data"
export CIX_SQLITE_PATH="$HOME/.cix/data/cix.db"
export CIX_PORT=21847
export CIX_BOOTSTRAP_ADMIN_EMAIL="you@example.com"
export CIX_BOOTSTRAP_ADMIN_PASSWORD="choose-a-strong-one"

/Applications/cix.app/Contents/MacOS/cix-server
```

The dashboard is then at <http://localhost:21847/dashboard>, and you will be
required to change that bootstrap password at first login. On later runs the two
`CIX_BOOTSTRAP_ADMIN_*` variables are no longer needed.

Cold starts are slow and quiet: loading the embedding model takes 30–60 seconds
warm and can take several minutes the first time, and the ready banner only
appears at the end. A server that has not answered yet is usually still starting.

To use the CLI against it, put it on your `PATH` as a **symlink**, so it keeps
pointing at the current bundle after an update:

```bash
ln -sf /Applications/cix.app/Contents/MacOS/cix /usr/local/bin/cix
```

### Forgotten password

`cix-server` can reset a password offline, without the server being stopped:

```bash
/Applications/cix.app/Contents/MacOS/cix-server -reset-password you@example.com
```

It prints a generated temporary password. Point it at the same `CIX_DATA_DIR` /
`CIX_SQLITE_PATH` the server uses, or it will not find the database.

## Building it yourself

```bash
MAC_VERSION=0.1.0-dev mac/scripts/build-app.sh
MAC_VERSION=0.1.0-dev mac/scripts/make-dmg.sh
```

See [`mac/README.md`](../mac/README.md) for the build pipeline, the signing
order and why each step is the way it is.

## Uninstalling

```bash
rm -rf /Applications/cix.app
rm -f  /usr/local/bin/cix          # if you created the symlink
rm -rf ~/.cix                      # config, database and index data
```
