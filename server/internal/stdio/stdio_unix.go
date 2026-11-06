//go:build !windows

package stdio

import "os"

// installStd is a no-op on Unix beyond what Redirect does: every child cix-server
// starts has its streams wired explicitly (exec.Cmd.Stdout/Stderr), so nothing
// relies on descriptors 1 and 2 themselves being the files.
func installStd(out, errf *os.File) (undo func(), err error) {
	return func() {}, nil
}
