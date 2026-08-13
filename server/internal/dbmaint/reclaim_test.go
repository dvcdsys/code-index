package dbmaint

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// openWithMode creates a database in a chosen auto-vacuum mode. The mode can
// only be set before any table exists, which is why this builds the DSN
// itself rather than going through db.Open.
func openWithMode(t *testing.T, path string, mode string) *sql.DB {
	t.Helper()
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if mode != "" {
		dsn += "&_pragma=auto_vacuum(" + mode + ")"
	}
	sdb, err := sql.Open(db.DriverName, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { sdb.Close() })
	if _, err := sdb.Exec(`CREATE TABLE blob_t(id INTEGER PRIMARY KEY, payload BLOB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return sdb
}

// fillAndChurn writes enough data to matter, then deletes most of it, leaving
// a freelist worth reclaiming.
func fillAndChurn(t *testing.T, sdb *sql.DB) {
	t.Helper()
	for range 10 {
		if _, err := sdb.Exec(`INSERT INTO blob_t(payload)
			SELECT randomblob(4096) FROM
			(WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i<500) SELECT i FROM c)`); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if _, err := sdb.Exec(`DELETE FROM blob_t WHERE id % 4 != 0`); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func pragmaInt(t *testing.T, sdb *sql.DB, q string) int64 {
	t.Helper()
	var v int64
	if err := sdb.QueryRow(q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// The point of the whole endpoint: the file on disk must actually get
// smaller. In WAL mode incremental_vacuum's page moves and truncation land in
// the log, so without the checkpoint that follows it the database file is
// unchanged and the dashboard reports a reclaim that visibly did nothing.
func TestReclaim_ShrinksTheFileOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "INCREMENTAL")
	fillAndChurn(t, sdb)

	// Fold the churn into the file first, so the "before" size is the real
	// on-disk size and not an artefact of a fat log.
	if _, err := Checkpoint(context.Background(), sdb, path); err != nil {
		t.Fatalf("baseline checkpoint: %v", err)
	}
	before := sizeOrZero(path)
	freeBefore := pragmaInt(t, sdb, `PRAGMA freelist_count`)
	if freeBefore == 0 {
		t.Fatal("no freelist to reclaim; the fixture is not exercising anything")
	}

	// A *small* reclaim on purpose. SQLite auto-checkpoints once the log
	// passes 1000 pages, so a large reclaim shrinks the file whether or not
	// we checkpoint ourselves — and a test that only exercised that case
	// would pass with the checkpoint removed. Under the threshold, the page
	// moves and the truncation sit in the log until something folds them in,
	// which is exactly the situation this endpoint has to handle.
	const smallReclaim = 50
	res, err := Reclaim(context.Background(), sdb, path, smallReclaim)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	after := sizeOrZero(path)

	if after >= before {
		t.Errorf("file did not shrink: %d -> %d bytes (freed %d pages / %d bytes)",
			before, after, res.PagesFreed, res.BytesFreed)
	}
	if res.PagesFreed <= 0 {
		t.Errorf("PagesFreed = %d, want > 0", res.PagesFreed)
	}
	if res.BytesFreed <= 0 {
		t.Errorf("BytesFreed = %d, want > 0", res.BytesFreed)
	}
	if res.FileBytes != after {
		t.Errorf("FileBytes = %d but the file is %d bytes", res.FileBytes, after)
	}
}

// The unbounded form drains the whole freelist.
func TestReclaim_UnboundedDrainsTheFreelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "INCREMENTAL")
	fillAndChurn(t, sdb)
	if _, err := Checkpoint(context.Background(), sdb, path); err != nil {
		t.Fatalf("baseline checkpoint: %v", err)
	}

	res, err := Reclaim(context.Background(), sdb, path, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if res.FreelistPages != 0 {
		t.Errorf("FreelistPages = %d after an unbounded reclaim, want 0", res.FreelistPages)
	}
	if res.PagesFreed <= 0 {
		t.Errorf("PagesFreed = %d, want > 0", res.PagesFreed)
	}
}

// A bounded reclaim must return only what it was asked for, so a very large
// freelist can be drained without holding the write lock for a long time.
func TestReclaim_RespectsThePageBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "INCREMENTAL")
	fillAndChurn(t, sdb)
	if _, err := Checkpoint(context.Background(), sdb, path); err != nil {
		t.Fatalf("baseline checkpoint: %v", err)
	}
	freeBefore := pragmaInt(t, sdb, `PRAGMA freelist_count`)

	const bound = 10
	res, err := Reclaim(context.Background(), sdb, path, bound)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if res.PagesFreed > bound {
		t.Errorf("freed %d pages, more than the requested bound of %d", res.PagesFreed, bound)
	}
	if res.FreelistPages == 0 && freeBefore > bound {
		t.Error("the whole freelist was drained despite a bound")
	}
}

// Incremental reclaim is meaningless on a database that cannot do it, and the
// caller needs to be told that specifically so the UI can offer compaction as
// the remedy rather than showing a generic failure.
func TestReclaim_RefusesWhenNotIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "") // default: none
	fillAndChurn(t, sdb)

	_, err := Reclaim(context.Background(), sdb, path, 0)
	if !errors.Is(err, ErrNotIncremental) {
		t.Fatalf("err = %v, want ErrNotIncremental", err)
	}
}

func TestCheckpoint_DrainsTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "")
	fillAndChurn(t, sdb)

	if sizeOrZero(path+"-wal") == 0 {
		t.Fatal("no write-ahead log to drain; the fixture is not exercising anything")
	}
	res, err := Checkpoint(context.Background(), sdb, path)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if res.Blocked {
		t.Fatal("checkpoint reported blocked with no concurrent reader")
	}
	if res.WALBytesAfter >= res.WALBytesBefore {
		t.Errorf("log did not shrink: %d -> %d", res.WALBytesBefore, res.WALBytesAfter)
	}
	if res.WALBytesAfter != 0 {
		t.Errorf("TRUNCATE left a %d-byte log behind", res.WALBytesAfter)
	}
	// PagesCheckpointed is deliberately not asserted to be positive: the
	// pragma reports the state after truncation, so a fully successful
	// TRUNCATE checkpoint reports zero pages remaining. The byte delta above
	// is what proves it worked.
}

// A reader holding a snapshot stops the log being truncated. That is a normal
// outcome, not a failure, and must be reported as such.
func TestCheckpoint_ReportsBlockedRatherThanFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "")
	fillAndChurn(t, sdb)

	ctx := context.Background()
	reader, err := sdb.Conn(ctx)
	if err != nil {
		t.Fatalf("reader conn: %v", err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatalf("begin read txn: %v", err)
	}
	var n int64
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM blob_t`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := Checkpoint(ctx, sdb, path)
	if err != nil {
		t.Fatalf("checkpoint returned an error instead of reporting blocked: %v", err)
	}
	if !res.Blocked {
		t.Error("checkpoint did not report being blocked by an open reader")
	}
	if _, err := reader.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestReadStats_ReportsWasteAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	sdb := openWithMode(t, path, "INCREMENTAL")
	fillAndChurn(t, sdb)
	if _, err := Checkpoint(context.Background(), sdb, path); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	st, err := ReadStats(context.Background(), sdb, path, 0, DefaultThresholds())
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}
	if st.AutoVacuum != AutoVacuumIncremental {
		t.Errorf("AutoVacuum = %q, want %q", st.AutoVacuum, AutoVacuumIncremental)
	}
	if st.PageSize == 0 || st.PageCount == 0 {
		t.Fatalf("page geometry not read: size=%d count=%d", st.PageSize, st.PageCount)
	}
	if st.FileBytes == 0 {
		t.Error("FileBytes is zero for a database that exists on disk")
	}
	if st.ReclaimableBytes != st.FreelistPages*st.PageSize {
		t.Errorf("ReclaimableBytes = %d, want freelist %d x page size %d",
			st.ReclaimableBytes, st.FreelistPages, st.PageSize)
	}
	if st.EstimatedSeconds < 0 {
		t.Errorf("EstimatedSeconds = %d", st.EstimatedSeconds)
	}
	if st.VerdictReason == "" {
		t.Error("no verdict reason to render")
	}
}

// Both thresholds have to be met. A percentage alone would nag on a tiny
// database; an absolute figure alone would nag on a large one.
func TestAdvise_RequiresBothThresholds(t *testing.T) {
	cases := []struct {
		name    string
		percent float64
		bytes   int64
		want    Verdict
	}{
		{"empty database", 0, 0, VerdictOK},
		{"high percentage of a tiny file", 90, 1 << 20, VerdictOK},
		{"large absolute waste in a much larger file", 5, 4 << 30, VerdictOK},
		{"both recommend thresholds met", 30, 512 << 20, VerdictRecommended},
		{"percentage urgent but bytes only recommend", 50, 512 << 20, VerdictRecommended},
		{"both urgent thresholds met", 48, 4 << 30, VerdictUrgent},
	}
	for _, c := range cases {
		got, reason := advise(Stats{ReclaimablePercent: c.percent, ReclaimableBytes: c.bytes}, DefaultThresholds())
		if got != c.want {
			t.Errorf("%s: verdict = %q, want %q", c.name, got, c.want)
		}
		if reason == "" {
			t.Errorf("%s: no reason given", c.name)
		}
	}
}

// The dev instance that motivated this feature: 8.86 GB, 48% freelist.
func TestAdvise_TheCaseThatPromptedThis(t *testing.T) {
	const pageSize = 4096
	s := Stats{
		PageSize:      pageSize,
		PageCount:     2161967,
		FreelistPages: 1037594,
	}
	s.ReclaimableBytes = s.FreelistPages * s.PageSize
	s.ReclaimablePercent = float64(s.FreelistPages) / float64(s.PageCount) * 100
	if got, _ := advise(s, DefaultThresholds()); got != VerdictUrgent {
		t.Errorf("verdict = %q for a database that is 48%% empty, want %q", got, VerdictUrgent)
	}
}
