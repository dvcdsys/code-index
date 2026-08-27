package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// Does recording measurably slow a search down, and does it fall over when many
// searches run at once?
//
// The counters sit on the critical path of the one operation this server exists
// to perform, and they are shared: every search on the box takes the same mutex.
// So the question is not only "what does one call cost" but "what does the Nth
// concurrent call cost".
//
// The two figures these numbers are measured against, both taken on this
// machine against the 45-repo / 1.9M-chunk load-test fixture and recorded in
// loadtests/SEARCH_PERF_CONTEXT.md:
//
//	single-project semantic search   1,422 ms p50
//	workspace search (45 repos)     10,544 ms p50
//
// Those are the latencies the overhead below has to be read against.

// heavyProject seeds a project with `files` indexed files, so the file-search
// handler does a real LIKE scan rather than returning from an empty table. It
// stands in for a large repository: the handler's own cost grows with the
// corpus while the recording cost does not, which is the relationship worth
// pinning.
func heavyProject(b *testing.B, f *statsFixture, hostPath string, files int) string {
	b.Helper()
	if _, err := projects.Create(context.Background(), f.Deps.DB, projects.CreateRequest{
		HostPath: hostPath, OwnerUserID: f.UserID,
	}); err != nil {
		b.Fatalf("create project: %v", err)
	}
	tx, err := f.Deps.DB.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO file_hashes (project_path, file_path, content_hash, indexed_at)
	                         VALUES (?, ?, 'h', datetime('now'))`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < files; i++ {
		// A tenth of the corpus matches the query below, so the handler
		// returns a realistic result set rather than one row or all of them.
		name := fmt.Sprintf("internal/pkg%d/module%d.go", i%64, i)
		if i%10 == 0 {
			name = fmt.Sprintf("internal/payments/handler%d.go", i)
		}
		if _, err := stmt.Exec(hostPath, name); err != nil {
			b.Fatalf("insert: %v", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
	return projects.HashPath(hostPath)
}

// searchRunner returns a closure that issues one authenticated file search.
func searchRunner(b *testing.B, f *statsFixture, hash string) func() {
	b.Helper()
	body := []byte(`{"query":"payments","limit":20}`)
	return func() {
		req := httptest.NewRequest(http.MethodPost,
			"/api/v1/projects/"+hash+"/search/files", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.FullKey)
		rr := httptest.NewRecorder()
		f.Router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("search = %d (%s)", rr.Code, rr.Body.String())
		}
	}
}

// benchSearch runs the same search with the recorder wired and unwired. The
// difference between the two sub-benchmarks IS the feature's cost — same
// router, same handler, same corpus, one field on Deps.
func benchSearch(b *testing.B, corpus int, parallel bool) {
	for _, stats := range []bool{false, true} {
		name := "stats=off"
		if stats {
			name = "stats=on"
		}
		b.Run(name, func(b *testing.B) {
			f := newStatsFixture(b)
			// Both sides are rebuilt, so the only difference between them is
			// the recorder. The discard logger is not cosmetic: the default
			// writes one structured line per request to stderr, which costs
			// more than the thing being measured and would bury it.
			d := f.Deps
			d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
			if !stats {
				d.SearchStats = nil
				d.SearchStatsWrite = nil
			}
			f.Deps = d
			f.Router = NewRouter(d)
			hash := heavyProject(b, f, "/bench/heavy", corpus)
			run := searchRunner(b, f, hash)

			b.ReportAllocs()
			b.ResetTimer()
			if parallel {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						run()
					}
				})
			} else {
				for i := 0; i < b.N; i++ {
					run()
				}
			}
		})
	}
}

// A small repository, where the handler is fast and the recording overhead has
// the least room to hide.
func BenchmarkSearchHandler_SmallRepo(b *testing.B)         { benchSearch(b, 500, false) }
func BenchmarkSearchHandler_HeavyRepo(b *testing.B)         { benchSearch(b, 50000, false) }
func BenchmarkSearchHandlerParallel_HeavyRepo(b *testing.B) { benchSearch(b, 50000, true) }

// Workspace search is the case with the most recording to do: one Record per
// project the fan-out scanned, so the cost scales with the workspace, not with
// the repositories in it. 45 is the load-test fixture; 100 is past anything
// that exists.
func BenchmarkRecordWorkspaceFanout(b *testing.B) {
	for _, projectCount := range []int{8, 45, 100} {
		b.Run(fmt.Sprintf("projects=%d", projectCount), func(b *testing.B) {
			f := newStatsFixture(b)
			srv := &Server{Deps: f.Deps}

			searched := make([]string, projectCount)
			surviving := make([]projectHits, 0, projectCount)
			for i := range searched {
				searched[i] = fmt.Sprintf("github.com/org/repo%d@main", i)
				// The per-project chunk cap is 5, and recording happens after
				// it, so this is the widest a real payload gets.
				chunks := make([]workspaceSearchChunkPayload, workspaceSearchPerProjChunkCap)
				for j := range chunks {
					chunks[j] = workspaceSearchChunkPayload{
						ProjectPath: searched[i],
						FilePath:    fmt.Sprintf("src/pkg%d/file%d.go", j, j),
					}
				}
				surviving = append(surviving, projectHits{
					ProjectPath: searched[i],
					FusedChunks: chunks,
				})
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				srv.recordWorkspaceSearch(searched, surviving)
			}
		})
	}
}
