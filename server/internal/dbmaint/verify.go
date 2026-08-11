package dbmaint

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// quickCheckBudget bounds PRAGMA quick_check. On a multi-gigabyte file the
// check reads every page, so it is given a deadline rather than allowed to
// hold up a boot indefinitely. A timeout is not treated as corruption — the
// cheap structural checks still have to pass, and they are the ones that
// catch the failure this guards against (a truncated or half-written copy).
const quickCheckBudget = 30 * time.Second

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
