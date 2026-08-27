package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/searchstats"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// statsFixture is newAuthFixture plus a wired search-statistics store. It is a
// separate constructor rather than a flag on the shared one so that every other
// test keeps running with the feature absent — which is also the shape a
// deployment with CIX_SEARCH_STATS_ENABLED=false has, and therefore worth
// leaving as the default the rest of the suite exercises.
type statsFixture struct {
	*authTestFixture
	Holder   *searchstats.Holder
	Store    *searchstats.Store
	Recorder *searchstats.Recorder
}

func newStatsFixture(t testing.TB) *statsFixture {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	usrSvc := users.New(database)
	sessSvc := sessions.New(database)
	akSvc := apikeys.New(database)

	u, err := usrSvc.Create(context.Background(), "admin@example.com", "secret-password", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	full, _, err := akSvc.Generate(context.Background(), u.ID, "test-key")
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// Built through the holder, the way production does, so the tests exercise
	// the same enable path an admin's toggle takes.
	holder := searchstats.NewHolder(context.Background(),
		filepath.Join(t.TempDir(), searchstats.DBFileName), nil)
	if err := holder.Enable(); err != nil {
		t.Fatalf("enable search stats: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })

	deps := Deps{
		DB:                  database,
		ServerVersion:       "0.0.0-test",
		APIVersion:          "v1",
		EmbeddingModel:      "test-model",
		Users:               usrSvc,
		Sessions:            sessSvc,
		APIKeys:             akSvc,
		Groups:              groups.New(database),
		Workspaces:          workspaces.New(database),
		SearchStats:         holder,
		SearchStatsSettings: searchstats.NewSettingsStore(database, true, true),
	}
	return &statsFixture{
		authTestFixture: &authTestFixture{
			Router: NewRouter(deps), Deps: deps, UserID: u.ID, FullKey: full,
		},
		Holder:   holder,
		Store:    holder.Store(),
		Recorder: holder.Recorder(),
	}
}

// seedLocalProject creates an owned local project and records `queries`
// searches against it, each returning the given files.
func seedLocalProject(t testing.TB, f *statsFixture, hostPath, ownerID string, queries int, files ...string) string {
	t.Helper()
	if _, err := projects.Create(context.Background(), f.Deps.DB, projects.CreateRequest{
		HostPath: hostPath, OwnerUserID: ownerID,
	}); err != nil {
		t.Fatalf("create project %s: %v", hostPath, err)
	}
	for i := 0; i < queries; i++ {
		f.Recorder.Record(hostPath, searchstats.KindSemantic, files)
	}
	if err := f.Recorder.Flush(context.Background()); err != nil {
		t.Fatalf("flush recorder: %v", err)
	}
	return projects.HashPath(hostPath)
}

type statsPayload struct {
	Projects []struct {
		ProjectPath   string `json:"project_path"`
		PathHash      string `json:"path_hash"`
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		Queries       int64  `json:"queries"`
		Results       int64  `json:"results"`
		FileHits      int64  `json:"file_hits"`
		DistinctFiles int64  `json:"distinct_files"`
		TopFileHits   int64  `json:"top_file_hits"`
		LastSeen      int64  `json:"last_seen"`
		TopFiles      []struct {
			FilePath string `json:"file_path"`
			Hits     int64  `json:"hits"`
		} `json:"top_files"`
	} `json:"projects"`
	Total                   int    `json:"total"`
	Window                  string `json:"window"`
	BucketSeconds           int    `json:"bucket_seconds"`
	RetentionSeconds        int    `json:"retention_seconds"`
	ProjectsWithoutActivity int    `json:"projects_without_activity"`
	Totals                  struct {
		Queries int64 `json:"queries"`
		Results int64 `json:"results"`
	} `json:"totals"`
}

func getStats(t *testing.T, f *statsFixture, cookie, query string) statsPayload {
	t.Helper()
	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodGet, "/api/v1/search-stats"+query, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /search-stats%s = %d (%s)", query, rr.Code, body)
	}
	var p statsPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	return p
}

// ---------------------------------------------------------------------------
// Access gating — the matrix in docs/AUTH_REVIEW.md.
// ---------------------------------------------------------------------------

func TestSearchStats_RequiresAuthentication(t *testing.T) {
	f := newStatsFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/search-stats"},
		{http.MethodGet, "/api/v1/search-stats/series"},
		{http.MethodPost, "/api/v1/admin/search-stats/reset"},
	} {
		rr, body := doReq(t, f.authTestFixture, "", c.method, c.path, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401 (%s)", c.method, c.path, rr.Code, body)
		}
	}
}

func TestSearchStats_ResetIsAdminOnly(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	userCookie := seedUser(t, f.authTestFixture, adminCookie, "bob@example.com", "bobpass1234")

	if rr, body := doReq(t, f.authTestFixture, userCookie,
		http.MethodPost, "/api/v1/admin/search-stats/reset", nil); rr.Code != http.StatusForbidden {
		t.Errorf("reset as a regular user = %d, want 403 (%s)", rr.Code, body)
	}
	if rr, body := doReq(t, f.authTestFixture, adminCookie,
		http.MethodPost, "/api/v1/admin/search-stats/reset", nil); rr.Code != http.StatusNoContent {
		t.Errorf("reset as admin = %d, want 204 (%s)", rr.Code, body)
	}
}

// A regular user's table is scoped to the projects they can already search.
// This is the finding the endpoint would be a data leak without: the counters
// carry file paths out of every project on the server.
func TestSearchStats_ScopedToAccessibleProjects(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	bobCookie := seedUser(t, f.authTestFixture, adminCookie, "bob@example.com", "bobpass1234")
	bobID := userIDByEmail(t, f.authTestFixture, adminCookie, "bob@example.com")

	seedLocalProject(t, f, "/tmp/admin-only", f.UserID, 5, "secret.go")
	seedLocalProject(t, f, "/tmp/bob-project", bobID, 3, "bob.go")

	bob := getStats(t, f, bobCookie, "")
	if len(bob.Projects) != 1 {
		t.Fatalf("bob sees %d projects, want 1: %+v", len(bob.Projects), bob.Projects)
	}
	if bob.Projects[0].ProjectPath != "/tmp/bob-project" {
		t.Errorf("bob sees %q, want only his own project", bob.Projects[0].ProjectPath)
	}
	if bob.Total != 1 {
		t.Errorf("bob's total = %d, want 1 — the count must be scoped too", bob.Total)
	}
	if bob.Totals.Queries != 3 {
		t.Errorf("bob's footer total = %d, want 3 — it must not sum the admin's project",
			bob.Totals.Queries)
	}

	admin := getStats(t, f, adminCookie, "")
	if len(admin.Projects) != 2 {
		t.Fatalf("admin sees %d projects, want 2", len(admin.Projects))
	}
	if admin.Totals.Queries != 8 {
		t.Errorf("admin's footer total = %d, want 8", admin.Totals.Queries)
	}
}

func TestSearchStats_UnavailableWhenDisabled(t *testing.T) {
	// The ordinary fixture leaves SearchStats nil — the shape of a server with
	// CIX_SEARCH_STATS_ENABLED=false.
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/search-stats"},
		{http.MethodGet, "/api/v1/search-stats/series"},
		{http.MethodPost, "/api/v1/admin/search-stats/reset"},
	} {
		rr, body := doReq(t, f, adminCookie, c.method, c.path, nil)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with statistics off = %d, want 503 (%s)", c.method, c.path, rr.Code, body)
		}
	}
}

// ---------------------------------------------------------------------------
// The table itself.
// ---------------------------------------------------------------------------

func TestSearchStats_RowCarriesProjectIdentityAndTopFiles(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	hash := seedLocalProject(t, f, "/tmp/proj", f.UserID, 4, "hot.go", "cold.go")
	// One extra search that only returns hot.go, so the two files differ.
	f.Recorder.Record("/tmp/proj", searchstats.KindSemantic, []string{"hot.go"})
	if err := f.Recorder.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	p := getStats(t, f, adminCookie, "?top_files=5")
	if len(p.Projects) != 1 {
		t.Fatalf("projects = %+v, want 1", p.Projects)
	}
	row := p.Projects[0]
	if row.PathHash != hash {
		t.Errorf("row path_hash = %q, want %q", row.PathHash, hash)
	}
	if row.Name != "/tmp/proj" {
		t.Errorf("row name = %q, want the project's display path", row.Name)
	}
	if row.Kind != "local" {
		t.Errorf("kind = %q, want local", row.Kind)
	}
	if row.Queries != 5 {
		t.Errorf("queries = %d, want 5", row.Queries)
	}
	if row.DistinctFiles != 2 {
		t.Errorf("distinct_files = %d, want 2", row.DistinctFiles)
	}
	if row.TopFileHits != 5 {
		t.Errorf("top_file_hits = %d, want 5 (hot.go in every search)", row.TopFileHits)
	}
	if row.LastSeen == 0 {
		t.Error("last_seen = 0, want the recorded timestamp")
	}
	if len(row.TopFiles) != 2 || row.TopFiles[0].FilePath != "hot.go" || row.TopFiles[0].Hits != 5 {
		t.Errorf("top_files = %+v, want hot.go first with 5 hits", row.TopFiles)
	}
	// The stated invariant: a file cannot appear in more searches than there
	// were searches.
	if row.TopFileHits > row.Queries {
		t.Errorf("top_file_hits %d exceeds queries %d — the dedupe is not holding",
			row.TopFileHits, row.Queries)
	}
	if p.BucketSeconds != searchstats.BucketSeconds {
		t.Errorf("bucket_seconds = %d, want %d", p.BucketSeconds, searchstats.BucketSeconds)
	}
}

func TestSearchStats_ServerSideFiltersAndSort(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	seedLocalProject(t, f, "/tmp/busy", f.UserID, 10, "a.go")
	seedLocalProject(t, f, "/tmp/quiet", f.UserID, 2, "b.go")

	if p := getStats(t, f, adminCookie, "?min_queries=5"); len(p.Projects) != 1 ||
		p.Projects[0].ProjectPath != "/tmp/busy" {
		t.Errorf("min_queries=5 gave %+v, want only busy", p.Projects)
	}
	if p := getStats(t, f, adminCookie, "?max_queries=5"); len(p.Projects) != 1 ||
		p.Projects[0].ProjectPath != "/tmp/quiet" {
		t.Errorf("max_queries=5 gave %+v, want only quiet", p.Projects)
	}
	if p := getStats(t, f, adminCookie, "?min_top_file_hits=5"); len(p.Projects) != 1 ||
		p.Projects[0].ProjectPath != "/tmp/busy" {
		t.Errorf("min_top_file_hits=5 gave %+v, want only busy", p.Projects)
	}

	// Default order is descending on queries.
	if p := getStats(t, f, adminCookie, ""); p.Projects[0].ProjectPath != "/tmp/busy" {
		t.Errorf("default sort put %q first, want busy", p.Projects[0].ProjectPath)
	}
	if p := getStats(t, f, adminCookie, "?sort=queries&order=asc"); p.Projects[0].ProjectPath != "/tmp/quiet" {
		t.Errorf("ascending sort put %q first, want quiet", p.Projects[0].ProjectPath)
	}
	if p := getStats(t, f, adminCookie, "?sort=project&order=asc"); p.Projects[0].ProjectPath != "/tmp/busy" {
		t.Errorf("sort by project put %q first, want alphabetical", p.Projects[0].ProjectPath)
	}

	// The name filter matches the display path, not the storage key.
	p := getStats(t, f, adminCookie, "?project=QUIET")
	if len(p.Projects) != 1 || p.Projects[0].ProjectPath != "/tmp/quiet" {
		t.Errorf("project=QUIET gave %+v, want a case-insensitive match on quiet", p.Projects)
	}
	if p.Total != 1 {
		t.Errorf("total under the name filter = %d, want 1", p.Total)
	}
}

func TestSearchStats_PaginationReportsTheFullTotal(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	seedLocalProject(t, f, "/tmp/p1", f.UserID, 3, "a.go")
	seedLocalProject(t, f, "/tmp/p2", f.UserID, 2, "b.go")
	seedLocalProject(t, f, "/tmp/p3", f.UserID, 1, "c.go")

	p := getStats(t, f, adminCookie, "?limit=1&offset=1&sort=queries&order=desc")
	if len(p.Projects) != 1 {
		t.Fatalf("page holds %d rows, want 1", len(p.Projects))
	}
	if p.Projects[0].ProjectPath != "/tmp/p2" {
		t.Errorf("offset=1 gave %q, want p2", p.Projects[0].ProjectPath)
	}
	if p.Total != 3 {
		t.Errorf("total = %d, want 3 — the count must ignore limit/offset", p.Total)
	}
	if p.Totals.Queries != 6 {
		t.Errorf("footer queries = %d, want 6 — the footer must sum the filtered set, not the page",
			p.Totals.Queries)
	}
}

// projects_without_activity must be a property of the projects, not of the
// current filters. A project excluded by min_queries has been searched.
func TestSearchStats_IdleCountIgnoresFilters(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	seedLocalProject(t, f, "/tmp/searched", f.UserID, 2, "a.go")
	// Never searched.
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{
		HostPath: "/tmp/untouched", OwnerUserID: f.UserID,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if p := getStats(t, f, adminCookie, ""); p.ProjectsWithoutActivity != 1 {
		t.Errorf("projects_without_activity = %d, want 1", p.ProjectsWithoutActivity)
	}
	// Filtering the searched project out of the page must not turn it into an
	// idle project.
	if p := getStats(t, f, adminCookie, "?min_queries=100"); p.ProjectsWithoutActivity != 1 {
		t.Errorf("projects_without_activity under a filter = %d, want 1 — it must not count filtered rows",
			p.ProjectsWithoutActivity)
	}
}

func TestSearchStats_KindFilter(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	seedLocalProject(t, f, "/tmp/proj", f.UserID, 2, "sem.go")
	f.Recorder.Record("/tmp/proj", searchstats.KindSymbols, []string{"sym.go"})
	if err := f.Recorder.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if p := getStats(t, f, adminCookie, ""); p.Projects[0].Queries != 3 {
		t.Errorf("unfiltered queries = %d, want 3", p.Projects[0].Queries)
	}
	if p := getStats(t, f, adminCookie, "?kinds=symbols"); p.Projects[0].Queries != 1 {
		t.Errorf("kinds=symbols queries = %d, want 1", p.Projects[0].Queries)
	}
	// An unknown kind is dropped, not rejected — the request still narrows to
	// the recognisable part rather than 422-ing on a typo.
	if p := getStats(t, f, adminCookie, "?kinds=symbols,not-a-kind"); p.Projects[0].Queries != 1 {
		t.Errorf("kinds with an unknown entry gave %d, want 1", p.Projects[0].Queries)
	}
	if p := getStats(t, f, adminCookie, "?kinds=not-a-kind"); p.Projects[0].Queries != 3 {
		t.Errorf("kinds with only unknown entries gave %d, want the unfiltered 3", p.Projects[0].Queries)
	}
}

func TestSearchStats_RejectsUnknownWindow(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	rr, body := doReq(t, f.authTestFixture, adminCookie,
		http.MethodGet, "/api/v1/search-stats?window=30d", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("window=30d = %d, want 422 (%s)", rr.Code, body)
	}
}

func TestSearchStatsSeries_ScopedAndBucketed(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	bobCookie := seedUser(t, f.authTestFixture, adminCookie, "bob@example.com", "bobpass1234")
	bobID := userIDByEmail(t, f.authTestFixture, adminCookie, "bob@example.com")

	adminHash := seedLocalProject(t, f, "/tmp/admin-only", f.UserID, 4, "a.go")
	seedLocalProject(t, f, "/tmp/bob-project", bobID, 1, "b.go")

	type series struct {
		Points []struct {
			Bucket  int64 `json:"bucket"`
			Queries int64 `json:"queries"`
		} `json:"points"`
		BucketSeconds int `json:"bucket_seconds"`
		WindowSeconds int `json:"window_seconds"`
	}
	read := func(cookie, query string) series {
		t.Helper()
		rr, body := doReq(t, f.authTestFixture, cookie, http.MethodGet,
			"/api/v1/search-stats/series"+query, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("series%s = %d (%s)", query, rr.Code, body)
		}
		var s series
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		return s
	}

	adminSeries := read(adminCookie, "")
	var adminTotal int64
	for _, pt := range adminSeries.Points {
		adminTotal += pt.Queries
	}
	if adminTotal != 5 {
		t.Errorf("admin series total = %d, want 5", adminTotal)
	}
	if adminSeries.BucketSeconds != searchstats.BucketSeconds {
		t.Errorf("bucket_seconds = %d, want %d", adminSeries.BucketSeconds, searchstats.BucketSeconds)
	}

	bobSeries := read(bobCookie, "")
	var bobTotal int64
	for _, pt := range bobSeries.Points {
		bobTotal += pt.Queries
	}
	if bobTotal != 1 {
		t.Errorf("bob's series total = %d, want 1 — the series must be access-scoped", bobTotal)
	}

	// A hash bob cannot see is a 404, indistinguishable from one that does not
	// exist, so the endpoint does not confirm the project is there.
	if rr, _ := doReq(t, f.authTestFixture, bobCookie, http.MethodGet,
		"/api/v1/search-stats/series?project_hash="+adminHash, nil); rr.Code != http.StatusNotFound {
		t.Errorf("bob asking for the admin's project = %d, want 404", rr.Code)
	}
	// The admin gets that project's own series.
	scoped := read(adminCookie, "?project_hash="+adminHash)
	var scopedTotal int64
	for _, pt := range scoped.Points {
		scopedTotal += pt.Queries
	}
	if scopedTotal != 4 {
		t.Errorf("project-scoped series = %d, want 4", scopedTotal)
	}
}

// Deleting a project must take its counters with it — otherwise the file paths
// of a removed repo sit in the statistics database forever, since the totals
// tier is never pruned.
//
// The assertion goes to the STORE, not to GET /search-stats. That endpoint is
// scoped to the projects the caller can see, so a deleted project is absent
// from it whether or not its counters were discarded — an earlier version of
// this test asserted through the endpoint and passed with the cleanup call
// stubbed out entirely.
func TestSearchStats_ProjectDeleteDiscardsCounters(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	hash := seedLocalProject(t, f, "/tmp/doomed", f.UserID, 3, "a.go")
	seedLocalProject(t, f, "/tmp/survivor", f.UserID, 1, "b.go")

	countRows := func(where string) int {
		t.Helper()
		var n int
		if err := f.Store.DB().QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM projects_seen WHERE project_path = ?`, where).Scan(&n); err != nil {
			t.Fatalf("count projects_seen: %v", err)
		}
		return n
	}
	if countRows("/tmp/doomed") != 1 {
		t.Fatal("the doomed project has no counters to begin with")
	}

	if rr, body := doReq(t, f.authTestFixture, adminCookie,
		http.MethodDelete, "/api/v1/projects/"+hash, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d (%s)", rr.Code, body)
	}

	if n := countRows("/tmp/doomed"); n != 0 {
		t.Errorf("the deleted project still has %d row(s) in the statistics database", n)
	}
	if n := countRows("/tmp/survivor"); n != 1 {
		t.Errorf("the surviving project lost its counters (%d rows) — the delete was too broad", n)
	}
	// The child tables must have gone with it, not just the parent row.
	for _, table := range []string{
		"search_totals", "search_file_totals", "search_buckets", "search_file_buckets",
	} {
		var n int
		if err := f.Store.DB().QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s holds %d rows after the delete, want 1 (only the survivor)", table, n)
		}
	}
}

func TestSearchStats_ResetEmptiesTheTable(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	seedLocalProject(t, f, "/tmp/proj", f.UserID, 3, "a.go")

	if rr, body := doReq(t, f.authTestFixture, adminCookie,
		http.MethodPost, "/api/v1/admin/search-stats/reset", nil); rr.Code != http.StatusNoContent {
		t.Fatalf("reset = %d (%s)", rr.Code, body)
	}
	p := getStats(t, f, adminCookie, "")
	if len(p.Projects) != 0 || p.Total != 0 {
		t.Errorf("after reset: %d rows / total %d, want nothing", len(p.Projects), p.Total)
	}
	if p.ProjectsWithoutActivity != 1 {
		t.Errorf("projects_without_activity after reset = %d, want 1", p.ProjectsWithoutActivity)
	}
}

func TestDedupePaths(t *testing.T) {
	got := dedupePaths([]string{"a.go", "b.go", "a.go", "", "b.go", "c.go"})
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("dedupePaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupePaths = %v, want %v", got, want)
		}
	}
}

// dedupePaths has two branches; both must agree, including on the boundary
// where it switches from scanning to hashing.
func TestDedupePathsBothBranches(t *testing.T) {
	for _, size := range []int{2, linearDedupeMax, linearDedupeMax + 1, linearDedupeMax * 3} {
		in := make([]string, 0, size*2)
		want := make([]string, 0, size)
		for i := 0; i < size; i++ {
			p := fmt.Sprintf("pkg/file%d.go", i)
			in = append(in, p, p) // every path twice
			want = append(want, p)
		}
		in = append(in, "") // and an empty one, which is dropped

		got := dedupePaths(in)
		if len(got) != len(want) {
			t.Fatalf("size %d: got %d paths, want %d", size, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("size %d: got %v, want %v", size, got, want)
			}
		}
	}
}

// The input must survive the call: the callers happen to build throwaway slices
// today, and an in-place dedupe would be a trap for the next call site.
func TestDedupePathsDoesNotMutateInput(t *testing.T) {
	in := []string{"a.go", "b.go", "a.go"}
	original := append([]string(nil), in...)
	dedupePaths(in)
	for i := range original {
		if in[i] != original[i] {
			t.Fatalf("input was modified: %v, was %v", in, original)
		}
	}
}

// ---------------------------------------------------------------------------
// The on/off switch.
// ---------------------------------------------------------------------------

type settingsPayload struct {
	Enabled   bool   `json:"enabled"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

func getSettings(t *testing.T, f *statsFixture, cookie string) settingsPayload {
	t.Helper()
	rr, body := doReq(t, f.authTestFixture, cookie, http.MethodGet, "/api/v1/search-stats/settings", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET settings = %d (%s)", rr.Code, body)
	}
	var p settingsPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return p
}

func TestSearchStatsSettings_ReadableByAnyUserWritableByAdmin(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	userCookie := seedUser(t, f.authTestFixture, adminCookie, "bob@example.com", "bobpass1234")

	// A regular user can read it — the statistics page has to be able to
	// explain why it is empty.
	if got := getSettings(t, f, userCookie); !got.Enabled {
		t.Errorf("user sees enabled=%v, want true", got.Enabled)
	}
	// But not change it.
	if rr, body := doReq(t, f.authTestFixture, userCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": false}); rr.Code != http.StatusForbidden {
		t.Errorf("user PUT = %d, want 403 (%s)", rr.Code, body)
	}
	// Unauthenticated reads are refused like everything else.
	if rr, _ := doReq(t, f.authTestFixture, "", http.MethodGet,
		"/api/v1/search-stats/settings", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET = %d, want 401", rr.Code)
	}
}

// Toggling takes effect immediately: no restart, and the endpoints follow.
func TestSearchStatsSettings_TogglesLive(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	seedLocalProject(t, f, "/tmp/proj", f.UserID, 3, "a.go")

	if p := getStats(t, f, adminCookie, ""); len(p.Projects) != 1 {
		t.Fatalf("before the toggle: %+v, want one row", p.Projects)
	}

	// Off.
	rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT off = %d (%s)", rr.Code, body)
	}
	var after settingsPayload
	if err := json.Unmarshal(body, &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Enabled {
		t.Error("response says still enabled after switching off")
	}
	if after.Source != "database" {
		t.Errorf("source = %q, want database — an admin's decision must outrank the environment", after.Source)
	}
	if f.Holder.Enabled() {
		t.Error("holder still reports enabled")
	}
	// The read endpoints now decline rather than reporting zeroes.
	if rr, _ := doReq(t, f.authTestFixture, adminCookie, http.MethodGet,
		"/api/v1/search-stats", nil); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /search-stats while off = %d, want 503", rr.Code)
	}
	// And recording is inert — this must not panic on a nil recorder.
	srv := &Server{Deps: f.Deps}
	srv.recordSearch("/tmp/proj", searchstats.KindSemantic, []string{"b.go"})

	// Back on: the counters collected before are still there.
	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": true}); rr.Code != http.StatusOK {
		t.Fatalf("PUT on = %d (%s)", rr.Code, body)
	}
	if !f.Holder.Enabled() {
		t.Fatal("holder did not come back up")
	}
	p := getStats(t, f, adminCookie, "")
	if len(p.Projects) != 1 || p.Projects[0].Queries != 3 {
		t.Errorf("after switching back on: %+v, want the 3 counters that were collected before", p.Projects)
	}
}

// Switching off must not discard what is still buffered.
func TestSearchStatsSettings_DisableDrainsPendingCounters(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	if _, err := projects.Create(context.Background(), f.Deps.DB, projects.CreateRequest{
		HostPath: "/tmp/proj", OwnerUserID: f.UserID,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Recorded but deliberately NOT flushed.
	f.Holder.Recorder().Record("/tmp/proj", searchstats.KindSemantic, []string{"a.go"})

	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": false}); rr.Code != http.StatusOK {
		t.Fatalf("PUT off = %d (%s)", rr.Code, body)
	}
	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": true}); rr.Code != http.StatusOK {
		t.Fatalf("PUT on = %d (%s)", rr.Code, body)
	}

	p := getStats(t, f, adminCookie, "")
	if len(p.Projects) != 1 || p.Projects[0].Queries != 1 {
		t.Errorf("after off/on: %+v, want the buffered counter to have been written on the way out", p.Projects)
	}
}

// The settings endpoint is readable by everyone so the page can explain why it
// is empty. `enabled` and `source` are that explanation; who administers this
// server and when they last did is not, and used to be handed to every
// authenticated user.
func TestSearchStatsSettings_DoesNotLeakTheAdminToRegularUsers(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	userCookie := seedUser(t, f.authTestFixture, adminCookie, "bob@example.com", "bobpass1234")

	// Make sure there IS something to leak.
	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": true}); rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d (%s)", rr.Code, body)
	}

	asAdmin := getSettings(t, f, adminCookie)
	if asAdmin.UpdatedBy != "admin@example.com" || asAdmin.UpdatedAt == "" {
		t.Errorf("admin sees updated_by=%q updated_at=%q, want both populated",
			asAdmin.UpdatedBy, asAdmin.UpdatedAt)
	}

	asUser := getSettings(t, f, userCookie)
	if !asUser.Enabled || asUser.Source != "database" {
		t.Errorf("user sees enabled=%v source=%q, want the state and its provenance",
			asUser.Enabled, asUser.Source)
	}
	if asUser.UpdatedBy != "" {
		t.Errorf("user sees updated_by=%q — that is an admin's email address", asUser.UpdatedBy)
	}
	if asUser.UpdatedAt != "" {
		t.Errorf("user sees updated_at=%q — when an admin last administered this server",
			asUser.UpdatedAt)
	}
}

// Discarding what was collected must not require switching collection back on.
// Otherwise "stop recording" and "delete what you recorded" are the same lever,
// and an admin has to resume the thing they stopped in order to clear it.
func TestSearchStats_ResetWorksWhileCollectionIsOff(t *testing.T) {
	f := newStatsFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	seedLocalProject(t, f, "/tmp/proj", f.UserID, 3, "a.go")

	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": false}); rr.Code != http.StatusOK {
		t.Fatalf("switch off = %d (%s)", rr.Code, body)
	}
	if rr, body := doReq(t, f.authTestFixture, adminCookie,
		http.MethodPost, "/api/v1/admin/search-stats/reset", nil); rr.Code != http.StatusNoContent {
		t.Fatalf("reset while off = %d (%s)", rr.Code, body)
	}

	// Back on: the counters really are gone, not merely hidden by the switch.
	if rr, body := doReq(t, f.authTestFixture, adminCookie, http.MethodPut,
		"/api/v1/admin/search-stats/settings", map[string]any{"enabled": true}); rr.Code != http.StatusOK {
		t.Fatalf("switch on = %d (%s)", rr.Code, body)
	}
	if p := getStats(t, f, adminCookie, ""); len(p.Projects) != 0 {
		t.Errorf("after a reset performed while off: %+v, want nothing", p.Projects)
	}
}
