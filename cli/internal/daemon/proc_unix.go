//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so closing the terminal that
// launched it does not deliver SIGHUP.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminate asks the process to exit (SIGTERM).
func terminate(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}

// isProcessRunning probes with signal 0, which checks existence and
// permission without delivering anything.
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
