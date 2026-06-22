//go:build !unix

package tswasm

import "github.com/tetratelabs/wazero/experimental"

// linearMemoryAllocator returns nil on non-unix platforms (no syscall.Mmap):
// initRuntime falls back to wazero's default Go-heap allocator. Functionally
// identical, just with the worse memory profile documented in mmap_unix.go.
func linearMemoryAllocator() experimental.MemoryAllocator { return nil }
