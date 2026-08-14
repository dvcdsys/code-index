//go:build !linux && !darwin

package vectorstore

// freeSpaceBytes is unavailable on platforms the server is not shipped on.
// The caller treats (0,false) as "skip the guard" rather than as "no space".
func freeSpaceBytes(string) (uint64, bool) { return 0, false }
