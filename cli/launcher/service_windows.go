package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Task Scheduler control for the cix-server agent — the Windows counterpart of
// launchd_darwin.go.
//
// A per-user scheduled task is the closest thing Windows has to a launch agent:
// it runs as the user, in their session, needs no administrator rights to
// register, survives the launcher quitting, and can fire at logon. A Windows
// Service would need elevation to install, which an unsigned installer must not
// ask for.
//
// The same lesson as launchd applies: trust nothing the tool says. schtasks
// returns before a task has actually changed state, so every answer here is
// re-derived from the system — "is it loaded" asks the scheduler, "is it
// running" asks the process table.

const (
	taskName = "cix-server"

	// Provenance marker written into the task description. Its absence is how a
	// task someone else created under our name is recognised.
	managedByMarker = "managed-by: cix launcher"
)

// serverProcName is the image the task runs.
const serverProcName = "cix-server.exe"

func runSchtasks(args ...string) (string, error) {
	return runHidden(20*time.Second, "schtasks.exe", args...)
}

// taskInfo asks the scheduler about our task once and remembers the answer for a
// moment. The poller wants four facts per tick — registered, ours, autostart,
// running — and the first three all come from the same `schtasks /Query /XML`;
// spawning it separately for each would cost a process launch apiece every five
// seconds, forever. Writes invalidate the cache so a change is seen at once.
var taskCache struct {
	mu     sync.Mutex
	at     time.Time
	exists bool
	xml    string
}

const taskCacheTTL = 3 * time.Second

func taskInfo() (exists bool, xml string) {
	taskCache.mu.Lock()
	defer taskCache.mu.Unlock()
	if time.Since(taskCache.at) < taskCacheTTL {
		return taskCache.exists, taskCache.xml
	}
	out, err := runSchtasks("/Query", "/TN", taskName, "/XML")
	taskCache.at = time.Now()
	taskCache.exists = err == nil
	// NULs stripped so the UTF-16 schtasks sometimes emits can be searched as text.
	taskCache.xml = strings.ReplaceAll(out, "\x00", "")
	if err != nil {
		taskCache.xml = ""
	}
	return taskCache.exists, taskCache.xml
}

func invalidateTaskInfo() {
	taskCache.mu.Lock()
	taskCache.at = time.Time{}
	taskCache.mu.Unlock()
}

// serviceLoaded reports whether the task is registered.
func serviceLoaded() bool {
	exists, _ := taskInfo()
	return exists
}

func taskXML() string {
	_, x := taskInfo()
	return x
}

// servicePID returns the pid of the server this app manages, or 0.
//
// The process table, not the task state: a task reports Running while its
// action runs, but only the process says whether it is OUR server. The match is
// by image path — a cix-server.exe under ~/.cix/runtime — so a development
// build running from a checkout is not mistaken for the installed one.
func servicePID() int {
	root, err := runtimeRoot()
	if err != nil {
		return 0
	}
	root = strings.ToLower(filepath.Clean(root)) + string(os.PathSeparator)

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return 0
	}
	for {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), serverProcName) {
			if p := processImagePath(pe.ProcessID); p != "" && strings.HasPrefix(strings.ToLower(p), root) {
				return int(pe.ProcessID)
			}
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			return 0
		}
	}
}

func processImagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// serviceBootout removes the task. A running server is not touched — deleting a
// task does not stop its process — which is why callers stop it first.
func serviceBootout() error {
	if !serviceLoaded() {
		return nil
	}
	defer invalidateTaskInfo()
	if out, err := runSchtasks("/Delete", "/TN", taskName, "/F"); err != nil {
		return fmt.Errorf("could not remove the scheduled task: %s", strings.TrimSpace(out))
	}
	return nil
}

// serviceBootstrap (re)registers the task from the current definition.
func serviceBootstrap() error { return writeServiceFiles(autostartEnabled()) }

// startServer brings the server up. The task is registered if it is not;
// `schtasks /Run` then starts it. A task that is already running is left alone
// (the definition says IgnoreNew), which is the right behaviour for a button
// that means "make it run".
func startServer() error {
	if !serviceLoaded() {
		if err := serviceBootstrap(); err != nil {
			return err
		}
	}
	if out, err := runSchtasks("/Run", "/TN", taskName); err != nil {
		return fmt.Errorf("could not start the scheduled task: %s", strings.TrimSpace(out))
	}
	return nil
}

// stopServer asks the server to shut down and waits for it.
//
// Graceful first: Windows has no SIGTERM, but cix-server handles Ctrl+Break the
// way it handles SIGTERM — it drains, stops its llama-server, and exits. Only if
// that does not work in time is the process terminated, and the server ties its
// children to a Job Object so even that leaves nothing behind.
func stopServer() error {
	pid := servicePID()
	if pid == 0 {
		return nil
	}
	if err := ctrlBreak(pid); err != nil {
		logf("could not send Ctrl+Break to the server (%v); terminating it", err)
	} else if waitProcessExit(pid, 30*time.Second) {
		return nil
	} else {
		logf("the server did not exit within 30s of Ctrl+Break; terminating it")
	}

	if err := terminateProcess(pid); err != nil {
		return fmt.Errorf("could not stop the server: %w", err)
	}
	waitProcessExit(pid, 10*time.Second)
	// The task still thinks its action is running until the scheduler notices.
	_, _ = runSchtasks("/End", "/TN", taskName)
	return nil
}

var (
	procFreeConsole         = kernel32.NewProc("FreeConsole")
	procAttachConsole       = kernel32.NewProc("AttachConsole")
	procGenerateConsoleCtrl = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const ctrlBreakEvent = 1

// ctrlBreak asks the server to shut down by delivering Ctrl+Break to its console.
//
// Windows only delivers a console control event to the processes attached to a
// console, so someone has to attach to the server's. That someone cannot be the
// launcher: the event goes to EVERY process on that console, the attacher
// included, and Ctrl+Break kills a process that does not handle it. Trying to
// shield the launcher does not work — SetConsoleCtrlHandler(NULL, TRUE) ignores
// Ctrl+C but not Ctrl+Break, and a Go signal.Notify handler did not hold either
// (the launcher died with the signal meant for the server).
//
// So the launcher starts a throwaway copy of itself (`-break <pid>`, see
// breakHelper) to take the hit. Whether that helper survives is irrelevant.
//
// AttachConsole cannot cross sessions: from session 0 (a service, an SSH
// session) it fails with access denied. The launcher runs in the user's session,
// which is where the server's task runs too.
func ctrlBreak(pid int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = hiddenCommand(ctx, exe, "-break", fmt.Sprint(pid)).Run()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch uint32(ee.ExitCode()) {
		case statusControlCExit:
			return nil // the helper took the event, as intended
		case exitAttachFailed:
			return fmt.Errorf("could not attach to the server's console")
		}
	}
	return err
}

const (
	statusControlCExit = 0xC000013A
	exitAttachFailed   = 2
)

// breakHelper is the body of `cix-launcher -break <pid>`: attach to the target's
// console, raise Ctrl+Break, exit. Expected to be killed by its own event.
func breakHelper(pid int) int {
	_, _, _ = procFreeConsole.Call()
	if r, _, _ := procAttachConsole.Call(uintptr(pid)); r == 0 {
		return exitAttachFailed
	}
	if r, _, _ := procGenerateConsoleCtrl.Call(ctrlBreakEvent, 0); r == 0 {
		return 1
	}
	time.Sleep(300 * time.Millisecond)
	_, _, _ = procFreeConsole.Call()
	return 0
}

func waitProcessExit(pid int, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // already gone
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

func terminateProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// writeServiceFiles (re)registers the scheduled task.
//
// Called on every launch. The task execs cix-server.exe through the `current`
// junction, so a runtime update moves where that points and the task does not
// change. cix-server reads its configuration from server.env itself
// (-env-file) — there is no wrapper script, which keeps the task's process the
// server's own process: its pid is the one the launcher watches and stops.
func writeServiceFiles(runAtLogon bool) error {
	server, err := runtimeServerPath()
	if err != nil {
		return err
	}
	envPath, err := serverEnvPath()
	if err != nil {
		return err
	}
	logs, err := logDir()
	if err != nil {
		return err
	}
	home, err := cixHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}

	xml, err := buildTaskXML(server, envPath, logs, home, runAtLogon)
	if err != nil {
		return err
	}

	// schtasks reads the definition from a file, and wants UTF-16 with a BOM.
	tmp, err := os.CreateTemp("", "cix-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encodeUTF16LE(xml)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	defer invalidateTaskInfo()
	if out, err := runSchtasks("/Create", "/TN", taskName, "/XML", tmp.Name(), "/F"); err != nil {
		return fmt.Errorf("could not register the scheduled task: %s", strings.TrimSpace(out))
	}
	invalidateTaskInfo()
	if !serviceLoaded() {
		return errors.New("the scheduled task was not registered")
	}
	return nil
}

func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(u)*2)
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func xmlEscape(s string) string {
	// Element text, not an attribute: quotes need no escaping, and the command
	// line quotes its own paths.
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// buildTaskXML renders the task definition. Split out so it is testable.
//
// Settings worth knowing about:
//   - Hidden, so a console-subsystem server gets no visible window.
//   - ExecutionTimeLimit PT0S: the default kills a task after 72 hours.
//   - MultipleInstancesPolicy IgnoreNew: Start on a running server is a no-op,
//     and two logons cannot give two servers fighting over one port.
//   - No restart-on-failure, matching macOS (KeepAlive=false): a crashed server
//     shows as stopped within five seconds, which is more honest than a loop.
func buildTaskXML(server, envPath, logDir, workDir string, runAtLogon bool) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	userID := xmlEscape(u.Username)

	triggers := ""
	if runAtLogon {
		triggers = fmt.Sprintf(`<LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId><Delay>PT15S</Delay></LogonTrigger>`, userID)
	}
	args := fmt.Sprintf(`-env-file "%s" -log-dir "%s"`, envPath, logDir)

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>cix-server (%s)</Description></RegistrationInfo>
  <Triggers>%s</Triggers>
  <Principals>
    <Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>%s</Command><Arguments>%s</Arguments><WorkingDirectory>%s</WorkingDirectory></Exec>
  </Actions>
</Task>
`, managedByMarker, triggers, userID, xmlEscape(server), xmlEscape(args), xmlEscape(workDir)), nil
}

// autostartEnabled reports whether the task fires at logon, read back from the
// registered definition rather than remembered — the same reason the macOS
// version reads RunAtLoad out of the plist.
func autostartEnabled() bool {
	x := taskXML()
	return strings.Contains(x, "<LogonTrigger>") && !strings.Contains(x, "<LogonTrigger><Enabled>false")
}

// setAutostart changes whether the task fires at logon. Unlike launchd, the
// scheduler re-reads a definition when it is re-registered and a running task
// is unaffected, so no restart cycle is needed.
func setAutostart(enabled bool) error { return writeServiceFiles(enabled) }

// foreignAgent reports whether a task under our name exists but was not created
// by this app.
func foreignAgent() bool {
	if !serviceLoaded() {
		return false
	}
	return !strings.Contains(taskXML(), managedByMarker)
}
