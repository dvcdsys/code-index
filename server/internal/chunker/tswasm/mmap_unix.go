//go:build unix

package tswasm

import (
	"syscall"

	"github.com/tetratelabs/wazero/experimental"
)

// mmapMemory backs a wasm linear memory with an anonymous mmap mapping instead
// of wazero's default Go-heap []byte. Two wins, both measured on the chunker
// churn workload (see memprofile_test.go):
//
//   - Grow is free. The full max region (MemLimitPages) is reserved up-front —
//     virtual only; the OS commits pages on first touch — so Reallocate just
//     re-slices. The default allocator grows by append-realloc-copy, which
//     turned ONE big parse into 100s of MB of transient Go-heap garbage and
//     ballooned heapSys to ~1.5 GiB.
//   - Free munmaps. A closed/recycled instance's memory leaves RSS immediately,
//     instead of lingering in the Go heap until the scavenger returns it.
//
// Not safe for wasm shared memories (the backing address never moves here, but
// nothing in this package uses threads/shared memory anyway).
type mmapMemory struct {
	// full is the original full-length mapping. Munmap must be called with a
	// slice whose len equals the mapping size — Reallocate hands out shorter
	// re-slices, so the original is kept separately.
	full []byte
}

func (m *mmapMemory) Reallocate(size uint64) []byte {
	if m.full == nil || size > uint64(len(m.full)) {
		// Over max → wasm memory.grow fails → guest malloc returns NULL →
		// tree-sitter aborts → contained trap → sliding-window fallback.
		return nil
	}
	return m.full[:size:len(m.full)]
}

func (m *mmapMemory) Free() {
	if m.full != nil {
		_ = syscall.Munmap(m.full)
		m.full = nil
	}
}

// heapMemory is the fallback when mmap fails (ENOMEM, exotic platforms): a
// fixed-capacity Go-heap buffer. cap is reserved once at max (virtual for large
// allocations — the Go runtime mmaps big spans and commits on touch), so grow
// is still a re-slice with no realloc-copy; the OS-return-on-Free win is lost.
type heapMemory struct {
	buf []byte // len = current size, cap = max, never reallocated
}

func (h *heapMemory) Reallocate(size uint64) []byte {
	if size > uint64(cap(h.buf)) {
		return nil
	}
	h.buf = h.buf[:size]
	return h.buf
}

func (h *heapMemory) Free() { h.buf = nil }

// linearMemoryAllocator returns the mmap-backed allocator. A nil return (only
// on non-unix builds, see mmap_other.go) makes initRuntime fall back to
// wazero's default Go-heap allocator.
func linearMemoryAllocator() experimental.MemoryAllocator {
	return experimental.MemoryAllocatorFunc(func(_, max uint64) experimental.LinearMemory {
		buf, err := syscall.Mmap(-1, 0, int(max),
			syscall.PROT_READ|syscall.PROT_WRITE,
			syscall.MAP_ANON|syscall.MAP_PRIVATE)
		if err != nil {
			return &heapMemory{buf: make([]byte, 0, max)}
		}
		return &mmapMemory{full: buf}
	})
}
