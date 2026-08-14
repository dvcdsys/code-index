package maintenance

import "runtime"

// MemorySnapshot is the process memory picture.
//
// RSS is the number that matters for this server: the SQLite-backed vector
// store keeps its page cache in modernc's own mmap arenas, which
// runtime.MemStats cannot see at all — a busy process can show a few MB of
// Go heap next to hundreds of MB resident. The heap figures are still
// reported for the Go side of the house (indexer batches, HTTP, chunker),
// but the OS view is the headline.
type MemorySnapshot struct {
	HeapAllocBytes    int64 `json:"heap_alloc_bytes"`
	HeapInuseBytes    int64 `json:"heap_inuse_bytes"`
	HeapIdleBytes     int64 `json:"heap_idle_bytes"`
	HeapReleasedBytes int64 `json:"heap_released_bytes"`
	SysBytes          int64 `json:"sys_bytes"`
	NumGC             int64 `json:"num_gc"`
	NumGoroutine      int   `json:"num_goroutine"`
	// RSSBytes is CURRENT resident set size. Omitted on platforms where it
	// cannot be read without cgo (darwin hides it behind mach's task_info).
	// Never report 0 for "unknown" — the dashboard shows "n/a" when the field
	// is absent, which is honest; a zero would read as "nothing resident".
	RSSBytes *int64 `json:"rss_bytes,omitempty"`
	// PeakRSSBytes is the high-water mark from getrusage. Available on both
	// linux and darwin, and it is the only resident-memory number macOS gives
	// us cheaply — but it only ever goes up, so it is reported as a separate,
	// clearly-labelled figure and never substituted for RSSBytes.
	PeakRSSBytes *int64 `json:"peak_rss_bytes,omitempty"`
}

// readMemory samples the runtime. ReadMemStats stops the world briefly, so
// this is fine for an admin endpoint but should not be put on a hot path.
func readMemory() MemorySnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out := MemorySnapshot{
		HeapAllocBytes:    int64(ms.HeapAlloc),
		HeapInuseBytes:    int64(ms.HeapInuse),
		HeapIdleBytes:     int64(ms.HeapIdle),
		HeapReleasedBytes: int64(ms.HeapReleased),
		SysBytes:          int64(ms.Sys),
		NumGC:             int64(ms.NumGC),
		NumGoroutine:      runtime.NumGoroutine(),
	}
	if rss, ok := processRSS(); ok {
		out.RSSBytes = &rss
	}
	if peak, ok := processPeakRSS(); ok {
		out.PeakRSSBytes = &peak
	}
	return out
}

// fsStats is filesystem capacity for the volume holding a path.
type fsStats struct {
	TotalBytes int64
	FreeBytes  int64
}

// DiskFree reports free and total bytes on the volume holding path, and
// whether the platform could answer at all.
//
// Exported for internal/dbmaint: compaction writes a full second copy of the
// database beside the original, so it has to refuse to start when the volume
// cannot hold both. Rather than add a fourth build-tagged file there, it
// borrows the three that already exist here.
func DiskFree(path string) (free, total int64, ok bool) {
	st, ok := diskStats(path)
	if !ok {
		return 0, 0, false
	}
	return st.FreeBytes, st.TotalBytes, true
}
