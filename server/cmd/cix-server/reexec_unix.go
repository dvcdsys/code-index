//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// reexec replaces this process image with a fresh one, which is how a
// compacted database is adopted: the swap happens at boot, before anything
// opens the file, and the only way to get there is to start over.
//
// It must run from main(), after run() has returned and every deferred
// cleanup has unwound. That ordering is not stylistic. The listener has to be
// closed or the new image fails to bind; the embeddings sidecar has to be
// reaped or the new image collides with it on its port and leaves a zombie it
// has no handle for; the tunnel binary is the same. syscall.Exec replaces the
// image outright, so no deferred function runs after it — everything that
// needs to happen must already have happened.
//
// Exec rather than exit: the process keeps its pid, so a container keeps its
// PID 1 and no restart policy or supervisor is involved.
func reexec() {
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	// The one-shot flags (-v, -healthcheck, -reset-password) all return from
	// main before run() is reached, so a process that got here provably was
	// not started with one and re-running this argv cannot land in one.
	if err := syscall.Exec(bin, os.Args, os.Environ()); err != nil {
		// Exec only returns on failure, and by now the listener, the database
		// and both sidecars are gone. There is nothing left to continue with.
		// The compacted copy and its journal are on disk, so whatever starts
		// this server next completes the swap.
		fmt.Fprintln(os.Stderr, "cix-server: could not restart to adopt the compacted database:", err)
		fmt.Fprintln(os.Stderr, "cix-server: start the server again to finish the operation")
		os.Exit(1)
	}
}
