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

// An existing database is untouched: the mode is only ever set on a file this
// build is creating. An upgrade must not quietly change anybody's database.
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

// A database in full auto-vacuum must survive being opened by this build.
//
// This is the case that made the mode a one-shot on a new file rather than a
// DSN pragma on every connection. SQLite ignores `auto_vacuum` on a populated
// database only when honouring it would mean moving pages — going to or from
// `none`. Between `full` and `incremental` it applies immediately, so the DSN
// form converted a full database on nothing more than a restart, and the
// compaction that followed then recorded the converted mode as if it had
// always been there.
func TestAutoVacuum_FullDatabaseSurvivesBeingOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.db")

	full, err := sql.Open(db.DriverName,
		"file:"+path+"?_pragma=auto_vacuum(FULL)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open full: %v", err)
	}
	if _, err := full.Exec(`CREATE TABLE t(a TEXT); INSERT INTO t VALUES('x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	full.Close()
	if got := modeOf(t, path); got != AutoVacuumFull {
		t.Fatalf("fixture is in %q mode, want %q", got, AutoVacuumFull)
	}

	reopened, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened.Close()

	if got := modeOf(t, path); got != AutoVacuumFull {
		t.Errorf("opening a full auto-vacuum database changed it to %q; the reclaim mode is the admin's to set", got)
	}
}

// A copy preserves the source's mode unless a different one was asked for.
//
// The copy runs on a connection of its own, opened without any auto_vacuum
// pragma and told explicitly which mode to produce. Both halves matter: a
// connection carries a pending auto_vacuum change into any VACUUM INTO it runs,
// even on a database where setting it was otherwise a no-op, so a copy taken
// through a pool that had the pragma anywhere in its DSN would come out in that
// mode regardless of what was requested.
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
	if err := svc.copyInto(context.Background(), out, svc.currentMode(context.Background())); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := modeOf(t, out); got != AutoVacuumNone {
		t.Errorf("a copy that preserves the source mode is in %q mode; it must match the source (%q)",
			got, AutoVacuumNone)
	}

	// Asking for incremental switches the copy...
	out2 := filepath.Join(dir, "copy2.db")
	if err := svc.copyInto(context.Background(), out2, AutoVacuumIncremental); err != nil {
		t.Fatalf("copy with enable_incremental: %v", err)
	}
	if got := modeOf(t, out2); got != AutoVacuumIncremental {
		t.Errorf("a copy asked for incremental is in %q mode, want %q", got, AutoVacuumIncremental)
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
	if err := svc.copyInto(context.Background(), out, svc.currentMode(context.Background())); err != nil {
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

// The switch goes both ways. Turning incremental reclaim *off* costs the same
// rebuild as turning it on, and that is the admin's call to make — the cost is
// stated, not decided for them.
func TestCompact_CopyCanTurnIncrementalOff(t *testing.T) {
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
	if got := modeOf(t, path); got != AutoVacuumIncremental {
		t.Fatalf("fixture is not incremental: %q", got)
	}
	svc := New(Deps{DB: sdb, DBPath: path, Logger: slog.New(slog.DiscardHandler)})

	out := filepath.Join(dir, "copy.db")
	if err := svc.copyInto(context.Background(), out, AutoVacuumNone); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := modeOf(t, out); got != AutoVacuumNone {
		t.Errorf("copy mode = %q after asking for none; the toggle must work in both directions", got)
	}
}

// A database in FULL auto-vacuum is a mode this server never sets but may
// well open — the file could have come from anywhere. Compacting it must
// return its space without quietly changing a setting nobody asked about.
func TestCompact_CopyPreservesFullAutoVacuum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "full.db")

	full, err := sql.Open(db.DriverName,
		"file:"+path+"?_pragma=auto_vacuum(FULL)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open full: %v", err)
	}
	if _, err := full.Exec(`CREATE TABLE t(a TEXT); INSERT INTO t VALUES('x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	full.Close()
	if got := modeOf(t, path); got != AutoVacuumFull {
		t.Fatalf("fixture is in %q mode, want %q", got, AutoVacuumFull)
	}

	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer sdb.Close()
	svc := New(Deps{DB: sdb, DBPath: path, Logger: slog.New(slog.DiscardHandler)})

	out := filepath.Join(dir, "copy.db")
	if err := svc.copyInto(context.Background(), out, svc.currentMode(context.Background())); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := modeOf(t, out); got != AutoVacuumFull {
		t.Errorf("copy mode = %q; compacting a full auto-vacuum database demoted it", got)
	}
}

func TestSetAutoVacuum_RejectsAnUnknownMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()
	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: slog.New(slog.DiscardHandler),
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() {},
	})
	if _, _, err := svc.SetAutoVacuum(context.Background(), AutoVacuum("sideways")); err == nil {
		t.Fatal("an unknown reclaim mode was accepted")
	}
}

// Asking for the mode the database is already in must do nothing at all — no
// rebuild, no restart, no interruption. This is the case the whole endpoint
// hangs on: the toggle is a setting, and setting it to what it already is is
// not an operation.
func TestSetAutoVacuum_SameModeIsANoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	quiesced := false
	restarted := false
	svc := New(Deps{
		DB: sdb, DBPath: path, Logger: slog.New(slog.DiscardHandler),
		Quiesce:        func(context.Context) error { quiesced = true; return nil },
		RequestRestart: func() { restarted = true },
	})

	// A fresh database is incremental; ask for incremental.
	st, changed, err := svc.SetAutoVacuum(context.Background(), AutoVacuumIncremental)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if changed {
		t.Error("changed = true when the database was already in the requested mode")
	}
	if st.Phase != PhaseIdle {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseIdle)
	}
	if quiesced || restarted {
		t.Error("a no-op stopped background work or asked for a restart")
	}
	if exists(JournalPath(path)) {
		t.Error("a no-op wrote a journal entry")
	}
}
