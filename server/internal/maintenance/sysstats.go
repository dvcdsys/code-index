package maintenance

import "runtime"

// MemorySnapshot is the process memory picture.
//
// HeapAlloc is the number that matters for this server: the vector store is an
// in-memory database, so live heap tracks the size of the loaded index almost
// one-for-one. Sys and RSS run higher because the Go runtime holds freed spans
// before returning them to the OS — which is exactly why Clean calls
// debug.FreeOSMemory() after dropping collections.
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
