package searchstats

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Sort keys accepted by Query.Sort.
const (
	SortProject       = "project"
	SortQueries       = "queries"
	SortResults       = "results"
	SortFileHits      = "file_hits"
	SortTopFileHits   = "top_file_hits"
	SortDistinctFiles = "distinct_files"
	SortLastSeen      = "last_seen"
)

// sortColumns maps a wire-level sort key to the SQL expression it orders by.
//
// A map rather than string concatenation because the sort key arrives from a
// query string. Anything not in this map is rejected by the caller; nothing
// user-supplied ever reaches the statement text.
var sortColumns = map[string]string{
	SortProject:       "s.project_path",
	SortQueries:       "queries",
	SortResults:       "results",
	SortFileHits:      "file_hits",
	SortTopFileHits:   "top_file_hits",
	SortDistinctFiles: "distinct_files",
	SortLastSeen:      "last_seen",
}

// ValidSort reports whether a sort key is one this package understands.
func ValidSort(key string) bool { _, ok := sortColumns[key]; return ok }

// Query is one request for the statistics table.
//
// ProjectPaths is REQUIRED and is the access-control boundary. This database
// has no idea who owns what — that lives in the system database — so the
// caller resolves the set of projects the requester may see (exactly as
// workspace search does, via access.AccessibleProjectHostPaths) and passes it
// here. An empty slice returns nothing, which is the correct answer for a user
// with no visible projects and the safe failure mode if a caller ever forgets
// to populate it.
type Query struct {
	ProjectPaths []string

	// Kinds narrows to particular search kinds. Empty means every kind.
	Kinds []string

	// Window selects the tier. Zero reads the cumulative totals — the
	// all-time numbers. A positive duration reads the bucket tier for that
	// much time back, and is silently capped at WindowRetention because
	// nothing older is kept.
	Window time.Duration

	// Counter range filters. Nil means unbounded on that side. They apply to
	// whichever tier Window selected, so "more than 50 queries" means more
	// than 50 in the window being displayed.
	MinQueries  *int64
	MaxQueries  *int64
	MinFileHits *int64
	MaxFileHits *int64
	MinTopFile  *int64
	MaxTopFile  *int64

	// TopFiles is how many of each project's most-returned files to attach.
	TopFiles int

	Sort   string
	Desc   bool
	Limit  int
	Offset int
}

// ProjectStats is one row of the table.
type ProjectStats struct {
	ProjectPath   string     `json:"project_path"`
	Queries       int64      `json:"queries"`
	Results       int64      `json:"results"`
	FileHits      int64      `json:"file_hits"`
	DistinctFiles int64      `json:"distinct_files"`
	TopFileHits   int64      `json:"top_file_hits"`
	LastSeen      int64      `json:"last_seen"`
	TopFiles      []FileStat `json:"top_files"`
}

// FileStat is one file inside a project's top-files list.
type FileStat struct {
	FilePath string `json:"file_path"`
	Hits     int64  `json:"hits"`
}

// Page is the result of a Query: one page of rows, plus the figures that
// describe the whole filtered set behind it.
type Page struct {
	Rows []ProjectStats
	// Total is how many projects matched the filters before LIMIT/OFFSET —
	// what the pagination control needs.
	Total int
	// TotalQueries and TotalResults sum the ENTIRE filtered set, not the page.
	// A footer that summed only the visible rows would change every time
	// somebody paged, which is the one thing a total must not do.
	TotalQueries int64
	TotalResults int64
}

// needsFileAggregate reports whether this query has to aggregate the per-file
// table across EVERY scoped project before it can build a page.
//
// It does, but only when the answer depends on it: ordering by one of the file
// columns, or filtering on one, needs every project's value before the first
// page can be chosen. Nothing else does — and that is the common case, because
// the dashboard's default view sorts by query count, which lives in
// search_totals at one row per project.
//
// The distinction is worth its complexity because the two costs are not close.
// The file aggregate is O(every file row of every visible project); measured on
// a 450k-row database with 100 projects it was 519 ms, while computing the same
// three numbers for only the 25 projects on the page costs 44 ms. On an admin's
// screen, which refreshes every 30 seconds and whose scope is every project on
// the server, that is the difference between a page that keeps up and one that
// does not.
func (q Query) needsFileAggregate() bool {
	switch q.Sort {
	case SortFileHits, SortTopFileHits, SortDistinctFiles:
		return true
	}
	return q.MinFileHits != nil || q.MaxFileHits != nil ||
		q.MinTopFile != nil || q.MaxTopFile != nil
}

// tier names the pair of tables a query reads.
type tier struct {
	counters string
	files    string
	// since is the bucket floor to filter on, or 0 for the untiered totals
	// tables, which carry no bucket column at all.
	since int64
}

func (q Query) tier(now time.Time) tier {
	if q.Window <= 0 {
		return tier{counters: "search_totals", files: "search_file_totals"}
	}
	w := q.Window
	if w > WindowRetention {
		w = WindowRetention
	}
	return tier{
		counters: "search_buckets",
		files:    "search_file_buckets",
		since:    BucketOf(now.Add(-w)),
	}
}

// ProjectStatsPage runs the table query: aggregate, filter, sort, paginate,
// then attach each surviving project's top files.
//
// Everything except the top-files attachment happens in one statement. That is
// the point of the shape — sorting by a computed aggregate and then paginating
// cannot be done correctly in the caller without pulling every project's
// numbers across first, which is exactly what "filters work on the server" is
// asking to avoid.
//
// Projects with no recorded search do not appear at all: this database only
// knows about projects somebody has searched. The caller knows the full
// accessible set and can report the difference.
func (s *Store) ProjectStatsPage(ctx context.Context, q Query, now time.Time) (Page, error) {
	if len(q.ProjectPaths) == 0 {
		return Page{}, nil
	}
	sortExpr, ok := sortColumns[q.Sort]
	if !ok {
		sortExpr = sortColumns[SortQueries]
	}
	t := q.tier(now)

	// args are accumulated in the order the placeholders appear in the
	// statement, which is why the CTEs are built in a fixed order below
	// rather than assembled from a map.
	var args []any

	scopedSQL := `SELECT id, project_path FROM projects_seen WHERE project_path IN (` +
		placeholders(len(q.ProjectPaths)) + `)`
	for _, p := range q.ProjectPaths {
		args = append(args, p)
	}

	kindFilter := ""
	kindArgs := make([]any, 0, len(q.Kinds))
	if len(q.Kinds) > 0 {
		kindFilter = ` AND kind IN (` + placeholders(len(q.Kinds)) + `)`
		for _, k := range q.Kinds {
			kindArgs = append(kindArgs, k)
		}
	}

	bucketFilter := ""
	if t.since > 0 {
		bucketFilter = ` AND bucket >= ?`
	}

	// last_seen always comes from the cumulative table, never from the
	// windowed one. search_buckets has no timestamp — only a bucket floor —
	// and rounding "last searched" to the nearest half hour, or worse
	// reporting the window's edge as if it were an event, would be a worse
	// answer than the exact one that is sitting right there in search_totals.
	seenSQL := `SELECT project_id, MAX(last_seen) AS last_seen
	              FROM search_totals
	             WHERE project_id IN (SELECT id FROM scoped)` + kindFilter + `
	          GROUP BY project_id`
	args = append(args, kindArgs...)

	countersSQL := `SELECT project_id,
	                       SUM(queries) AS queries,
	                       SUM(results) AS results
	                  FROM ` + t.counters + `
	                 WHERE project_id IN (SELECT id FROM scoped)` + kindFilter + bucketFilter + `
	              GROUP BY project_id`
	args = append(args, kindArgs...)
	if t.since > 0 {
		args = append(args, t.since)
	}

	// The inner regroup by file_path is what makes top_file_hits mean "the
	// busiest FILE". On the windowed tier a file's hits are split across
	// buckets, so MAX(hits) taken straight off the table would report the
	// busiest half hour of the busiest file instead. Applied to both tiers so
	// the two agree on what the column means.
	//
	// Built only when the page's ORDER or a filter depends on it — see
	// needsFileAggregate. Otherwise the CTE is replaced by a stub that joins
	// nothing, the three columns come back as zero, and fillFileAggregates
	// computes them for the page's rows alone.
	wantFileAggregate := q.needsFileAggregate()
	filesSQL := `SELECT NULL AS project_id, 0 AS file_hits, 0 AS distinct_files, 0 AS top_file_hits
	              WHERE 0`
	if wantFileAggregate {
		filesSQL = `SELECT project_id,
		                    SUM(hits) AS file_hits,
		                    COUNT(*)  AS distinct_files,
		                    MAX(hits) AS top_file_hits
		               FROM (SELECT project_id, file_path, SUM(hits) AS hits
		                       FROM ` + t.files + `
		                      WHERE project_id IN (SELECT id FROM scoped)` + kindFilter + bucketFilter + `
		                   GROUP BY project_id, file_path)
		           GROUP BY project_id`
		args = append(args, kindArgs...)
		if t.since > 0 {
			args = append(args, t.since)
		}
	}

	base := `
		WITH scoped AS (` + scopedSQL + `),
		     seen AS (` + seenSQL + `),
		     counters AS (` + countersSQL + `),
		     files AS (` + filesSQL + `)
		SELECT s.project_path,
		       COALESCE(counters.queries, 0)      AS queries,
		       COALESCE(counters.results, 0)      AS results,
		       COALESCE(files.file_hits, 0)       AS file_hits,
		       COALESCE(files.distinct_files, 0)  AS distinct_files,
		       COALESCE(files.top_file_hits, 0)   AS top_file_hits,
		       COALESCE(seen.last_seen, 0)        AS last_seen
		  FROM scoped s
		  LEFT JOIN seen     ON seen.project_id     = s.id
		  LEFT JOIN counters ON counters.project_id = s.id
		  LEFT JOIN files    ON files.project_id    = s.id`

	// A project whose only rows fell outside the window has no counters and no
	// files — it must not occupy a row in a windowed view claiming zero. On the
	// totals tier every project in projects_seen has counters by construction,
	// so this changes nothing there.
	where := []string{"counters.project_id IS NOT NULL"}
	rangeFilters := []struct {
		expr     string
		min, max *int64
	}{
		{"queries", q.MinQueries, q.MaxQueries},
		{"file_hits", q.MinFileHits, q.MaxFileHits},
		{"top_file_hits", q.MinTopFile, q.MaxTopFile},
	}
	for _, f := range rangeFilters {
		if f.min != nil {
			where = append(where, f.expr+" >= ?")
			args = append(args, *f.min)
		}
		if f.max != nil {
			where = append(where, f.expr+" <= ?")
			args = append(args, *f.max)
		}
	}
	base += "\n WHERE " + strings.Join(where, " AND ")

	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	// The row count and the footer sums are computed by window functions over
	// the same result set the page is cut from, in ONE pass.
	//
	// They used to be a second statement wrapping the identical CTE chain,
	// which meant the whole aggregate ran twice per request — measured at 519 ms
	// for the pair on a 450k-row database where each pass was ~260 ms. Reading
	// them off the page's own rows keeps the property that mattered about the
	// wrapper (the three figures cannot disagree with each other about what
	// matched, because they are one query) and halves the work.
	//
	// project_path is the tie-break on every sort. Without it, two projects with
	// equal counters could swap places between the page-1 and page-2 queries and
	// a row would be shown twice or not at all.
	listSQL := `SELECT *,
	                   COUNT(*)       OVER () AS total_rows,
	                   SUM(queries)   OVER () AS total_queries,
	                   SUM(results)   OVER () AS total_results
	              FROM (` + base + fmt.Sprintf("\n ORDER BY %s %s, s.project_path ASC)", sortExpr, dir) +
		"\n LIMIT ? OFFSET ?"
	listArgs := append(append([]any{}, args...), limit, offset)

	rows, err := s.db.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return Page{}, fmt.Errorf("searchstats: list project stats: %w", err)
	}
	defer rows.Close()

	var out []ProjectStats
	var total int
	var totalQueries, totalResults int64
	for rows.Next() {
		var p ProjectStats
		if err := rows.Scan(&p.ProjectPath, &p.Queries, &p.Results,
			&p.FileHits, &p.DistinctFiles, &p.TopFileHits, &p.LastSeen,
			&total, &totalQueries, &totalResults); err != nil {
			return Page{}, fmt.Errorf("searchstats: scan project stats: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("searchstats: iterate project stats: %w", err)
	}
	if len(out) == 0 {
		// No rows means nothing matched, or the offset is past the end. The
		// window functions produced no row to read the totals from, and zero is
		// the right answer for the first case; for the second the caller is
		// paging past a set it has already seen the size of.
		return Page{}, nil
	}

	if !wantFileAggregate {
		if err := s.fillFileAggregates(ctx, out, q, t); err != nil {
			return Page{}, err
		}
	}
	if q.TopFiles > 0 {
		if err := s.attachTopFiles(ctx, out, q, t); err != nil {
			return Page{}, err
		}
	}
	return Page{
		Rows:         out,
		Total:        total,
		TotalQueries: totalQueries,
		TotalResults: totalResults,
	}, nil
}

// fillFileAggregates computes file_hits, distinct_files and top_file_hits for
// the rows on the page.
//
// This is the other half of needsFileAggregate: when nothing in the request
// ordered or filtered by those columns, the page was chosen without them, and
// the same three numbers can be had by scanning ~25 projects instead of every
// project the caller can see. One statement for the whole page rather than one
// per row — the page is a bounded IN list, and this is not the loop that has to
// stay obvious.
func (s *Store) fillFileAggregates(ctx context.Context, rows []ProjectStats, q Query, t tier) error {
	if len(rows) == 0 {
		return nil
	}
	paths := make([]any, 0, len(rows))
	for i := range rows {
		paths = append(paths, rows[i].ProjectPath)
	}
	args := append([]any{}, paths...)

	kindFilter := ""
	if len(q.Kinds) > 0 {
		kindFilter = ` AND kind IN (` + placeholders(len(q.Kinds)) + `)`
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	bucketFilter := ""
	if t.since > 0 {
		bucketFilter = ` AND bucket >= ?`
		args = append(args, t.since)
	}

	// The inner regroup by file_path carries the same meaning it does in the
	// full aggregate: top_file_hits is the busiest FILE, which on the windowed
	// tier means summing a file across its buckets first.
	stmt := `
		SELECT ps.project_path, SUM(t.hits), COUNT(*), MAX(t.hits)
		  FROM (SELECT project_id, file_path, SUM(hits) AS hits
		          FROM ` + t.files + `
		         WHERE project_id IN (SELECT id FROM projects_seen WHERE project_path IN (` +
		placeholders(len(paths)) + `))` + kindFilter + bucketFilter + `
		      GROUP BY project_id, file_path) t
		  JOIN projects_seen ps ON ps.id = t.project_id
	      GROUP BY t.project_id`

	found, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("searchstats: page file aggregates: %w", err)
	}
	defer found.Close()

	byPath := make(map[string][3]int64, len(rows))
	for found.Next() {
		var path string
		var hits, distinct, top int64
		if err := found.Scan(&path, &hits, &distinct, &top); err != nil {
			return fmt.Errorf("searchstats: scan page file aggregates: %w", err)
		}
		byPath[path] = [3]int64{hits, distinct, top}
	}
	if err := found.Err(); err != nil {
		return fmt.Errorf("searchstats: iterate page file aggregates: %w", err)
	}
	// A project with no file rows keeps its zeroes, which is the truth: it was
	// searched and nothing came back.
	for i := range rows {
		if v, ok := byPath[rows[i].ProjectPath]; ok {
			rows[i].FileHits, rows[i].DistinctFiles, rows[i].TopFileHits = v[0], v[1], v[2]
		}
	}
	return nil
}

// attachTopFiles fills in each row's TopFiles.
//
// One statement per project on the page, rather than one window-function query
// over all of them. The page is bounded by the caller's limit — tens of rows —
// and SQLite is in-process, so there is no round trip to save; what there is to
// lose is a query plan that stops being obvious. Per project the statement is
// an indexed range on the primary key followed by a bounded sort.
func (s *Store) attachTopFiles(ctx context.Context, rows []ProjectStats, q Query, t tier) error {
	if len(rows) == 0 {
		return nil
	}
	kindFilter := ""
	kindArgs := make([]any, 0, len(q.Kinds))
	if len(q.Kinds) > 0 {
		kindFilter = ` AND kind IN (` + placeholders(len(q.Kinds)) + `)`
		for _, k := range q.Kinds {
			kindArgs = append(kindArgs, k)
		}
	}
	bucketFilter := ""
	if t.since > 0 {
		bucketFilter = ` AND bucket >= ?`
	}

	stmtSQL := `
		SELECT file_path, SUM(hits) AS hits
		  FROM ` + t.files + `
		 WHERE project_id = (SELECT id FROM projects_seen WHERE project_path = ?)` +
		kindFilter + bucketFilter + `
	  GROUP BY file_path
	  ORDER BY hits DESC, file_path ASC
	     LIMIT ?`

	stmt, err := s.db.PrepareContext(ctx, stmtSQL)
	if err != nil {
		return fmt.Errorf("searchstats: prepare top files: %w", err)
	}
	defer stmt.Close()

	for i := range rows {
		args := []any{rows[i].ProjectPath}
		args = append(args, kindArgs...)
		if t.since > 0 {
			args = append(args, t.since)
		}
		args = append(args, q.TopFiles)

		fileRows, err := stmt.QueryContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("searchstats: top files for %s: %w", rows[i].ProjectPath, err)
		}
		var files []FileStat
		for fileRows.Next() {
			var f FileStat
			if err := fileRows.Scan(&f.FilePath, &f.Hits); err != nil {
				fileRows.Close()
				return fmt.Errorf("searchstats: scan top file: %w", err)
			}
			files = append(files, f)
		}
		err = fileRows.Err()
		fileRows.Close()
		if err != nil {
			return fmt.Errorf("searchstats: iterate top files: %w", err)
		}
		rows[i].TopFiles = files
	}
	return nil
}

// TimePoint is one bucket of the windowed series.
type TimePoint struct {
	Bucket  int64 `json:"bucket"`
	Queries int64 `json:"queries"`
}

// Series returns the windowed query counts, one point per bucket, summed over
// the given projects. Buckets with no activity are absent rather than zero —
// the caller knows the bucket width and can fill the gaps for a chart without
// this having to transmit the empties.
func (s *Store) Series(ctx context.Context, q Query, now time.Time) ([]TimePoint, error) {
	if len(q.ProjectPaths) == 0 {
		return nil, nil
	}
	w := q.Window
	if w <= 0 || w > WindowRetention {
		w = WindowRetention
	}
	since := BucketOf(now.Add(-w))

	args := make([]any, 0, len(q.ProjectPaths)+len(q.Kinds)+1)
	for _, p := range q.ProjectPaths {
		args = append(args, p)
	}
	kindFilter := ""
	if len(q.Kinds) > 0 {
		kindFilter = ` AND kind IN (` + placeholders(len(q.Kinds)) + `)`
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	args = append(args, since)

	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket, SUM(queries)
		  FROM search_buckets
		 WHERE project_id IN (SELECT id FROM projects_seen WHERE project_path IN (`+
		placeholders(len(q.ProjectPaths))+`))`+kindFilter+`
		   AND bucket >= ?
	  GROUP BY bucket
	  ORDER BY bucket ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("searchstats: series: %w", err)
	}
	defer rows.Close()

	var out []TimePoint
	for rows.Next() {
		var p TimePoint
		if err := rows.Scan(&p.Bucket, &p.Queries); err != nil {
			return nil, fmt.Errorf("searchstats: scan series point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ActiveProjectCount reports how many of the given projects have ever recorded
// a search.
//
// Deliberately independent of every filter in Query. The question it answers —
// "how many of the projects I can see has nobody ever searched" — is a property
// of the projects, not of the current view, so deriving it from a filtered page
// would report projects excluded by a `min_queries` filter as never searched.
func (s *Store) ActiveProjectCount(ctx context.Context, paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(paths))
	for _, p := range paths {
		args = append(args, p)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects_seen WHERE project_path IN (`+
			placeholders(len(paths))+`)`, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("searchstats: count active projects: %w", err)
	}
	return n, nil
}
