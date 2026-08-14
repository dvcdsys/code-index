//go:build linux

package maintenance

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processRSS reads the current resident set size from /proc/self/statm, whose
// second field is the resident page count. Linux is the deployment target
// (both the CPU and CUDA images), so this is the platform where the number
// actually gets looked at.
func processRSS() (int64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

// processPeakRSS returns the high-water mark. On Linux getrusage reports
// Maxrss in kilobytes.
func processPeakRSS() (int64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return int64(ru.Maxrss) * 1024, true
}

func diskStats(path string) (fsStats, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fsStats{}, false
	}
	bsize := int64(st.Bsize)
	return fsStats{
		TotalBytes: int64(st.Blocks) * bsize,
		FreeBytes:  int64(st.Bavail) * bsize,
	}, true
}
