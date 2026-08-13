package dbmaint

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/dvcdsys/code-index/server/internal/maintenance"
)

// AutoVacuum is the reclaim mode recorded in the database file's header.
type AutoVacuum string

const (
	AutoVacuumNone        AutoVacuum = "none"
	AutoVacuumFull        AutoVacuum = "full"
	AutoVacuumIncremental AutoVacuum = "incremental"
)

func autoVacuumFromPragma(v int64) AutoVacuum {
	switch v {
	case 1:
		return AutoVacuumFull
	case 2:
		return AutoVacuumIncremental
	default:
		return AutoVacuumNone
	}
}

// Verdict is the advice rendered next to the numbers.
type Verdict string

const (
	VerdictOK          Verdict = "ok"
	VerdictRecommended Verdict = "recommended"
	VerdictUrgent      Verdict = "urgent"
)

// Advice thresholds.
//
// Both must be met before anything is recommended. A percentage alone nags on
// a small database where 40% of 12 MB is not worth a maintenance window; an
// absolute figure alone nags on a large one where 500 MB of slack is normal
// operating headroom.
// The recommend thresholds are the configurable ones; "urgent" is derived from
// them — 1.6× the percentage and 8× the size — so moving one moves both and the
// two verdicts cannot cross over.
const (
	recommendPercent = 25
	recommendBytes   = 256 << 20 // 256 MiB
)

// copyThroughputBytesPerSec seeds the duration estimate before a real
// compaction has been measured on this machine. 120 MB/s is the observed rate
// on a warm SSD (8.86 GB read + 4.53 GB written in 76 s). The first real run
// replaces this with its own measurement, recorded in the journal.
const copyThroughputBytesPerSec = 120 << 20

// diskHeadroom is the slack required on top of the expected copy size. The
// copy exists alongside the original for the whole operation.
const diskHeadroom = 1.1

// State of the database file, as reported by GET /api/v1/admin/database.
type Stats struct {
	Path      string `json:"path"`
	FileBytes int64  `json:"file_bytes"`
	WALBytes  int64  `json:"wal_bytes"`

	PageSize      int64 `json:"page_size"`
	PageCount     int64 `json:"page_count"`
	FreelistPages int64 `json:"freelist_pages"`

	ReclaimableBytes   int64   `json:"reclaimable_bytes"`
	ReclaimablePercent float64 `json:"reclaimable_percent"`

	AutoVacuum AutoVacuum `json:"auto_vacuum"`

	Verdict       Verdict `json:"verdict"`
	VerdictReason string  `json:"verdict_reason"`

	FreeDiskBytes     int64 `json:"free_disk_bytes"`
	RequiredDiskBytes int64 `json:"required_disk_bytes"`
	EstimatedSeconds  int64 `json:"estimated_seconds"`
}

// ReadStats collects everything the dashboard needs about the database file.
//
// Cheap by construction: page_count and freelist_count are header reads, not
// scans, so this is safe to poll and safe to call on every dashboard load.
func ReadStats(ctx context.Context, sdb *sql.DB, dbPath string, measuredThroughput int64, th Thresholds) (Stats, error) {
	out := Stats{Path: dbPath}

	pragmas := []struct {
		q    string
		into *int64
	}{
		{`PRAGMA page_size`, &out.PageSize},
		{`PRAGMA page_count`, &out.PageCount},
		{`PRAGMA freelist_count`, &out.FreelistPages},
	}
	for _, p := range pragmas {
		if err := sdb.QueryRowContext(ctx, p.q).Scan(p.into); err != nil {
			return Stats{}, fmt.Errorf("read %s: %w", p.q, err)
		}
	}
	var av int64
	if err := sdb.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&av); err != nil {
		return Stats{}, fmt.Errorf("read PRAGMA auto_vacuum: %w", err)
	}
	out.AutoVacuum = autoVacuumFromPragma(av)

	if info, err := os.Stat(dbPath); err == nil {
		out.FileBytes = info.Size()
	}
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		out.WALBytes = info.Size()
	}

	out.ReclaimableBytes = out.FreelistPages * out.PageSize
	if out.PageCount > 0 {
		out.ReclaimablePercent = float64(out.FreelistPages) / float64(out.PageCount) * 100
	}
	out.Verdict, out.VerdictReason = advise(out, th)

	// The copy is roughly the live pages; the freelist is what disappears.
	expectedCopy := (out.PageCount - out.FreelistPages) * out.PageSize
	out.RequiredDiskBytes = int64(float64(expectedCopy) * diskHeadroom)

	rate := measuredThroughput
	if rate <= 0 {
		rate = copyThroughputBytesPerSec
	}
	// The copy reads the whole file and writes the live part, so time tracks
	// the sum rather than either alone.
	out.EstimatedSeconds = (out.FileBytes + expectedCopy) / rate

	if free, _, ok := maintenance.DiskFree(dbPath); ok {
		out.FreeDiskBytes = free
	}
	return out, nil
}

// advise renders the verdict from the *resolved* thresholds, not from the
// constants, so an operator who moved them does not read one number on screen
// while the automation acts on another.
func advise(s Stats, th Thresholds) (Verdict, string) {
	urgentPercent, urgentBytes := th.MinFreePercent*8/5, th.MinFreeBytes*8
	switch {
	case s.ReclaimablePercent >= float64(urgentPercent) && s.ReclaimableBytes >= urgentBytes:
		return VerdictUrgent, fmt.Sprintf(
			"%.0f%% of the database file is empty space — compacting would return about %s to the filesystem.",
			s.ReclaimablePercent, humanBytes(s.ReclaimableBytes))
	case s.ReclaimablePercent >= float64(th.MinFreePercent) && s.ReclaimableBytes >= th.MinFreeBytes:
		return VerdictRecommended, fmt.Sprintf(
			"%.0f%% of the database file is empty space (%s). Compacting is worth doing when you can spare the window.",
			s.ReclaimablePercent, humanBytes(s.ReclaimableBytes))
	case s.ReclaimableBytes == 0:
		return VerdictOK, "The database has no wasted space."
	default:
		return VerdictOK, fmt.Sprintf(
			"%s of the database file is empty space, which is normal working headroom.",
			humanBytes(s.ReclaimableBytes))
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
