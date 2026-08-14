//go:build darwin

package maintenance

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processRSS asks ps(1) for the current resident set size. There is no
// cgo-free mach API for it (task_info needs a mach port), but ps queries the
// kernel for us and this only runs on an admin-endpoint hit, never on a hot
// path. Caveat inherited from the platform: darwin releases Go-heap pages with
// MADV_FREE, which ps still counts until memory pressure — the store's own
// memory (munmap-backed SQLite arenas) does drop immediately.
func processRSS() (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, false
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || kb <= 0 {
		return 0, false
	}
	return kb * 1024, true
}

// processPeakRSS returns the high-water mark. On darwin getrusage reports
// Maxrss in bytes, unlike Linux.
func processPeakRSS() (int64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return int64(ru.Maxrss), true
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
