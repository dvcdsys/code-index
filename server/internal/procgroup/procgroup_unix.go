//go:build !windows

// Package procgroup owns the lifecycle of child processes cix-server spawns
// (llama-server, cloudflared, ngrok) in a way that works on both Unix and
// Windows.
//
// On Unix each child leads its own process group, so one signal reaches the
// child and anything it forked. On Windows there are no process groups in that
// sense; instead every child is placed in a Job Object configured with
// KILL_ON_JOB_CLOSE, so the children die with cix-server even when it is
// terminated hard (see procgroup_windows.go).
package procgroup

import (
	"os/exec"
	"syscall"
)

// Prepare configures cmd before Start so the child leads its own process
// group.
func Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Adopt is called after a successful Start. A no-op on Unix.
func Adopt(cmd *exec.Cmd) error { return nil }

// groupID returns the process group to signal, falling back to the pid itself
// when the group cannot be looked up.
func groupID(cmd *exec.Cmd) int {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Pid
	}
	return pgid
}

// Terminate asks the child (and its group) to exit: SIGTERM. It returns the
// id that was signalled, for logging.
func Terminate(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	id := groupID(cmd)
	_ = syscall.Kill(-id, syscall.SIGTERM)
	return id
}

// Kill force-terminates the child and its group: SIGKILL.
func Kill(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	id := groupID(cmd)
	_ = syscall.Kill(-id, syscall.SIGKILL)
	return id
}
