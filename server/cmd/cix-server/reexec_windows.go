//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// reexec starts a fresh copy of this process and exits.
//
// Windows has no exec(2): nothing replaces a process image in place, so the pid
// cannot be preserved the way it is on Unix. The ordering argument in
// reexec_unix.go still holds — run() has returned, so the listener, the
// database and both sidecars are already gone before the successor starts and
// it can bind the same port.
//
// The child inherits stdio and the environment, including anything -env-file
// loaded, so it comes up configured identically. It is started without waiting:
// under Task Scheduler the task stays "running" while any process it started is
// alive, so the supervisor still sees one continuous instance.
func reexec() {
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cix-server: could not restart to adopt the compacted database:", err)
		fmt.Fprintln(os.Stderr, "cix-server: start the server again to finish the operation")
		os.Exit(1)
	}
	os.Exit(0)
}
