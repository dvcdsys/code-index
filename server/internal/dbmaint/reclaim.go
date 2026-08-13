package dbmaint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// ErrNotIncremental is returned when a caller asks for incremental reclaim on
// a database whose header does not support it. The remedy is a compaction,
// which is the only way to change the mode of a populated database.
var ErrNotIncremental = errors.New("database is not in incremental auto-vacuum mode")

// CheckpointResult reports what a WAL checkpoint moved.
type CheckpointResult struct {
	// PagesCheckpointed is the third column of PRAGMA wal_checkpoint, and it
	// reports the state *after* the checkpoint ran. In TRUNCATE mode a
	// complete checkpoint leaves an empty log, so this is 0 on exactly the
	// runs that worked best — measured, not assumed. The byte figures below
	// are the ones worth showing a human.
	PagesCheckpointed int64 `json:"pages_checkpointed"`
	WALBytesBefore    int64 `json:"wal_bytes_before"`
	WALBytesAfter     int64 `json:"wal_bytes_after"`
	// Blocked is true when a reader prevented the log from being fully folded
	// in. Not an error: the checkpoint moved what it could, and the rest goes
	// on the next attempt.
	Blocked bool `json:"blocked"`
}

// Checkpoint folds the write-ahead log back into the database file.
//
// Cheap, needs no maintenance window, and only moves data that is already
// durable — the first thing to try when the -wal has grown large, and worth
// offering separately so nobody reaches for a compaction to fix it.
func Checkpoint(ctx context.Context, sdb *sql.DB, dbPath string) (CheckpointResult, error) {
	out := CheckpointResult{WALBytesBefore: sizeOrZero(dbPath + "-wal")}

	// wal_checkpoint returns a row — (busy, log, checkpointed) — so it has to
	// be queried, not executed. Exec would run it and silently discard the
	// only report of whether it actually worked.
	var busy, logPages, checkpointed int64
	if err := sdb.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logPages, &checkpointed); err != nil {
		return CheckpointResult{}, fmt.Errorf("wal_checkpoint: %w", err)
	}
	out.Blocked = busy != 0
	if checkpointed > 0 {
		out.PagesCheckpointed = checkpointed
	}
	out.WALBytesAfter = sizeOrZero(dbPath + "-wal")
	return out, nil
}

// ReclaimResult reports what incremental reclaim returned to the filesystem.
type ReclaimResult struct {
	PagesFreed    int64 `json:"pages_freed"`
	BytesFreed    int64 `json:"bytes_freed"`
	FileBytes     int64 `json:"file_bytes"`
	FreelistPages int64 `json:"freelist_pages"`
}

// Reclaim returns free pages to the filesystem in bounded chunks.
//
// maxPages of 0 drains the whole freelist. Bounding it keeps the write lock
// short on a database with a very large freelist — the pages are moved under
// an ordinary write transaction, not an exclusive lock, so this needs no
// maintenance window at all.
//
// The checkpoint afterwards is not optional. In WAL mode the page moves and
// the truncation both land in the log, so the database file does not shrink
// until the log is folded back in: without it this reports a reclaim that
// visibly did nothing.
func Reclaim(ctx context.Context, sdb *sql.DB, dbPath string, maxPages int64) (ReclaimResult, error) {
	var mode int64
	if err := sdb.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return ReclaimResult{}, fmt.Errorf("read PRAGMA auto_vacuum: %w", err)
	}
	if autoVacuumFromPragma(mode) != AutoVacuumIncremental {
		return ReclaimResult{}, ErrNotIncremental
	}

	var pageSize, freeBefore int64
	if err := sdb.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return ReclaimResult{}, fmt.Errorf("read PRAGMA page_size: %w", err)
	}
	if err := sdb.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freeBefore); err != nil {
		return ReclaimResult{}, fmt.Errorf("read PRAGMA freelist_count: %w", err)
	}

	stmt := `PRAGMA incremental_vacuum`
	if maxPages > 0 {
		stmt = fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, maxPages)
	}
	if _, err := sdb.ExecContext(ctx, stmt); err != nil {
		return ReclaimResult{}, fmt.Errorf("incremental_vacuum: %w", err)
	}
	if _, err := Checkpoint(ctx, sdb, dbPath); err != nil {
		return ReclaimResult{}, fmt.Errorf("checkpoint after incremental_vacuum: %w", err)
	}

	var freeAfter int64
	if err := sdb.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freeAfter); err != nil {
		return ReclaimResult{}, fmt.Errorf("read PRAGMA freelist_count after: %w", err)
	}

	out := ReclaimResult{
		PagesFreed:    freeBefore - freeAfter,
		FreelistPages: freeAfter,
		FileBytes:     sizeOrZero(dbPath),
	}
	if out.PagesFreed < 0 {
		// A concurrent writer freed pages of its own while we ran. Report
		// nothing rather than a negative.
		out.PagesFreed = 0
	}
	out.BytesFreed = out.PagesFreed * pageSize
	return out, nil
}

func sizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
