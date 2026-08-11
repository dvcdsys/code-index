//go:build !linux && !darwin

package maintenance

// Fallbacks for platforms the server is not shipped on. Every caller treats
// (0,false) as "omit the field", so an unsupported platform degrades to
// showing Go heap numbers only rather than reporting a confident zero.

func processRSS() (int64, bool) { return 0, false }

func processPeakRSS() (int64, bool) { return 0, false }

func diskStats(string) (fsStats, bool) { return fsStats{}, false }
