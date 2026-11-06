# The Windows runtime

The Windows build of the cix server, as the desktop launcher installs and runs
it. This page is about the **runtime** — what is in it, how it is built, how it
behaves differently from the Linux/macOS server and how to prove a build works.
Using the launcher is covered separately.

## What it is

One zip per server release, attached to the same `server/vX.Y.Z` GitHub release
as the Docker images:

```
cix-runtime-0.15.1-windows-amd64.zip
└─ cix-runtime-0.15.1/
   ├─ cix-server.exe
   ├─ cix.exe                     the CLI, pinned to this server
   ├─ runtime.json                {"server_version","cli_version","llama_version","platform":"windows-amd64","cuda":"13.3"}
   └─ llama/
      ├─ llama-server.exe
      ├─ ggml-cuda.dll, ggml*.dll, llama*.dll, mtmd.dll …
      └─ cudart64_13.dll, cublas64_13.dll, cublasLt64_13.dll
```

It is the same server as the container with the same version number, cross-
compiled (`CGO_ENABLED=0`). `llama/` sits next to `cix-server.exe` because the
server looks for its sidecar at `<dir of its own executable>\llama`, so
`CIX_LLAMA_BIN_DIR` never has to be set.

About 545 MB, almost all of it NVIDIA's cuBLAS (`cublasLt64_13.dll` alone is
460 MB). The CUDA runtime is bundled, so **no CUDA Toolkit install is needed** —
only an NVIDIA driver.

### Requirements

- Windows 10/11, x64
- NVIDIA GPU with a driver supporting **CUDA 13** (R580 or newer)

The llama.cpp asset is the CUDA **13.3** build, not 12.4: Blackwell (RTX 50
series, `sm_120`) needs CUDA ≥ 12.8, and the 12.4 build carries no kernels for
it. Without an NVIDIA GPU `llama-server` falls back to the CPU backends
(`ggml-cpu-*.dll`), so the server still runs, just slowly.

## How the server differs on Windows

| Concern | Linux / macOS | Windows |
|---|---|---|
| Sidecar transport | unix socket | loopback **TCP**, auto-picked port. `llama-server`'s socket mode is POSIX-only. A persisted `unix` setting is downgraded to TCP at start. |
| GPU layers default | `0` (macOS: `-1`) | `-1` — all layers; with no CUDA device it stays on the CPU. |
| Child processes | own process group, `SIGTERM`/`SIGKILL` | one **Job Object** with `KILL_ON_JOB_CLOSE`: `llama-server`, `cloudflared` and `ngrok` die with `cix-server` **even when it is killed hard**, so a crash cannot leave a process holding the GPU. |
| Graceful stop | `SIGTERM` | `CTRL_BREAK` / `CTRL_C` (Go delivers both as `os.Interrupt`) — see below. |
| Compacted-database restart | `exec(2)` in place | starts a successor process and exits (Windows has no exec). |
| Directory `fsync` | supported | not possible (`Access is denied`); skipped — NTFS journals the rename itself. File `fsync` opens read-write, as `FlushFileBuffers` requires. |
| Key-file permission check | rejects group/other bits | skipped — Windows has no such bits; access is the ACL inherited from the profile. |
| Default model cache | `~/.cache/cix/models` | `%LOCALAPPDATA%\cix\models` |
| Tunnels | managed download or `PATH` | `cloudflared`/`ngrok` must be on `PATH` (the managed download is Docker-only and has no Windows asset). |

### Starting it: `-env-file` and `-log-dir`

There is no shell wrapper on Windows. A scheduled task execs `cix-server.exe`
directly, so the task's process **is** the server (the pid the launcher tracks)
and `CTRL_BREAK` reaches the right process:

```
cix-server.exe -env-file C:\Users\me\.cix\server.env -log-dir C:\Users\me\.cix\logs
```

- **`-env-file <path>`** loads `KEY='value'` lines into the environment before
  configuration is read (the dialect the launcher writes: sorted, single-quoted,
  `'\''` for an embedded quote, `#` comments, optional `export `, UTF-8 BOM
  tolerated). **The real environment wins**: a variable that is already set —
  even empty — is left alone, so you can override one setting for one run
  without editing the file. It applies to the one-shot flags too, so
  `cix-server.exe -env-file … -reset-password you@example.com` finds the same
  database the server uses.
- **`-log-dir <dir>`** appends stdout to `<dir>\cix-server.log` (structured JSON
  logs) and stderr to `<dir>\cix-server.err` (startup banner, refusals, panics),
  creating the directory. Task Scheduler cannot redirect output; wrapping the
  server in `cmd /c … >log` would put a shell between the task and the server.
  On Windows the process-level standard handles are replaced too, so the
  runtime's crash output lands in the file. Same file names as the macOS agent.

Both work on every OS.

### Stopping it gracefully

`schtasks /end` and `Stop-Process` are `TerminateProcess` — no draining. The
graceful path is a console control event:

1. In the launcher process: **swallow the event first** — `signal.Notify(ch,
   os.Interrupt)` in Go, or a `SetConsoleCtrlHandler` routine that returns
   `TRUE`. (`SetConsoleCtrlHandler(NULL, TRUE)` is *not* enough: it ignores
   `CTRL_C` only, and `CTRL_BREAK` still terminates the sender.)
2. `FreeConsole()`; `AttachConsole(serverPid)`;
   `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, 0)`; `FreeConsole()`.

The event goes to every process on that console, which is the server and nothing
else. The server logs `shutdown signal received`, drains HTTP, stops the sidecar
and exits. `AttachConsole` **cannot cross Windows sessions**: it fails with
`ERROR_ACCESS_DENIED` from session 0 (SSH, services) against a task running in
the user's session. The launcher lives in the user's session, so it is fine;
anything driving this over SSH has to run inside an Interactive scheduled task
(`verify-runtime.ps1` does that for you).

## Building

```bash
SERVER_VERSION=0.15.1 CLI_VERSION=0.9.0 win/scripts/build-runtime.sh
```

Runs on macOS, Linux (that is what CI does) or Git Bash — everything is cross-
compiled or downloaded. Output is `win/dist/` (gitignored):
`cix-runtime-<ver>-windows-amd64.zip` and `checksums.txt` (`sha256sum` format).

| Variable | Meaning |
|---|---|
| `SERVER_VERSION` / `CLI_VERSION` | stamped into the binaries; default from the nearest `server/v*` / `cli/v*` tag |
| `OUT_DIR` | default `win/dist` |
| `LLAMA_CUDA` | CUDA generation of the upstream asset, default `13.3` |
| `LLAMA_STRICT` | default `1`: the two upstream archives must already have a pinned SHA256 in `server/scripts/llama-checksums.txt` |
| `SKIP_DASHBOARD_BUILD` | `1` reuses the dashboard already embedded |
| `SKIP_VERIFY` | `1` skips the structural check of the finished zip |

The script checks the zip's *structure* — one top directory, forward-slash
member names, no symlinks, PE headers, the required DLLs, and that the version
in each binary matches its label. What it cannot check without Windows and a GPU
is that it works; that is the next section.

The llama.cpp pin is `LLAMA_VERSION` in `server/Makefile`; the Windows archives
are `llama-<ver>-bin-win-cuda-<cuda>-x64.zip` and
`cudart-llama-bin-win-cuda-<cuda>-x64.zip`. Bumping the version means recording
both new rows in `server/scripts/llama-checksums.txt` (`make fetch-llama
OS=windows ARCH=amd64` does it on the first run; check them against the sha256
digests GitHub shows for the release assets). The Windows set stages in
`server/dist/llama-windows-amd64/`, separate from the macOS `dist/llama/`.

## Verifying a build

On a Windows machine with an NVIDIA GPU:

```powershell
win\scripts\verify-runtime.ps1 -Zip .\cix-runtime-0.15.1-windows-amd64.zip
```

It unpacks the zip into a temp directory and then, using the same mechanism as
the launcher (a Hidden, Interactive scheduled task whose action is
`cix-server.exe -env-file … -log-dir …`, with a throwaway data directory,
loopback only, port 21850):

1. checks `cix-server -v` / `cix --version` against `runtime.json`;
2. checks `llama-server --list-devices` lists a CUDA device;
3. boots the server, waits for `/health`, confirms it is **not** reachable on the
   LAN address;
4. waits for the sidecar and proves **by process id** that the GPU holds memory
   for it (`nvidia-smi --query-compute-apps`, falling back to the
   `GPU Process Memory` performance counter) — the GPU is verified, not assumed;
5. stops it with `AttachConsole` + `CTRL_BREAK`: the server must exit by itself,
   log `shutdown signal received`, take `llama-server` with it, and the task must
   return to `Ready`;
6. boots again and kills `cix-server` with `TerminateProcess`: `llama-server`
   must still die (Job Object).

It exits non-zero on any failed check. The first run downloads the embedding
model (~150 MB) into `%LOCALAPPDATA%\cix\models`. `-Direct` starts the server
with `Start-Process` instead of a task; `-Keep` keeps the working directory.

## CI

- `ci-server.yml` → `test-windows`: `go vet`, `go test`, `go build` on
  `windows-latest`.
- `release-server.yml` → `windows-runtime`: builds the zip on Ubuntu with
  `win/scripts/build-runtime.sh`; the `release` job attaches it, with the other
  assets, to the `server/v*` release and lists it in `checksums.txt`.
  Behavioural verification (`verify-runtime.ps1`) is manual — a hosted runner has
  neither a GPU nor an interactive session.
