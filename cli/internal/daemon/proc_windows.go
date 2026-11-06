//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the child with no console and in its own process group, so it
// survives the terminal that launched it and a Ctrl+C aimed at that terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

// terminate stops the process. Windows has no SIGTERM to deliver to a detached
// process; the watcher keeps no state that needs a graceful path (its debounce
// queue is re-derived from the next sync), so this is TerminateProcess.
func terminate(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

// isProcessRunning reports whether pid is a live process. os.Process.Signal(0)
// is not supported on Windows, so ask the kernel for the exit code instead:
// STILL_ACTIVE means it has not exited.
func isProcessRunning(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
