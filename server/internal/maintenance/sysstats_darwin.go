//go:build darwin

package maintenance

import "syscall"

// processRSS has no cgo-free equivalent on darwin — current RSS lives behind
// mach's task_info. Report it as unavailable rather than substituting the peak
// (see processPeakRSS), because a number labelled "resident" that only ever
// goes up would make a successful clean look like it did nothing.
func processRSS() (int64, bool) { return 0, false }

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
