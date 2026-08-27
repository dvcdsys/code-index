package searchstats

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// How does the size of the statistics database affect the response?
//
// Two paths, and only one of them can degrade:
//
//   - RECORDING is a mutex and a map write. It never touches the database, so
//     no size can reach it. Measured here anyway, because "it cannot" is a
//     claim and this file exists to replace claims with numbers.
//   - READING is the dashboard's aggregate. For a regular user it is scoped to
//     their own projects; for an ADMIN the scope is every project on the
//     server, so the query aggregates the whole table. That is the worst case
//     and the one this measures.
//
// Run with:
//
//	go test -run TestScaling -v -timeout 30m ./internal/searchstats/
//
// Skipped by default: populating a million rows takes minutes and this is a
// measurement, not an assertion.

// seedAtScale writes `projects` x `filesPerProject` rows into both totals
// tables and a week of buckets, using direct inserts rather than the recorder —
// the recorder's own cost is measured separately and would dominate here.
func seedAtScale(tb testing.TB, s *Store, projects, filesPerProject int, now time.Time) {
	tb.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	insProject, _ := tx.PrepareContext(ctx, `INSERT INTO projects_seen (project_path) VALUES (?)`)
	insTotal, _ := tx.PrepareContext(ctx,
		`INSERT INTO search_totals (project_id, kind, queries, results, last_seen) VALUES (?, ?, ?, ?, ?)`)
	insFileTotal, _ := tx.PrepareContext(ctx,
		`INSERT INTO search_file_totals (project_id, kind, file_path, hits, last_seen) VALUES (?, ?, ?, ?, ?)`)
	insBucket, _ := tx.PrepareContext(ctx,
		`INSERT INTO search_buckets (bucket, project_id, kind, queries, results) VALUES (?, ?, ?, ?, ?)`)
	insFileBucket, _ := tx.PrepareContext(ctx,
		`INSERT INTO search_file_buckets (bucket, project_id, kind, file_path, hits) VALUES (?, ?, ?, ?, ?)`)
	defer func() {
		for _, st := range []*sql.Stmt{insProject, insTotal, insFileTotal, insBucket, insFileBucket} {
			_ = st.Close()
		}
	}()

	// A week of 30-minute buckets is the retained window; spreading each
	// project's recent files over a handful of them mirrors real traffic
	// without writing 336 buckets per file.
	bucketsPerProject := 8
	base := BucketOf(now)

	for p := 0; p < projects; p++ {
		res, err := insProject.ExecContext(ctx, fmt.Sprintf("github.com/org/repo%04d@main", p))
		if err != nil {
			tb.Fatalf("insert project: %v", err)
		}
		id, _ := res.LastInsertId()

		if _, err := insTotal.ExecContext(ctx, id, KindSemantic, filesPerProject*3, filesPerProject*7, now.Unix()); err != nil {
			tb.Fatalf("insert total: %v", err)
		}
		for f := 0; f < filesPerProject; f++ {
			path := fmt.Sprintf("internal/pkg%02d/module%05d.go", f%64, f)
			if _, err := insFileTotal.ExecContext(ctx, id, KindSemantic, path, (f%17)+1, now.Unix()); err != nil {
				tb.Fatalf("insert file total: %v", err)
			}
		}
		for b := 0; b < bucketsPerProject; b++ {
			bucket := base - int64(b)*BucketSeconds
			if _, err := insBucket.ExecContext(ctx, bucket, id, KindSemantic, 5, 20); err != nil {
				tb.Fatalf("insert bucket: %v", err)
			}
			// A tenth of the project's files appear in each retained bucket.
			for f := 0; f < filesPerProject/10; f++ {
				path := fmt.Sprintf("internal/pkg%02d/module%05d.go", f%64, f)
				if _, err := insFileBucket.ExecContext(ctx, bucket, id, KindSemantic, path, (f%5)+1); err != nil {
					tb.Fatalf("insert file bucket: %v", err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit: %v", err)
	}
}

func countRows(tb testing.TB, s *Store, table string) int64 {
	tb.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		tb.Fatalf("count %s: %v", table, err)
	}
	return n
}

func median(ds []time.Duration) time.Duration {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j] < ds[j-1]; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
	return ds[len(ds)/2]
}

func timeIt(reps int, fn func()) time.Duration {
	ds := make([]time.Duration, reps)
	for i := range ds {
		start := time.Now()
		fn()
		ds[i] = time.Since(start)
	}
	return median(ds)
}

func TestScalingWithDatabaseSize(t *testing.T) {
	if os.Getenv("CIX_SCALE_TEST") == "" {
		t.Skip("set CIX_SCALE_TEST=1 to run — populates up to a million rows")
	}
	now := time.Date(2026, 8, 27, 14, 43, 17, 0, time.UTC)

	cases := []struct {
		label           string
		projects        int
		filesPerProject int
	}{
		{"small     ", 10, 200},
		{"medium    ", 50, 1000},
		{"large     ", 100, 2500},
		{"very large", 200, 5000},
	}

	fmt.Printf("\n%-11s %9s %9s %11s %11s %11s %11s %11s %11s\n",
		"scale", "projects", "file rows", "db size",
		"read(all)", "read(win)", "read(bySort)", "flush", "record")
	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, DBFileName)
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		seedAtScale(t, s, c.projects, c.filesPerProject, now)

		scope := make([]string, c.projects)
		for i := range scope {
			scope[i] = fmt.Sprintf("github.com/org/repo%04d@main", i)
		}

		ctx := context.Background()
		// The admin's view: every project in scope, sorted by an aggregate,
		// first page. This is what the dashboard issues, on a 30s refresh.
		readAll := timeIt(5, func() {
			if _, err := s.ProjectStatsPage(ctx, Query{
				ProjectPaths: scope, TopFiles: 5, Sort: SortQueries, Desc: true, Limit: 25,
			}, now); err != nil {
				t.Fatalf("read: %v", err)
			}
		})
		readWindow := timeIt(5, func() {
			if _, err := s.ProjectStatsPage(ctx, Query{
				ProjectPaths: scope, TopFiles: 5, Sort: SortQueries, Desc: true, Limit: 25,
				Window: WindowRetention,
			}, now); err != nil {
				t.Fatalf("read windowed: %v", err)
			}
		})

		// Sorting by a file column is one click away in the dashboard — the
		// "Top files in results" and "Files" headers are both sortable, and the
		// filter bar carries a range on the top file. That request cannot be
		// served from the page alone: it needs every visible project's file
		// aggregate before it can decide which projects the page holds. Measured
		// rather than left for the reader to extrapolate, because it is the
		// expensive path this optimisation deliberately keeps.
		readBySort := timeIt(5, func() {
			if _, err := s.ProjectStatsPage(ctx, Query{
				ProjectPaths: scope, TopFiles: 5, Sort: SortTopFileHits, Desc: true, Limit: 25,
			}, now); err != nil {
				t.Fatalf("read by file sort: %v", err)
			}
		})

		// The background writer, at this database size.
		r := NewRecorder(s, nil)
		r.now = func() time.Time { return now }
		flush := timeIt(5, func() {
			for p := 0; p < 20; p++ {
				r.Record(scope[p%len(scope)], KindSemantic,
					[]string{"a.go", "b.go", "c.go", "d.go", "e.go"})
			}
			if err := r.Flush(ctx); err != nil {
				t.Fatalf("flush: %v", err)
			}
		})

		// The search path itself. Nothing here touches the database.
		files := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
		record := timeIt(2000, func() { r.Record(scope[0], KindSemantic, files) })

		fileRows := countRows(t, s, "search_file_totals") + countRows(t, s, "search_file_buckets")
		_ = s.Close()
		info, _ := os.Stat(path)
		var size int64
		if info != nil {
			size = info.Size()
		}

		fmt.Printf("%-11s %9d %9d %10.1fMB %10v %10v %11v %10v %10v\n",
			c.label, c.projects, fileRows, float64(size)/(1<<20),
			readAll.Round(time.Microsecond), readWindow.Round(time.Microsecond),
			readBySort.Round(time.Microsecond),
			flush.Round(time.Microsecond), record.Round(time.Nanosecond))
	}
	fmt.Println()
}
