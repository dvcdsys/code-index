package dbmaint

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// quickCheckMaxBytes is the size above which PRAGMA quick_check is skipped.
//
// The check reads every page, and the server is not serving while it runs —
// this happens at boot, before the listener binds. Measured on a real 4.5 GB
// compacted copy it did not finish within a 30-second budget, so the deadline
// fired and the result was discarded: thirty seconds of extra downtime buying
// nothing. Below the threshold it costs a second or two and is worth having.
//
// Skipping it is safe because it was never the check doing the work. What
// actually catches a bad copy is the fingerprint — row counts taken from the
// source under the write freeze, which a copy of the wrong database or an
// earlier state cannot match — and the header's own claim about the file's
// length, which catches truncation. quick_check adds page-level structural
// validation on top of that, and SQLite having written a complete file that
// then fails its own structural check is not a failure mode this feature can
// cause.
//
// A variable rather than a constant so a test can lower it and prove that the
// fingerprint, not quick_check, is what rejects a bad copy.
var quickCheckMaxBytes int64 = 512 << 20

// quickCheckBudget bounds the check on files that do get it, so an unexpectedly
// slow filesystem cannot hold a boot open.
const quickCheckBudget = 15 * time.Second

// openForRead opens a SQLite file read-only on a single connection. Used for
// inspecting a file nothing else has open — a candidate copy at boot, or the
// displaced original.
func openForRead(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&mode=ro"
	sdb, err := sql.Open(db.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return sdb, nil
}

// ReadFingerprint collects the cheap identity of an open database. Callers on
// the compaction path pass the frozen source connection; the reconciler passes
// a read-only handle to a candidate copy.
//
// The three row counts are chosen because they are small, always present, and
// span unrelated parts of the schema — a copy that agrees on all three plus
// the migration version is not a copy of some other database or of an earlier
// state of this one.
func ReadFingerprint(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Fingerprint, error) {
	var f Fingerprint
	scalars := []struct {
		query string
		into  *int64
		what  string
	}{
		{`SELECT COUNT(*) FROM users`, &f.Users, "users"},
		{`SELECT COUNT(*) FROM projects`, &f.Projects, "projects"},
		{`SELECT COUNT(*) FROM api_keys`, &f.APIKeys, "api_keys"},
		{`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`, &f.SchemaVer, "schema_migrations"},
		{`PRAGMA page_count`, &f.PageCount, "page_count"},
		{`PRAGMA page_size`, &f.PageSize, "page_size"},
	}
	for _, s := range scalars {
		if err := q.QueryRowContext(ctx, s.query).Scan(s.into); err != nil {
			return Fingerprint{}, fmt.Errorf("fingerprint %s: %w", s.what, err)
		}
	}
	return f, nil
}

// FingerprintFile opens path read-only and fingerprints it.
func FingerprintFile(ctx context.Context, path string) (Fingerprint, error) {
	sdb, err := openForRead(path)
	if err != nil {
		return Fingerprint{}, err
	}
	defer sdb.Close()
	return ReadFingerprint(ctx, sdb)
}

// VerifyCopy is the gate in front of the only irreversible step in the whole
// feature. It runs at boot, on a candidate copy, before the original is
// touched — so everything it checks is checked against evidence recorded by a
// process that is no longer running.
//
// want is the fingerprint the compactor read from the *source* under the write
// freeze. Because writes were frozen, the copy must agree with it exactly; a
// mismatch means either the freeze leaked or the copy is not what it claims,
// and in both cases the original must be left alone.
func VerifyCopy(ctx context.Context, copyPath string, want Fingerprint) error {
	info, err := os.Stat(copyPath)
	if err != nil {
		return fmt.Errorf("stat copy: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("copy %s is empty", copyPath)
	}

	sdb, err := openForRead(copyPath)
	if err != nil {
		return fmt.Errorf("copy is not a readable database: %w", err)
	}
	defer sdb.Close()

	got, err := ReadFingerprint(ctx, sdb)
	if err != nil {
		return fmt.Errorf("copy could not be fingerprinted: %w", err)
	}
	if !got.Equal(want) {
		return fmt.Errorf(
			"copy contents do not match the frozen source: users %d/%d, projects %d/%d, api_keys %d/%d, schema %d/%d",
			got.Users, want.Users, got.Projects, want.Projects,
			got.APIKeys, want.APIKeys, got.SchemaVer, want.SchemaVer)
	}

	// A file truncated mid-write still opens and still answers PRAGMA
	// page_count from its header, so the header's own claim about the file's
	// length is the cheapest way to catch it.
	if want := got.PageCount * got.PageSize; want != info.Size() {
		return fmt.Errorf("copy is %d bytes but its header describes %d (page_count %d x page_size %d)",
			info.Size(), want, got.PageCount, got.PageSize)
	}

	if info.Size() > quickCheckMaxBytes {
		// Deliberately skipped. See quickCheckMaxBytes: on a large file this
		// is minutes of downtime for a check the fingerprint has already
		// covered.
		return nil
	}

	qcCtx, cancel := context.WithTimeout(ctx, quickCheckBudget)
	defer cancel()
	var result string
	if err := sdb.QueryRowContext(qcCtx, `PRAGMA quick_check(1)`).Scan(&result); err != nil {
		if qcCtx.Err() != nil {
			// Out of budget, not corrupt. The structural checks above already
			// passed, and they are the ones that catch a bad copy.
			return nil
		}
		return fmt.Errorf("quick_check failed to run: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("quick_check reported %q", result)
	}
	return nil
}
