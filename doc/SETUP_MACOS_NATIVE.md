# Native macOS setup (Apple Silicon, Metal GPU)

Docker Desktop on macOS runs containers inside a Linux VM, and the
Metal GPU is **not accessible** from within that VM. For full Metal
acceleration on Apple Silicon you must run cix-server natively.

> For Docker (CPU) and Docker (CUDA) deployments, follow README's
> *Quick Start* section instead. This doc is only for native macOS.

## 1. Install (recommended: the installer)

Prerequisites:

- Apple Silicon Mac (M1/M2/M3/M4 family). Intel Macs are not supported
  by the bundled `llama-server` build.
- Go 1.25+ (`brew install go` or [go.dev/dl](https://go.dev/dl)).
- Node.js (`brew install node`) — builds the embedded dashboard.
- Xcode Command Line Tools — `xcode-select --install` if you don't
  already have them.

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
./install-server.sh
```

or, without cloning first (the installer clones for you):

```bash
curl -fsSL https://raw.githubusercontent.com/dvcdsys/code-index/main/install-server.sh | bash
```

The same installer also handles the Docker modes — on an Apple Silicon
Mac it defaults to `native`. It checks the prerequisites, asks a few
questions (each has a sensible default — pressing Enter through all of
them works):

| Question | Default |
|---|---|
| Mode | `native` on Apple Silicon |
| Data directory | `~/.cix/data` |
| HTTP port | `21847` (on a re-run: the port already in `.env`) |
| Admin email | your `git config user.email` |
| Admin password | auto-generated (printed at the end) |
| Run mode | `launchd` (background, starts at login) |
| Install + connect the `cix` CLI | yes (fresh installs; an existing default server is never overridden) |

then builds `cix-server` + the dashboard, downloads the Metal-enabled
`llama-server`, writes the configuration to `.env`, installs a
`launchd` agent, and waits for the server to come up. At the end it
prints the dashboard URL and your login. That's it — sign in and
change the password when prompted.

Whatever password path you chose, it is temporary: the dashboard
forces a change on first login.

Non-interactive variant (CI, provisioning scripts):

```bash
./install-server.sh --non-interactive --mode native --email you@example.com
```

All flags: `./install-server.sh --help`.

### Everyday management

```bash
tail -f ~/.cix/logs/cix-server.err                    # logs
launchctl kickstart -k gui/$(id -u)/com.cix.server    # restart
launchctl bootout gui/$(id -u)/com.cix.server         # stop
./install-server.sh --uninstall                       # remove the agent (keeps data + .env)
```

Configuration lives in `.env` at the repo root — edit it and restart.
The `launchd` agent reads `.env` on every start (via a generated
launcher script at `~/.cix/launchd/run-cix-server.sh`), so there is no
second copy of the settings to keep in sync.

### Upgrading

```bash
./install-server.sh    # checks the remote, offers to git pull, rebuilds; data, accounts and .env are kept
```

The port question defaults to the port already in `.env`, so pressing
Enter changes nothing. Answering differently (or passing `--port`)
rewrites `CIX_PORT` in the kept `.env` and restarts the server there.

### Forgot the admin password?

If another admin exists, they can reset yours from **Dashboard →
Users**. If you're locked out entirely, reset it offline on the server
machine:

```bash
./server/scripts/reset-password.sh you@example.com
```

Leave the prompt empty to have a strong password generated and
printed. The account is forced to change it on next login and all its
sessions are revoked. The server can keep running — no restart needed.

(Docker deployments run the underlying binary directly:
`docker exec -i <container> /cix-server -reset-password you@example.com`.)

## 2. Verify

```bash
curl http://localhost:21847/health   # → {"status":"ok"}
```

Open `http://localhost:21847/dashboard` and sign in with the admin
email + password printed by the installer. You'll be forced to change
the password on first login. Next: mint an API key and connect the CLI
(README Quick Start, steps 2–4).

## 3. Manual setup (what the installer automates)

<details>
<summary>Expand if you'd rather wire everything yourself</summary>

### Build

```bash
cd server && make bundle
```

`make bundle` builds `cix-server` (dashboard included) and downloads
the Metal-enabled `llama-server` (llama.cpp + `libggml-metal.dylib`).
The binaries land in `server/dist/cix-darwin-arm64/`.

Each step is skipped when it would reproduce identical output, so a
repeat `make bundle` (or `make run`) takes well under a second instead
of re-downloading ~11 MB from GitHub and re-signing 52 MB of dylibs.
Force a step when you need to:

| Variable | Forces |
|---|---|
| `LLAMA_FORCE=1` | re-fetch llama.cpp, restage `dist/llama/`, and rebuild the bundle |
| `BUNDLE_FORCE=1` | re-copy + re-sign the bundle's `llama/` only |
| `DASHBOARD_FORCE=1` | rebuild the React dashboard |

Verified llama.cpp archives are cached in `~/.cache/cix/llama/`
(override with `LLAMA_CACHE_DIR`), so even a forced re-fetch is
usually offline.

> The bundled `llama-server` is re-signed whenever it is copied into
> the bundle (commit `8c56fc3`) so macOS amfid doesn't kill it on
> first launch. If you see "killed: 9" on startup, run
> `make bundle BUNDLE_FORCE=1` to refresh the signature — a plain
> `make bundle` will skip the copy, and the re-sign with it.

### Configure

Copy the environment template and fill in the required values:

```bash
cp .env.example .env
```

The minimum env-var set for a Metal native run:

| Variable | Recommended | Notes |
|---|---|---|
| `CIX_API_KEY` | (any 256-bit value) | Bearer token for CLI / agent traffic. |
| `CIX_BOOTSTRAP_ADMIN_EMAIL` | (your email) | Required for the first boot only — fresh DB seeds. |
| `CIX_BOOTSTRAP_ADMIN_PASSWORD` | (strong value) | Required for the first boot only — must be changed at first login. |
| `CIX_N_GPU_LAYERS` | (leave unset) | macOS defaults to offloading all layers to Metal. `0` forces CPU. |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Default. Set `false` to skip the sidecar entirely. |
| `CIX_LLAMA_BIN_DIR` | (set by `make run`) | Path to the `llama-server` bundle dir. The dev runner sets it; for `launchd` you set it yourself (see below). |

The full env-var surface is documented in
[`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md).

### Run in the foreground (dev)

```bash
cd server && make run
```

`make run` runs `make bundle` first (no-op if already built), loads
`.env`, and launches the server in the foreground. Tail logs in the
terminal; Ctrl-C to stop.

### Auto-start with launchd

For a "runs in the background on login" setup, drop a `launchd` plist
into `~/Library/LaunchAgents/`. Replace every `/ABSOLUTE/PATH/TO/`
and `YOUR_USER` placeholder before loading.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cix.server</string>

  <key>ProgramArguments</key>
  <array>
    <string>/ABSOLUTE/PATH/TO/server/dist/cix-darwin-arm64/cix-server</string>
  </array>

  <key>EnvironmentVariables</key>
  <dict>
    <key>CIX_API_KEY</key><string>YOUR_KEY</string>
    <key>CIX_BOOTSTRAP_ADMIN_EMAIL</key><string>admin@example.com</string>
    <key>CIX_BOOTSTRAP_ADMIN_PASSWORD</key><string>change-me-on-first-login</string>
    <key>CIX_LLAMA_BIN_DIR</key><string>/ABSOLUTE/PATH/TO/server/dist/cix-darwin-arm64/llama</string>
    <key>CIX_PORT</key><string>21847</string>
    <key>CIX_SQLITE_PATH</key><string>/Users/YOUR_USER/.cix/data/sqlite/projects.db</string>
    <key>CIX_CHROMA_PERSIST_DIR</key><string>/Users/YOUR_USER/.cix/data/chroma</string>
    <key>CIX_GGUF_CACHE_DIR</key><string>/Users/YOUR_USER/.cix/data/models</string>
  </dict>

  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/cix-server.log</string>
  <key>StandardErrorPath</key><string>/tmp/cix-server.err</string>
</dict></plist>
```

Save as `~/Library/LaunchAgents/com.cix.server.plist`, then:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.cix.server.plist
launchctl print gui/$(id -u)/com.cix.server   # confirm it really loaded
```

Use `bootstrap`, not the legacy `launchctl load`: `load` prints its
errors but still exits `0`, so a failed load looks like a successful
one. Either way, `launchctl print` is the only trustworthy answer to
"is it loaded?".

After every `git pull` that updates `server/`, rebuild and the
plist picks up the new binary automatically (the path doesn't
change):

```bash
cd server && make bundle
launchctl kickstart -k gui/$(id -u)/com.cix.server  # restart onto the new binary
```

(The installer's variant of this differs in one way: its plist runs a
launcher script that sources `.env`, so configuration stays in one
place instead of being duplicated into `EnvironmentVariables`.)

</details>

## 4. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Installer stops at "Checking prerequisites" | Missing Go / Node / Xcode CLT. | Follow the `brew install` hint it prints, re-run. |
| `make bundle` fails downloading llama-server | Network blocked, or upstream release moved. | Inspect `server/Makefile`'s download URL; report if upstream changed. |
| Server starts but `/health` 404s | Wrong port. | `lsof -i :21847` to confirm. Check `CIX_PORT` in `.env`. |
| Health check takes minutes on first boot | The embedding model (~150 MB) downloads before serving. | Watch `tail -f ~/.cix/logs/cix-server.err`; it's a one-time cost. |
| GPU not used (CPU fallback) | `CIX_N_GPU_LAYERS=0` set in `.env`. | Remove it (macOS default offloads all layers) or set `99`. |
| "killed: 9" on first llama-server launch | macOS amfid rejected the unsigned binary. | Re-run `make bundle` (or the installer) to refresh the local signature. |
| Server starts via terminal but not via `launchd` | Launcher script or `.env` missing / unreadable. | Check `~/.cix/logs/cix-server.err`; re-run `./install-server.sh` to regenerate. |
| Can't log in — password lost | — | `./server/scripts/reset-password.sh <email>` (see above). |

## 5. Related files

- `install-server.sh` — the interactive installer / uninstaller
- `server/scripts/reset-password.sh` — offline password recovery
- `server/Makefile` — `bundle` / `run` targets
- [`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md) — full env-var surface
- [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md) — production hardening
- [`vram-profiling.md`](vram-profiling.md) — Metal memory profile
