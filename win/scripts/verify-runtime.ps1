<#
.SYNOPSIS
  Behavioural check of a cix Windows runtime zip: unpack it as a stranger would
  and prove it runs, uses the GPU, stops cleanly and leaves nothing behind.

.DESCRIPTION
  build-runtime.sh can only check a zip's structure (it usually runs on a
  machine with no Windows and no GPU). This is the half that needs both:

    1. unzip into a throwaway directory
    2. cix-server.exe -v   must match runtime.json, cix.exe --version must run
    3. llama-server.exe --list-devices must list a CUDA device
    4. boot cix-server the way the desktop launcher does -- as a Task Scheduler
       task, Hidden, action = `cix-server.exe -env-file <f> -log-dir <d>`, no
       shell in between -- with a throwaway data dir, loopback only
    5. /health answers; the embedding sidecar comes up; the GPU holds the model
       (checked by process id, not assumed)
    6. stop it the way the launcher does: AttachConsole(pid) +
       GenerateConsoleCtrlEvent(CTRL_BREAK). The server must exit by itself,
       log its shutdown, and take llama-server with it
    7. boot again and kill cix-server HARD (TerminateProcess): llama-server must
       still die with it (Job Object), or it would keep holding the GPU

  Exits 0 only if every check passed. The first boot downloads the embedding
  model (~150 MB) into -ModelCache unless it is already there.

.PARAMETER Zip
  The runtime archive (cix-runtime-<ver>-windows-amd64.zip).
.PARAMETER Port
  Test port. Default 21850 -- not the launcher's 21847, so a real install can
  keep running alongside.
.PARAMETER Direct
  Start cix-server with Start-Process instead of a scheduled task.
.PARAMETER Keep
  Keep the working directory (logs, database) afterwards.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Zip,
    [int]$Port = 21850,
    [string]$Work = (Join-Path $env:TEMP ("cix-verify-" + [guid]::NewGuid().ToString('N').Substring(0, 8))),
    [string]$ModelCache = (Join-Path $env:LOCALAPPDATA 'cix\models'),
    [int]$StartupTimeoutSec = 600,
    [switch]$Direct,
    [switch]$Keep,
    # Internal: set on the relaunch described under "Session 0" below.
    [switch]$InSession
)

$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
$script:failures = New-Object System.Collections.Generic.List[string]

function Pass($m) { Write-Host "  PASS  $m" -ForegroundColor Green }
function Fail($m) { Write-Host "  FAIL  $m" -ForegroundColor Red; $script:failures.Add($m) }
function Check($ok, $m) { if ($ok) { Pass $m } else { Fail $m }; return [bool]$ok }
function Step($m) { Write-Host "`n== $m" -ForegroundColor Cyan }

# --- Session 0 ----------------------------------------------------------------
# Over SSH (and from services) this script runs in Windows session 0, while an
# Interactive scheduled task -- and so the server -- runs in the logged-on user's
# session. AttachConsole cannot cross sessions (ERROR_ACCESS_DENIED), so the
# graceful-stop check would fail for a reason the real launcher never meets: it
# lives in the user's session. So in session 0 the whole script re-runs itself
# as an Interactive task and relays its output and exit code.
if (-not $InSession -and -not $Direct -and (Get-Process -Id $PID).SessionId -eq 0) {
    $log = Join-Path $env:TEMP ('cix-verify-harness-' + [guid]::NewGuid().ToString('N').Substring(0, 8) + '.log')
    # The inner script goes in a file, not a -Command string: three levels of
    # quoting (ssh, the task action, powershell) is how this breaks.
    $inner = [IO.Path]::ChangeExtension($log, '.ps1')
    $keepArg = if ($Keep) { '-Keep' } else { '' }
    @(
        "& '$PSCommandPath' -Zip '$Zip' -Port $Port -ModelCache '$ModelCache' -StartupTimeoutSec $StartupTimeoutSec -InSession $keepArg *>&1 | Out-File -Encoding utf8 '$log'",
        'exit $LASTEXITCODE'
    ) | Set-Content -Path $inner -Encoding UTF8
    $a = "-NoProfile -ExecutionPolicy Bypass -File `"$inner`""
    $tn = 'cix-verify-harness-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
    $act = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $a
    $set = New-ScheduledTaskSettingsSet -Hidden -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::FromHours(1))
    $pri = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
    Register-ScheduledTask -TaskName $tn -Action $act -Settings $set -Principal $pri | Out-Null
    Write-Host "session 0 detected -- re-running inside the interactive session (task $tn)"
    Start-ScheduledTask -TaskName $tn
    Start-Sleep -Seconds 3
    while ((Get-ScheduledTask -TaskName $tn).State -eq 'Running') { Start-Sleep -Seconds 2 }
    $rc = (Get-ScheduledTaskInfo -TaskName $tn).LastTaskResult
    Unregister-ScheduledTask -TaskName $tn -Confirm:$false
    if (Test-Path $log) { Get-Content $log; Remove-Item $log -Force }
    Remove-Item $inner -Force -ErrorAction SilentlyContinue
    exit $rc
}

# --- helpers ----------------------------------------------------------------

# Processes whose executable lives under $dir -- how a run is told apart from
# any other cix-server on the machine.
function Get-ProcsUnder($name, $dir) {
    Get-Process -Name $name -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($dir, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    }
}

function Wait-Until([scriptblock]$cond, [int]$sec, [int]$every = 1) {
    $end = (Get-Date).AddSeconds($sec)
    while ((Get-Date) -lt $end) {
        if (& $cond) { return $true }
        Start-Sleep -Seconds $every
    }
    return [bool](& $cond)
}

# The graceful-stop path the launcher uses. Runs in a separate powershell so its
# FreeConsole/AttachConsole cannot disturb this session.
#
# The event goes to every process on the attached console -- the sender
# included -- so the sender must swallow it. SetConsoleCtrlHandler(NULL, TRUE)
# is NOT enough: that only disables CTRL_C, and CTRL_BREAK still terminates the
# process. A handler routine that returns TRUE does handle both. (In a Go
# launcher: signal.Notify(ch, os.Interrupt) before sending does the same job.)
function Send-CtrlBreak([int]$ProcessId) {
    $code = @"
using System; using System.Runtime.InteropServices;
public static class CixCtl {
  [DllImport("kernel32.dll", SetLastError=true)] public static extern bool FreeConsole();
  [DllImport("kernel32.dll", SetLastError=true)] public static extern bool AttachConsole(uint pid);
  public delegate bool Handler(uint ctrlType);
  static readonly Handler swallow = delegate(uint t) { return true; };
  [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetConsoleCtrlHandler(Handler h, bool add);
  [DllImport("kernel32.dll", SetLastError=true)] public static extern bool GenerateConsoleCtrlEvent(uint ev, uint grp);
  public static string Send(uint pid) {
    FreeConsole();
    if (!AttachConsole(pid)) return "AttachConsole failed: " + Marshal.GetLastWin32Error();
    SetConsoleCtrlHandler(swallow, true);
    bool ok = GenerateConsoleCtrlEvent(1 /*CTRL_BREAK_EVENT*/, 0);
    int err = Marshal.GetLastWin32Error();
    System.Threading.Thread.Sleep(300); // let our own handler see the event
    FreeConsole();
    return ok ? "sent" : "GenerateConsoleCtrlEvent failed: " + err;
  }
}
"@
    $helper = Join-Path $Work 'ctrlbreak.ps1'
    # The result goes to a file: after FreeConsole the helper's own stdout is not
    # reliably captured by the parent.
    $result = Join-Path $Work 'ctrlbreak.result'
    Remove-Item $result -ErrorAction SilentlyContinue
    "Add-Type -TypeDefinition @'`n$code`n'@`n[CixCtl]::Send($ProcessId) | Set-Content -Path '$result'" | Set-Content -Path $helper -Encoding UTF8
    & powershell -NoProfile -ExecutionPolicy Bypass -File $helper 2>&1 | Out-Null
    if (Test-Path $result) { return (Get-Content $result -Raw).Trim() }
    return 'helper produced no result'
}

# Does the GPU hold memory for this pid? Three witnesses, strongest first.
function Get-GpuEvidence([int]$ProcessId) {
    $smi = & nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader 2>$null
    foreach ($l in @($smi)) { if ($l -match "^\s*$ProcessId\s*,") { return "nvidia-smi compute-apps: $($l.Trim())" } }
    try {
        $s = (Get-Counter '\GPU Process Memory(*)\Dedicated Usage' -ErrorAction Stop).CounterSamples |
            Where-Object { $_.InstanceName -like "pid_${ProcessId}_*" -and $_.CookedValue -gt 100MB } |
            Sort-Object CookedValue -Descending | Select-Object -First 1
        if ($s) { return ("GPU Process Memory counter: {0} = {1:N0} MiB dedicated" -f $s.InstanceName, ($s.CookedValue / 1MB)) }
    } catch { }
    return $null
}

function Write-ServerEnv($path, $dataDir) {
    # The launcher's dialect: sorted KEY='value', single quotes, '\'' escaping.
    $vars = [ordered]@{
        CIX_API_KEY                  = 'verify-' + [guid]::NewGuid().ToString('N')
        CIX_BIND_ADDR                = '127.0.0.1'
        CIX_BOOTSTRAP_ADMIN_EMAIL    = 'verify@example.com'
        CIX_BOOTSTRAP_ADMIN_PASSWORD = 'verify-' + [guid]::NewGuid().ToString('N')
        CIX_DATA_DIR                 = $dataDir
        CIX_GGUF_CACHE_DIR           = $ModelCache
        CIX_PORT                     = "$Port"
    }
    $lines = $vars.GetEnumerator() | Sort-Object Name | ForEach-Object { "$($_.Name)='$($_.Value -replace "'", "'\''")'" }
    Set-Content -Path $path -Value (@('# verify-runtime.ps1') + $lines) -Encoding ASCII
    return $vars
}

$taskName = $null
function Start-Server($exe, $envFile, $logDir) {
    $args = "-env-file `"$envFile`" -log-dir `"$logDir`""
    if ($Direct) {
        Start-Process -FilePath $exe -ArgumentList $args -WindowStyle Hidden | Out-Null
        return
    }
    $script:taskName = 'cix-verify-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
    $action = New-ScheduledTaskAction -Execute $exe -Argument $args -WorkingDirectory (Split-Path $exe)
    $settings = New-ScheduledTaskSettingsSet -Hidden -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
    $principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
    Register-ScheduledTask -TaskName $script:taskName -Action $action -Settings $settings -Principal $principal | Out-Null
    Start-ScheduledTask -TaskName $script:taskName
}

function Remove-VerifyTask {
    if ($script:taskName) {
        Stop-ScheduledTask -TaskName $script:taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $script:taskName -Confirm:$false -ErrorAction SilentlyContinue
        $script:taskName = $null
    }
}

function Wait-Healthy {
    Wait-Until { try { (Invoke-WebRequest "http://127.0.0.1:$Port/health" -UseBasicParsing -TimeoutSec 3).StatusCode -eq 200 } catch { $false } } $StartupTimeoutSec 2
}

# --- main -------------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $Work | Out-Null
Write-Host "work dir: $Work"
try {
    Step '1. unpack'
    $x = Join-Path $Work 'rt'
    New-Item -ItemType Directory -Force -Path $x | Out-Null
    & tar -xf $Zip -C $x
    $root = Get-ChildItem $x -Directory
    if (-not (Check ($root.Count -eq 1 -and $root[0].Name -like 'cix-runtime-*') "one top-level directory cix-runtime-*")) { throw 'bad archive layout' }
    $rt = $root[0].FullName
    foreach ($f in 'cix-server.exe', 'cix.exe', 'runtime.json', 'llama\llama-server.exe', 'llama\ggml-cuda.dll', 'llama\cudart64_13.dll', 'llama\cublas64_13.dll', 'llama\cublasLt64_13.dll') {
        Check (Test-Path (Join-Path $rt $f)) "present: $f" | Out-Null
    }

    Step '2. binaries run'
    $info = Get-Content (Join-Path $rt 'runtime.json') -Raw | ConvertFrom-Json
    $sv = (& (Join-Path $rt 'cix-server.exe') -v 2>&1 | Out-String).Trim()
    Check ($sv -like "*$($info.server_version)*") "cix-server -v reports the manifest version: $sv" | Out-Null
    $cv = (& (Join-Path $rt 'cix.exe') --version 2>&1 | Out-String).Trim()
    Check ($cv -like "*$($info.cli_version)*") "cix --version reports the manifest version: $cv" | Out-Null
    $devs = (& (Join-Path $rt 'llama\llama-server.exe') --list-devices 2>&1 | Out-String)
    Check ($devs -match 'CUDA\d+:') "llama-server sees a CUDA device: $(($devs -split "`n" | Where-Object { $_ -match 'CUDA\d+:' } | Select-Object -First 1).Trim())" | Out-Null

    $serverExe = Join-Path $rt 'cix-server.exe'
    $envFile = Join-Path $Work 'server.env'
    $logDir = Join-Path $Work 'logs'
    $dataDir = Join-Path $Work 'data'
    $null = Write-ServerEnv $envFile $dataDir
    New-Item -ItemType Directory -Force -Path $ModelCache | Out-Null

    foreach ($round in 1, 2) {
        if ($round -eq 1) { Step "3. boot ($(if ($Direct) { 'Start-Process' } else { 'Task Scheduler, Hidden' })) and graceful stop" }
        else { Step '4. boot again and kill cix-server hard' }

        Start-Server $serverExe $envFile $logDir
        $up = Wait-Until { [bool](Get-ProcsUnder 'cix-server' $rt) } 30
        if (-not (Check $up 'cix-server.exe process started')) { throw 'server did not start' }
        $srv = Get-ProcsUnder 'cix-server' $rt | Select-Object -First 1
        Pass "pid $($srv.Id) is cix-server.exe from the runtime dir (the task's process, no shell wrapper)"

        $healthy = Wait-Healthy
        if (-not (Check $healthy "/health answers on 127.0.0.1:$Port")) {
            Write-Host (Get-Content (Join-Path $logDir 'cix-server.err') -Tail 30 -ErrorAction SilentlyContinue | Out-String)
            throw 'server never became healthy'
        }
        $lan = Test-NetConnection -ComputerName (Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notlike '127.*' -and $_.PrefixOrigin -ne 'WellKnown' } | Select-Object -First 1 -ExpandProperty IPAddress) -Port $Port -WarningAction SilentlyContinue -InformationLevel Quiet
        Check (-not $lan) "bound to loopback only (not reachable on the LAN address)" | Out-Null

        $llamaUp = Wait-Until { [bool](Get-ProcsUnder 'llama-server' $rt) } $StartupTimeoutSec 2
        if (-not (Check $llamaUp 'llama-server.exe sidecar is running')) { throw 'no sidecar' }
        $llama = Get-ProcsUnder 'llama-server' $rt | Select-Object -First 1
        Pass "sidecar pid $($llama.Id)"

        # The model is loaded onto the GPU just after the sidecar's port opens.
        $gpu = $null
        Wait-Until { $script:gpu = Get-GpuEvidence $llama.Id; [bool]$script:gpu } 120 2 | Out-Null
        if (-not (Check ([bool]$script:gpu) "GPU holds memory for the sidecar pid: $script:gpu")) {
            Write-Host (nvidia-smi | Out-String)
        }
        # Structured logs go to stdout (cix-server.log); the banner to stderr.
        $log = Get-Content (Join-Path $logDir 'cix-server.log') -Raw -ErrorAction SilentlyContinue
        Check ($log -match 'llama-server ready') "server log reports the sidecar ready" | Out-Null
        Check ((Test-Path (Join-Path $logDir 'cix-server.err')) -and (Test-Path (Join-Path $logDir 'cix-server.log'))) "-log-dir produced cix-server.log and cix-server.err" | Out-Null

        if ($round -eq 1) {
            $r = Send-CtrlBreak $srv.Id
            Check ($r -eq 'sent') "CTRL_BREAK delivered via AttachConsole+GenerateConsoleCtrlEvent ($r)" | Out-Null
            $gone = Wait-Until { -not (Get-Process -Id $srv.Id -ErrorAction SilentlyContinue) } 40
            Check $gone 'cix-server exited by itself after CTRL_BREAK' | Out-Null
            $sidecarGone = Wait-Until { -not (Get-Process -Id $llama.Id -ErrorAction SilentlyContinue) } 15
            Check $sidecarGone 'llama-server exited with it' | Out-Null
            $log = Get-Content (Join-Path $logDir 'cix-server.log') -Raw -ErrorAction SilentlyContinue
            Check ($log -match 'shutdown signal received') 'log says the shutdown was graceful ("shutdown signal received")' | Out-Null
            Check ((Get-ProcsUnder 'llama-server' $rt | Measure-Object).Count -eq 0) 'no orphan llama-server.exe' | Out-Null
            if ($gone -eq $false) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
            if (-not $Direct) {
                $ok = Wait-Until { (Get-ScheduledTask -TaskName $script:taskName).State -eq 'Ready' } 15
                Check $ok 'scheduled task returned to Ready' | Out-Null
            }
        } else {
            Stop-Process -Id $srv.Id -Force
            $sidecarGone = Wait-Until { -not (Get-Process -Id $llama.Id -ErrorAction SilentlyContinue) } 15
            Check $sidecarGone 'llama-server died with a hard-killed cix-server (Job Object kill-on-close)' | Out-Null
            Check ((Get-ProcsUnder 'llama-server' $rt | Measure-Object).Count -eq 0) 'no orphan llama-server.exe' | Out-Null
        }
        Remove-VerifyTask
    }
}
catch {
    Fail "aborted: $($_.Exception.Message)"
}
finally {
    Remove-VerifyTask
    # Never leave the GPU held by a failed run.
    foreach ($n in 'cix-server', 'llama-server') {
        Get-ProcsUnder $n $Work | Stop-Process -Force -ErrorAction SilentlyContinue
    }
    if ($Keep) { Write-Host "kept: $Work" }
    else { Start-Sleep -Seconds 1; Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue }
}

Write-Host ''
if ($script:failures.Count -eq 0) { Write-Host 'verify-runtime: ALL CHECKS PASSED' -ForegroundColor Green; exit 0 }
Write-Host "verify-runtime: $($script:failures.Count) FAILED" -ForegroundColor Red
$script:failures | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
exit 1
