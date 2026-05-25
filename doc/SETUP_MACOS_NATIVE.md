# Native macOS setup (Apple Silicon, Metal GPU)

Docker Desktop on macOS runs containers inside a Linux VM, and the
Metal GPU is **not accessible** from within that VM. For full Metal
acceleration on Apple Silicon you must run cix-server natively. This
doc covers the build, the env vars Metal cares about, and a working
`launchd` plist for running it as a login agent.

> For Docker (CPU) and Docker (CUDA) deployments, follow README's
> *Quick Start* section instead. This doc is only for native macOS.

## 1. Build

Prerequisites:

- Apple Silicon Mac (M1/M2/M3/M4 family). Intel Macs are not supported
  by the bundled `llama-server` build.
- Go 1.25+ (`brew install go` or [go.dev/dl](https://go.dev/dl)).
- Xcode Command Line Tools — `xcode-select --install` if you don't
  already have them.

```bash
git clone https://github.com/dvcdsys/code-index && cd code-index
cd server && make bundle
```

`make bundle` builds `cix-server` and downloads the Metal-enabled
`llama-server` (llama.cpp + `libggml-metal.dylib`). The binaries land
in `server/dist/cix-darwin-arm64/`.

> The bundled `llama-server` is re-signed at bundle time (commit
> `8c56fc3`) so macOS amfid doesn't kill it on first launch. If you
> see "killed: 9" on startup, re-run `make bundle` to refresh the
> signature.

## 2. Configure

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
| `CIX_N_GPU_LAYERS` | `99` | Offload all layers to Metal. `0` forces CPU. |
| `CIX_EMBEDDINGS_ENABLED` | `true` | Default. Set `false` to skip the sidecar entirely. |
| `CIX_LLAMA_BIN_DIR` | (set by `make run`) | Path to the `llama-server` bundle dir. The dev runner sets it; for `launchd` you set it yourself (see below). |

The full env-var surface is documented in
[`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md).

## 3. Run (dev)

```bash
cd server && make run
```

`make run` runs `make bundle` first (no-op if already built), loads
`.env`, and launches the server in the foreground. Tail logs in the
terminal; Ctrl-C to stop.

Verify:

```bash
curl http://localhost:21847/health   # → {"status":"ok"}
```

Open `http://localhost:21847/dashboard` and sign in with the bootstrap
admin email + password. You'll be forced to change the password on
first login.

## 4. Auto-start with launchd

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
    <key>CIX_N_GPU_LAYERS</key><string>99</string>
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
launchctl load ~/Library/LaunchAgents/com.cix.server.plist
launchctl start com.cix.server
```

The agent starts at login and respawns on crash. Logs:

```bash
tail -f /tmp/cix-server.log
tail -f /tmp/cix-server.err
```

To stop / disable / reload:

```bash
launchctl stop com.cix.server
launchctl unload ~/Library/LaunchAgents/com.cix.server.plist
# re-load after editing the plist
launchctl load ~/Library/LaunchAgents/com.cix.server.plist
```

After every `git pull` that updates `server/`, rebuild and the
plist picks up the new binary automatically (the path doesn't
change):

```bash
cd server && make bundle
launchctl stop com.cix.server  # KeepAlive will respawn the new binary
```

## 5. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `make bundle` fails downloading llama-server | Network blocked, or upstream release moved. | Inspect `server/Makefile`'s download URL; report if upstream changed. |
| Server starts but `/health` 404s | Wrong port. | `lsof -i :21847` to confirm. Check `CIX_PORT`. |
| GPU not used (CPU fallback) | `CIX_N_GPU_LAYERS` unset or `0`. | Set to `99`. `make run` logs the resolved value at startup. |
| "killed: 9" on first llama-server launch | macOS amfid rejected the unsigned binary. | Re-run `make bundle` to refresh the local signature. |
| Server starts via terminal but not via `launchd` | `EnvironmentVariables` plist block missing a required var. | Run `launchctl getenv CIX_API_KEY` — empty means the agent doesn't see it. Re-edit the plist and `launchctl load` again. |

## 6. Related files

- `server/Makefile` — `bundle` / `run` targets
- [`CONFIG_REFERENCE.md`](CONFIG_REFERENCE.md) — full env-var surface
- [`SECURITY_DEPLOYMENT.md`](SECURITY_DEPLOYMENT.md) — production hardening
- [`vram-profiling.md`](vram-profiling.md) — Metal memory profile
