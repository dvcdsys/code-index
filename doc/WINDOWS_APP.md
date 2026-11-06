# cix for Windows

> **Status: experimental.** The Windows port is new and has been exercised on a
> single machine (Windows 11 with an NVIDIA GPU). The install, first-run, start/stop,
> server update, rollback and app self-update paths were run end to end there;
> nothing has been tried on other hardware, other Windows builds, a non-NVIDIA
> machine, or a fresh release published from CI. Expect rough edges, and please
> report what you find.

The cix tray app runs a local cix server on Windows 11 — the server itself, the
`cix` CLI and a CUDA-accelerated `llama-server` for embeddings.

It sits in the notification area and shows whether the server is running and what
its embedding provider is doing, with start/stop and a link to the dashboard.
There is no taskbar button and no window until you click the icon.

It keeps itself up to date, checks its downloads, and never asks for an
administrator password to do it.

The installer is small — about 5 MB — because the server is not inside it. The app
fetches that part on first launch (about 500 MB, almost all of it NVIDIA's CUDA
libraries) and keeps it in `%USERPROFILE%\.cix\runtime\`, which is what lets it
update the server without restarting itself, and roll back automatically if a new
one will not start.

The server it installs is the same build the Docker images are cut from, with the
same version number, and it is released on its own schedule. A new cix server
reaches your PC without waiting for a new version of this app.

## Requirements

- Windows 11, x64
- An NVIDIA GPU with a driver that supports **CUDA 13** (R580 or newer). Blackwell
  cards (RTX 50 series) need CUDA 12.8 or later, which is why the bundled
  `llama-server` is the CUDA 13 build; older cards work with it too.
- The WebView2 Runtime — part of Windows 11. Without it the tray icon still works
  (right-click: Open Dashboard, Quit), but the panel cannot draw.

There is no ARM build and no CPU-only build: upstream publishes those separately
and the runtime ships one `llama-server`.

## Install

1. Download `cix-<version>-x64-setup.exe` from the
   [releases page](https://github.com/dvcdsys/code-index/releases) (the `win/v*`
   releases).
2. Run it. It installs for your user only — no administrator prompt.
3. Leave **Launch cix** ticked on the last page.

Verify the download first if you like:

```powershell
(Get-FileHash .\cix-*-x64-setup.exe -Algorithm SHA256).Hash
```

and compare it with `checksums.txt` from the same release.

### SmartScreen will warn once, and that is expected

cix is open source and is **not** signed with a paid code-signing certificate, so
SmartScreen reports an unknown publisher: *"Windows protected your PC"*. It is not
damaged and there is nothing wrong with the download. Choose **More info → Run
anyway**.

The checksum proves the download arrived intact. It is not a trust anchor —
`checksums.txt` travels the same path as the installer, so anyone who can replace
one can replace the other. Without a signature there is nothing stronger available
here.

### What the installer does

| Setting | Default | What it does |
|---|---|---|
| Install location | `%LOCALAPPDATA%\Programs\cix` | the tray app and its icon, nothing else |
| Start the cix tray icon when I sign in | on | `HKCU\...\Run` entry |
| Add the cix command to my PATH | on | appends `%USERPROFILE%\.cix\runtime\current` to your user `PATH` |

Open a **new** terminal after installing for the `PATH` change to apply.

## What is inside

```
%LOCALAPPDATA%\Programs\cix\
  cix-launcher.exe     the tray app
  cix.ico              application icon
```

Everything it runs lives outside it, under your home directory:

```
%USERPROFILE%\.cix\runtime\
  0.13.0\        cix-server.exe  cix.exe  llama\  runtime.json
  0.12.9\        the version this one replaced, kept for rollback
  current        junction -> 0.13.0
```

Those are *server* versions — the same ones on Docker Hub. The app has its own,
smaller version, and the two have nothing to do with each other.

`llama\` sits next to `cix-server.exe` because `cix-server` looks for
`llama-server.exe` at `<dir of cix-server>\llama`.

`current` is a directory junction (the Windows counterpart of the macOS symlink —
it needs no administrator rights, unlike a symbolic link). Updating the server
means extracting the new one beside the old and repointing the junction, and the
version that was working five seconds ago is still on disk.

To see what is installed:

```powershell
& "$env:LOCALAPPDATA\Programs\cix\cix-launcher.exe" -report
```

It asks each binary for its own version rather than reading the manifest, which is
what catches a runtime that is not what it claims to be.

## First run

The very first launch asks for an email address, downloads the runtime, then
generates a password and an API key, starts the server, and shows you the
credentials. You will be asked to change the password when you first sign in.

The download happens before anything is written. A setup that had created an
account and a scheduled task pointing at a server that was never downloaded would
look finished and be broken.

What it writes:

| Path | Contents |
|---|---|
| `~\.cix\server.env` | port, data paths, API key, bootstrap credentials |
| `~\.cix\runtime\` | the server, the CLI and llama-server; one directory per version |
| `~\.cix\data\` | SQLite database and the index |
| scheduled task `cix-server` | runs `cix-server.exe -env-file ~\.cix\server.env`; see below |
| `~\.cix\config.yaml` | a `local` server entry, so the `cix` CLI works |
| `~\.cix\launcher.json` | remembered answers, currently only the takeover choice |
| `~\.cix\logs\` | `launcher.log` (the app), `cix-server.log` / `cix-server.err` (the server) |

`server.env` is the single source of truth for the server process. To change a
setting, edit that file and use **Stop Server** then **Start Server**. It sits in
your profile, which only you, administrators and SYSTEM can read.

## How the server runs

The server is a **per-user scheduled task** named `cix-server`, the Windows
counterpart of the macOS launch agent. A task runs as you, in your session, needs
no administrator rights to register, survives the tray app quitting, and can start
at sign-in. A Windows Service would need elevation to install.

- **Start Server** runs the task. **Stop Server** sends the server Ctrl+Break, which
  it handles like SIGTERM — it drains, stops `llama-server`, and exits — and only
  terminates it if that does not work within 30 seconds.
- **Launch at login** adds or removes the task's sign-in trigger. Changing it does
  not restart a running server.
- The task never restarts the server on its own. A crashed server shows as stopped
  within five seconds, which is more honest than a restart loop.
- `llama-server` is tied to the server by a Job Object, so it cannot outlive it.

You can see the task in Task Scheduler (`taskschd.msc`) under its name; its
description says it is managed by the cix launcher.

## The panel

Click the tray icon (it may be under the `^` overflow — drag it onto the taskbar to
pin it). The panel shows the status, port, uptime, the embedding provider and
model, the installed server version, and the actions:

- **Start Server / Stop Server**, **Open Dashboard**
- **Allow network access** — see below
- **Launch at login**
- **Password…** — reset the admin password
- **Updates…** — check for a newer server and app
- **Quit cix** — quits the tray app only; the server keeps running

Right-click the icon for a small menu that works even when the panel cannot draw.

There is nothing in the panel that needs the network except updates.

### Allow Network Access

Off, the server binds to `127.0.0.1` and only this PC can reach it. On, it binds to
every interface and any machine that can reach this PC on its port can too — useful
for querying your index from a laptop on the same network, and not something to
leave on by accident. Turning it **on** asks for confirmation; turning it off does
not.

Accounts and API keys apply either way. The setting is `CIX_BIND_ADDR` in
`server.env`, read once at process start, so changing it restarts the server.

Windows Defender Firewall will ask whether to allow `cix-server.exe` on private
networks the first time it listens on a non-loopback address; that prompt is
Windows', not cix's.

## Updating

**Updates…** (and a quiet check shortly after launch) asks GitHub for the newest
release of each stream — `server/v*` for the server, `win/v*` for the app — and
offers whatever is behind. Everything is downloaded and checked before anything is
replaced.

- **Server:** installed beside the old one, the junction repointed, the server
  restarted. If the new server does not come back, the app switches back
  automatically and says so.
- **App:** the new installer runs silently and reopens the app. The server keeps
  running throughout.

A release only counts if it carries this platform's asset, so a server release that
predates Windows support is never offered.

## Using the CLI

If you left **Add the cix command to my PATH** on, `cix` works in any new terminal
and follows server updates. Otherwise:

```powershell
$env:Path += ";$env:USERPROFILE\.cix\runtime\current"
```

Point at `current`, not at a version directory: `current` is what moves when the
runtime is updated, and old versions are eventually deleted.

### Forgotten password, from a terminal

The panel's **Password…** does this for you. The same thing by hand:

```powershell
& "$env:USERPROFILE\.cix\runtime\current\cix-server.exe" -env-file "$env:USERPROFILE\.cix\server.env" -reset-password you@example.com
```

It prints a generated temporary password.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| The tray icon is there but the panel does not open | The WebView2 Runtime is missing. Install it from Microsoft; the right-click menu still works meanwhile. |
| The server starts and stops straight away | `~\.cix\logs\cix-server.err` has the reason — the panel shows its tail. A missing GPU driver shows up here. |
| Embeddings never become ready | `nvidia-smi` should list `llama-server.exe` while the server runs. A driver older than R580 cannot load the CUDA 13 libraries. |
| "Another cix installation was found" | A scheduled task called `cix-server` exists that this app did not create. **Leave It Alone** keeps it observe-only; **Take Over** backs it up under `~\.cix\backup` and replaces it. |
| `cix` works in your terminal but not over SSH or inside an installer | Windows 11 refuses to follow a user-created junction in processes that have opted into Redirection Guard. Call `%USERPROFILE%\.cix\runtime\<version>\cix.exe` directly. The tray app is not affected. |
| The icon is missing after Explorer restarts | The app re-adds it by itself; if it does not, quit and reopen cix. |

## Building it yourself

```bash
WIN_VERSION=0.1.0 win/scripts/build-app.sh                     # app + installer
SERVER_VERSION=0.0.0-dev win/scripts/build-runtime.sh          # the runtime zip
```

See [`win/README.md`](../win/README.md) for the build pipeline.

## Uninstalling

Use **Settings → Apps → Installed apps → cix → Uninstall** (or run
`unins000.exe` from the install folder). It stops the server, removes the
`cix-server` task, the `PATH` entry and the sign-in entry, and asks whether to
delete `%USERPROFILE%\.cix` — your configuration, the runtime, the database and the
index. Silent uninstalls (`/VERYSILENT`) leave that folder alone.
