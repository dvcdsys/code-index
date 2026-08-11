package dbmaint

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func modeOf(t *testing.T, path string) AutoVacuum {
	t.Helper()
	sdb, err := openForRead(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer sdb.Close()
	var v int64
	if err := sdb.QueryRow(`PRAGMA auto_vacuum`).Scan(&v); err != nil {
		t.Fatalf("read auto_vacuum: %v", err)
	}
	return autoVacuumFromPragma(v)
}

// A database created by this build can reclaim its own space.
func TestAutoVacuum_FreshDatabaseIsIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sdb.Close()

	if got := modeOf(t, path); got != AutoVacuumIncremental {
		t.Errorf("fresh database mode = %q, want %q", got, AutoVacuumIncremental)
	}
}

// An existing database is untouched. The pragma is silently ignored on a file
// that already has tables, which is what makes it safe to apply to every
// connection — an upgrade must not quietly change anybody's database.
func TestAutoVacuum_ExistingDatabaseIsNotConverted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a database the way an older server would have: no auto_vacuum in
	// the DSN, so it lands in mode none.
	legacy, err := sql.Open(db.DriverName,
		"file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE t(a TEXT); INSERT INTO t VALUES('x')`); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	legacy.Close()
	if got := modeOf(t, path); got != AutoVacuumNone {
		t.Fatalf("fixture is not in none mode: %q", got)
	}

	// Reopen it the way this build does.
	reopened, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened.Close()

	if got := modeOf(t, path); got != AutoVacuumNone {
		t.Errorf("an existing database was converted to %q by being opened; upgrades must change nothing", got)
	}
}

// The one that would fail without a dedicated pool for the copy.
//
// modernc applies DSN pragmas to every connection, and a connection carries a
// pending auto_vacuum change into any VACUUM INTO it runs — even on a database
// where setting it was otherwise a no-op. Copying through the shared pool
// would therefore produce an incremental copy on every run regardless of what
// the admin asked for, silently converting a legacy database and making the
// request field decorative.
func TestCompact_CopyKeepsTheSourceModeUnlessAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	legacy, err := sql.Open(db.DriverName,
		"file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE t(a TEXT); INSERT INTO t VALUES('x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	legacy.Close()

	// A service whose *shared* pool is the one this build hands out — with
	// auto_vacuum(INCREMENTAL) in its DSN.
	shared, err := db.Open(path)
	if err != nil {
		t.Fatalf("open shared: %v", err)
	}
	defer shared.Close()
	svc := New(Deps{DB: shared, DBPath: path, Logger: slog.New(slog.DiscardHandler)})

	out := filepath.Join(dir, "copy.db")
	if err := svc.copyInto(context.Background(), out, false); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := modeOf(t, out); got != AutoVacuumNone {
		t.Errorf("a copy made without enable_incremental is in %q mode; it must match the source (%q)",
			got, AutoVacuumNone)
	}

	// And asking for it does switch the copy.
	out2 := filepath.Join(dir, "copy2.db")
	if err := svc.copyInto(context.Background(), out2, true); err != nil {
		t.Fatalf("copy with enable_incremental: %v", err)
	}
	if got := modeOf(t, out2); got != AutoVacuumIncremental {
		t.Errorf("a copy made with enable_incremental is in %q mode, want %q", got, AutoVacuumIncremental)
	}
	_ = os.Remove(out)
}

// A database already in incremental mode must not be dropped back to none by
// a compaction that simply did not ask to change anything.
func TestCompact_CopyPreservesIncrementalWithoutBeingAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE t(a TEXT); INSERT INTO t VALUES('x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := New(Deps{DB: sdb, DBPath: path, Logger: slog.New(slog.DiscardHandler)})

	out := filepath.Join(dir, "copy.db")
	if err := svc.copyInto(context.Background(), out, false); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := modeOf(t, out); got != AutoVacuumIncremental {
		t.Errorf("copy mode = %q; an incremental database must stay incremental", got)
	}
}

// Above the size threshold quick_check is skipped, and the copy is still
// rejected when it is not this database. This is the claim the skip rests on:
// the fingerprint does the work, quick_check is insurance on small files.
func TestVerifyCopy_RejectsAWrongCopyWithoutQuickCheck(t *testing.T) {
	orig := quickCheckMaxBytes
	quickCheckMaxBytes = 1 // every file is now "too large to check"
	t.Cleanup(func() { quickCheckMaxBytes = orig })

	dir := t.TempDir()
	mine := filepath.Join(dir, "mine.db")
	theirs := filepath.Join(dir, "theirs.db")
	makeDB(t, mine, 5)
	makeDB(t, theirs, 2)

	want := fingerprintOf(t, mine)
	if err := VerifyCopy(context.Background(), theirs, want); err == nil {
		t.Fatal("a copy of a different database passed verification with quick_check skipped")
	}
	// And the right one still passes.
	if err := VerifyCopy(context.Background(), mine, want); err != nil {
		t.Fatalf("the matching database failed verification: %v", err)
	}
}
