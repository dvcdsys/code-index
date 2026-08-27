package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/access"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/searchstats"
)

// ---------------------------------------------------------------------------
// Search statistics — GET /api/v1/search-stats
//                     GET /api/v1/search-stats/series
//                     POST /api/v1/admin/search-stats/reset
//
// Access levels (see docs/AUTH_REVIEW.md):
//   - the two GETs are GroupRead. There is no per-resource helper to lean on
//     because the resource is a LIST spanning projects, so the gate is applied
//     the way ListProjects and WorkspaceSearch apply it: resolve the caller's
//     accessible host_paths and let the query see nothing else. An empty set
//     yields an empty table, never the whole server's numbers.
//   - the reset is Admin.
// ---------------------------------------------------------------------------

// searchStatsProjects is the per-request view of the projects table that the
// statistics endpoints need: which projects the caller may see, what to call
// them, and whether each is local or external.
type searchStatsProjects struct {
	// paths is the access-scoped, name-filtered set handed to the stats
	// database. Order is irrelevant — it becomes an IN list.
	paths []string
	// byPath carries display metadata for the page, keyed by host_path.
	byPath map[string]projects.Project
	// external is the subset with a git_repos peer.
	external map[string]struct{}
}

// resolveSearchStatsProjects applies the access gate and the name filter.
//
// The name filter runs HERE, against the projects table, rather than as a LIKE
// inside the statistics database. The statistics database stores only
// host_path — which for a local project is the namespaced identity key
// `local:{machine}:{abs_path}` — so a substring match there would be matching
// against a string the user has never seen. Narrowing the accessible set first
// means the filter matches what is on screen, and it still happens before
// pagination, which is what "server-side" has to mean for the page numbers to
// be right.
func (s *Server) resolveSearchStatsProjects(
	ctx context.Context, r *http.Request, nameFilter string,
) (*searchStatsProjects, error) {

	list, err := projects.List(ctx, s.Deps.DB)
	if err != nil {
		return nil, err
	}

	if userID, isAdmin := s.callerIdentity(r); !isAdmin {
		allowed, aerr := access.AccessibleProjectHostPaths(ctx, s.Deps.DB, userID)
		if aerr != nil {
			return nil, aerr
		}
		allowedSet := make(map[string]struct{}, len(allowed))
		for _, hp := range allowed {
			allowedSet[hp] = struct{}{}
		}
		filtered := list[:0]
		for _, p := range list {
			if _, ok := allowedSet[p.HostPath]; ok {
				filtered = append(filtered, p)
			}
		}
		list = filtered
	}

	// One statement for the whole external set rather than IsProjectExternal
	// per project — the alternative is N queries to render one page.
	external := make(map[string]struct{})
	rows, err := s.Deps.DB.QueryContext(ctx, `SELECT project_path FROM git_repos`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		external[p] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	needle := strings.ToLower(strings.TrimSpace(nameFilter))
	out := &searchStatsProjects{
		byPath:   make(map[string]projects.Project, len(list)),
		external: external,
	}
	for _, p := range list {
		if needle != "" &&
			!strings.Contains(strings.ToLower(p.DisplayPath), needle) &&
			!strings.Contains(strings.ToLower(p.HostPath), needle) {
			continue
		}
		out.byPath[p.HostPath] = p
		out.paths = append(out.paths, p.HostPath)
	}
	return out, nil
}

// searchStatsWindows maps the wire enum onto a duration. `all` is the zero
// value, which the store reads as "the cumulative tier".
var searchStatsWindows = map[string]time.Duration{
	"all": 0,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// parseSearchKinds turns the comma-separated `kinds` parameter into the set the
// store filters on.
//
// An unrecognised kind is DROPPED rather than rejected. The alternative is a
// 422 on a typo in a query string that only ever narrows a read, and dropping
// keeps the endpoint forward-compatible: an older server asked for a kind a
// newer one records simply has none of them. Returning nil — every kind — is
// correct when nothing recognisable was asked for, because that is also what an
// absent parameter means.
func parseSearchKinds(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	known := make(map[string]struct{}, len(searchstats.KnownKinds))
	for _, k := range searchstats.KnownKinds {
		known[k] = struct{}{}
	}
	var out []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(*raw, ",") {
		k := strings.ToLower(strings.TrimSpace(part))
		if _, ok := known[k]; !ok {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// GetSearchStats — GET /api/v1/search-stats.
func (s *Server) GetSearchStats(w http.ResponseWriter, r *http.Request, params openapi.GetSearchStatsParams) {
	store := s.Deps.SearchStats
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "search statistics are not enabled on this server")
		return
	}

	scope, err := s.resolveSearchStatsProjects(r.Context(), r, derefString(params.Project))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve visible projects: "+err.Error())
		return
	}

	window := "all"
	if params.Window != nil {
		window = string(*params.Window)
	}
	dur, ok := searchStatsWindows[window]
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "unknown window "+window)
		return
	}

	sortKey := searchstats.SortQueries
	if params.Sort != nil {
		sortKey = string(*params.Sort)
	}
	if !searchstats.ValidSort(sortKey) {
		writeError(w, http.StatusUnprocessableEntity, "unknown sort key "+sortKey)
		return
	}
	// Descending is the default because every column here is a "how much"
	// counter and the interesting end of one of those is the top.
	desc := true
	if params.Order != nil {
		desc = *params.Order != "asc"
	}

	q := searchstats.Query{
		ProjectPaths: scope.paths,
		Kinds:        parseSearchKinds(params.Kinds),
		Window:       dur,
		MinQueries:   params.MinQueries,
		MaxQueries:   params.MaxQueries,
		MinFileHits:  params.MinFileHits,
		MaxFileHits:  params.MaxFileHits,
		MinTopFile:   params.MinTopFileHits,
		MaxTopFile:   params.MaxTopFileHits,
		TopFiles:     clampInt(params.TopFiles, 5, 0, 20),
		Sort:         sortKey,
		Desc:         desc,
		Limit:        clampInt(params.Limit, 50, 1, 200),
		Offset:       clampInt(params.Offset, 0, 0, 1<<30),
	}

	page, err := store.ProjectStatsPage(r.Context(), q, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]openapi.SearchStatsProject, 0, len(page.Rows))
	for _, row := range page.Rows {
		item := openapi.SearchStatsProject{
			ProjectPath:   row.ProjectPath,
			Queries:       row.Queries,
			Results:       row.Results,
			FileHits:      row.FileHits,
			DistinctFiles: row.DistinctFiles,
			TopFileHits:   row.TopFileHits,
			LastSeen:      row.LastSeen,
			TopFiles:      make([]openapi.SearchStatsFile, 0, len(row.TopFiles)),
		}
		if p, ok := scope.byPath[row.ProjectPath]; ok {
			item.Exists = true
			item.PathHash = p.PathHash
			item.Name = p.DisplayPath
			kind := openapi.Local
			if _, isExternal := scope.external[p.HostPath]; isExternal {
				kind = openapi.External
			}
			item.Kind = &kind
		}
		for _, f := range row.TopFiles {
			item.TopFiles = append(item.TopFiles, openapi.SearchStatsFile{
				FilePath: f.FilePath,
				Hits:     f.Hits,
			})
		}
		out = append(out, item)
	}

	// Asked as its own question rather than derived from the page. The page is
	// both paginated and filtered, so counting the projects missing from it
	// would report anything excluded by a min_queries filter as never
	// searched — which is a different, and wrong, statement.
	activeCount, err := store.ActiveProjectCount(r.Context(), scope.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	idle := len(scope.paths) - activeCount
	if idle < 0 {
		idle = 0
	}

	writeJSON(w, http.StatusOK, openapi.SearchStatsResponse{
		Projects:                out,
		Total:                   page.Total,
		Window:                  window,
		BucketSeconds:           searchstats.BucketSeconds,
		RetentionSeconds:        int(searchstats.WindowRetention / time.Second),
		ProjectsWithoutActivity: &idle,
		Totals: &openapi.SearchStatsTotals{
			Queries: page.TotalQueries,
			Results: page.TotalResults,
		},
	})
}

// GetSearchStatsSeries — GET /api/v1/search-stats/series.
func (s *Server) GetSearchStatsSeries(w http.ResponseWriter, r *http.Request, params openapi.GetSearchStatsSeriesParams) {
	store := s.Deps.SearchStats
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "search statistics are not enabled on this server")
		return
	}

	scope, err := s.resolveSearchStatsProjects(r.Context(), r, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve visible projects: "+err.Error())
		return
	}

	paths := scope.paths
	if params.ProjectHash != nil && *params.ProjectHash != "" {
		// Resolved through the caller's own accessible set rather than by a
		// direct lookup, so an unauthorised hash is indistinguishable from a
		// nonexistent one — a 404 either way, leaking nothing about whether
		// the project exists.
		var match string
		for _, p := range scope.paths {
			if scope.byPath[p].PathHash == *params.ProjectHash {
				match = p
				break
			}
		}
		if match == "" {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		paths = []string{match}
	}

	window := "7d"
	if params.Window != nil {
		window = string(*params.Window)
	}
	dur, ok := searchStatsWindows[window]
	if !ok || dur <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "unknown window "+window)
		return
	}

	points, err := store.Series(r.Context(), searchstats.Query{
		ProjectPaths: paths,
		Kinds:        parseSearchKinds(params.Kinds),
		Window:       dur,
	}, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]openapi.SearchStatsSeriesPoint, 0, len(points))
	for _, p := range points {
		out = append(out, openapi.SearchStatsSeriesPoint{Bucket: p.Bucket, Queries: p.Queries})
	}
	writeJSON(w, http.StatusOK, openapi.SearchStatsSeriesResponse{
		Points:        out,
		BucketSeconds: searchstats.BucketSeconds,
		WindowSeconds: int(dur / time.Second),
	})
}

// ResetSearchStats — POST /api/v1/admin/search-stats/reset.
func (s *Server) ResetSearchStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	store := s.Deps.SearchStats
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "search statistics are not enabled on this server")
		return
	}
	if err := store.Reset(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
