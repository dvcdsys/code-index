package storage

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"

	_ "modernc.org/sqlite"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeUnifiedDB creates a real system DB at path via db.OpenWith (so it
// has schema_migrations + users), then writes a sentinel row so we can
// prove the exact file survived an adoption.
func makeUnifiedDB(t *testing.T, path, sentinel string) {
	t.Helper()
	d, err := db.OpenWith(db.OpenOptions{Path: path})
	if err != nil {
		t.Fatalf("OpenWith(%s): %v", path, err)
	}
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS sentinel (v TEXT)`); err != nil {
		t.Fatalf("create sentinel: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO sentinel (v) VALUES (?)`, sentinel); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// makeFossilDB creates a pre-auth fossil: a bare DB with only a projects
// table, no users / schema_migrations.
func makeFossilDB(t *testing.T, path string) {
	t.Helper()
	sdb, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open fossil: %v", err)
	}
	if _, err := sdb.Exec(`CREATE TABLE projects (host_path TEXT)`); err != nil {
		t.Fatalf("create fossil projects: %v", err)
	}
	if err := sdb.Close(); err != nil {
		t.Fatalf("close fossil: %v", err)
	}
}

func readSentinel(t *testing.T, path string) string {
	t.Helper()
	sdb, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer sdb.Close()
	var v string
	if err := sdb.QueryRow(`SELECT v FROM sentinel LIMIT 1`).Scan(&v); err != nil {
		t.Fatalf("read sentinel from %s: %v", path, err)
	}
	return v
}

func TestAdoptLegacyModelDB_AdoptIntoAbsentTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	legacy := filepath.Join(dir, "projects_model.db")
	makeUnifiedDB(t, legacy, "hello")

	if err := AdoptLegacyModelDB(target, legacy, quietLogger()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if fileExists(legacy) {
		t.Errorf("legacy should have been renamed away")
	}
	if !fileExists(target) {
		t.Fatalf("target should exist after adoption")
	}
	if got := readSentinel(t, target); got != "hello" {
		t.Errorf("sentinel = %q, want hello (wrong file adopted?)", got)
	}
}

func TestAdoptLegacyModelDB_NoopWhenTargetIsUnified(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	legacy := filepath.Join(dir, "projects_model.db")
	makeUnifiedDB(t, target, "target-data")
	makeUnifiedDB(t, legacy, "legacy-data")

	if err := AdoptLegacyModelDB(target, legacy, quietLogger()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	// Target untouched, legacy left in place (stale).
	if got := readSentinel(t, target); got != "target-data" {
		t.Errorf("target sentinel = %q, want target-data (must not be overwritten)", got)
	}
	if !fileExists(legacy) {
		t.Errorf("legacy should be left in place when target is already unified")
	}
}

func TestAdoptLegacyModelDB_FossilMovedAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	legacy := filepath.Join(dir, "projects_model.db")
	makeFossilDB(t, target)
	makeUnifiedDB(t, legacy, "real")

	if err := AdoptLegacyModelDB(target, legacy, quietLogger()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := readSentinel(t, target); got != "real" {
		t.Errorf("target sentinel = %q, want real (legacy not adopted over fossil)", got)
	}
	// Fossil moved aside to a *.pre-unify-* file.
	matches, _ := filepath.Glob(target + ".pre-unify-*")
	if len(matches) == 0 {
		t.Errorf("expected fossil moved aside to %s.pre-unify-*", target)
	}
	if fileExists(legacy) {
		t.Errorf("legacy should have been adopted (renamed away)")
	}
}

func TestAdoptLegacyModelDB_Idempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	legacy := filepath.Join(dir, "projects_model.db")
	makeUnifiedDB(t, legacy, "once")

	for i := 0; i < 2; i++ {
		if err := AdoptLegacyModelDB(target, legacy, quietLogger()); err != nil {
			t.Fatalf("adopt run %d: %v", i, err)
		}
	}
	if got := readSentinel(t, target); got != "once" {
		t.Errorf("sentinel = %q, want once", got)
	}
}

func TestAdoptLegacyModelDB_LegacyEqualsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	makeUnifiedDB(t, target, "same")
	if err := AdoptLegacyModelDB(target, target, quietLogger()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := readSentinel(t, target); got != "same" {
		t.Errorf("sentinel = %q, want same", got)
	}
}

// TestAdoptLegacyModelDB_WALDrainedAndSidecarsGone writes rows under WAL,
// adopts, and asserts the data survives the checkpoint+rename and the
// legacy -wal/-shm sidecars are removed.
func TestAdoptLegacyModelDB_WALDrainedAndSidecarsGone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "projects.db")
	legacy := filepath.Join(dir, "projects_model.db")
	makeUnifiedDB(t, legacy, "wal-data")

	if err := AdoptLegacyModelDB(target, legacy, quietLogger()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := readSentinel(t, target); got != "wal-data" {
		t.Errorf("sentinel = %q, want wal-data", got)
	}
	if fileExists(legacy + "-wal") {
		t.Errorf("legacy -wal sidecar should be gone")
	}
	if fileExists(legacy + "-shm") {
		t.Errorf("legacy -shm sidecar should be gone")
	}
}
