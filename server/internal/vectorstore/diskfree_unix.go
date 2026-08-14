//go:build linux || darwin

package vectorstore

import "syscall"

// freeSpaceBytes reports the space available to an unprivileged writer on the
// filesystem holding path. Bavail (not Bfree) is the right field: the
// difference is the root-reserved margin the server can never use.
func freeSpaceBytes(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
