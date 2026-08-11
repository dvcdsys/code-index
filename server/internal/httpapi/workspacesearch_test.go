package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/chunksfts"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// fixedEmbedder is the stub query embedder. Workspace search calls
// EmbedQuery once before the fan-out; tests need a vector with the
// same dim as the seeded chunks, so the embedding is wired per-test
// via a struct field rather than baked into the type.
type fixedEmbedder struct {
	q []float32
}

func (e fixedEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return e.q, nil
}
func (e fixedEmbedder) Ready(_ context.Context) error { return nil }

// newSearchRouter wires the minimum surface workspace search needs:
// workspaces, git_repos, workspace_projects, jobs (unused but in Deps),
// vectorstore (real, on tmpdir), and a query embedder the caller
// controls.
func newSearchRouter(t *testing.T, d *sql.DB, vs *vectorstore.Store, emb fixedEmbedder) http.Handler {
	t.Helper()
	t.Setenv("CIX_SECRET_KEY", "")
	t.Setenv("CIX_SECRET_KEYFILE", "")
	sec, err := secrets.Open(secrets.OpenOptions{DataDir: t.TempDir(), AllowGenerate: true})
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	return NewRouter(Deps{
		DB:                d,
		AuthDisabled:      true,
		Users:             seedlessUsers(d),
		Sessions:          seedlessSessions(d),
		APIKeys:           seedlessAPIKeys(d),
		Workspaces:        workspaces.New(d),
		GithubTokens:      githubtokens.New(d, sec),
		GitRepos:          gitrepos.New(d),
		WorkspaceProjects: workspaceprojects.New(d),
		Jobs:              jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour}),
		VectorStore:       vs,
		EmbeddingSvc:      emb,
	})
}

// seedRepoWithChunks inserts a projects row + workspace_projects
// membership for the given project_path inside the workspace, then
// upserts the supplied chunks into chromem so /search has something to
// retrieve. Bypasses the clone+index job chain — those are exercised
// in gitrepos tests already.
func seedRepoWithChunks(
	t *testing.T,
	d *sql.DB,
	vs *vectorstore.Store,
	wsID, projectPath string,
	chunks []vectorstore.Chunk,
	embeddings [][]float32,
) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(
		`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		 VALUES (?, ?, '[]', '{}', '{}', 'indexed', ?, ?, ?)`,
		projectPath, projectPath, now, now, projects.HashPath(projectPath),
	); err != nil {
		t.Fatalf("insert project %q: %v", projectPath, err)
	}
	if _, err := d.Exec(
		`INSERT INTO workspace_projects (workspace_id, project_path, added_at)
		 VALUES (?, ?, ?)`,
		wsID, projectPath, now,
	); err != nil {
		t.Fatalf("insert workspace_project %q: %v", projectPath, err)
	}
	_ = uuid.NewString() // keep import in case future tests need it
	if err := vs.UpsertChunks(context.Background(), projectPath, chunks, embeddings); err != nil {
		t.Fatalf("upsert chunks for %q: %v", projectPath, err)
	}
	// Mirror the production indexer: every chunk that lands in
	// chromem also gets written to chunks_fts so the BM25 side of
	// workspace search has data. Grouped by file so the per-file
	// upsert path is exercised the same way as in the indexer.
	byFile := map[string][]chunksfts.Chunk{}
	for _, c := range chunks {
		byFile[c.FilePath] = append(byFile[c.FilePath], chunksfts.Chunk{
			Content:    c.Content,
			FilePath:   c.FilePath,
			StartLine:  c.StartLine,
			EndLine:    c.EndLine,
			ChunkType:  c.ChunkType,
			SymbolName: c.SymbolName,
			Language:   c.Language,
		})
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	for fp, cs := range byFile {
		if err := chunksfts.UpsertByFileTx(context.Background(), tx, projectPath, fp, cs); err != nil {
			t.Fatalf("chunksfts upsert %q: %v", fp, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit chunksfts tx: %v", err)
	}
}

// l2 returns v scaled to unit length. chromem expects normalized
// vectors for clean cosine similarity; pre-normalising saves the
// re-norm chromem does internally and makes scores predictable.
func l2(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	scale := float32(1.0 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * scale
	}
	return out
}

func openTestVectorStore(t *testing.T) *vectorstore.Store {
	t.Helper()
	vs, err := vectorstore.Open(filepath.Join(t.TempDir(), "chroma"))
	if err != nil {
		t.Fatalf("vectorstore: %v", err)
	}
	return vs
}

type searchProjectResp struct {
	ProjectPath  string  `json:"project_path"`
	Label        string  `json:"label"`
	ProjectScore float32 `json:"project_score"`
	NumHits      int     `json:"num_hits"`
	BM25Score    float32 `json:"bm25_score"`
	DenseScore   float32 `json:"dense_score"`
}

type searchChunkResp struct {
	ProjectPath string  `json:"project_path"`
	FilePath    string  `json:"file_path"`
	StartLine   int     `json:"start_line"`
	EndLine     int     `json:"end_line"`
	SymbolName  string  `json:"symbol_name,omitempty"`
	Score       float32 `json:"score"`
	Content     string  `json:"content"`
}

type searchPendingResp struct {
	ProjectPath string `json:"project_path"`
	Status      string `json:"status"`
}

type searchFailedResp struct {
	ProjectPath string `json:"project_path"`
	Reason      string `json:"reason"`
}

type searchResp struct {
	Status        string                `json:"status"`
	Projects      []searchProjectResp   `json:"projects"`
	Chunks        []searchChunkResp     `json:"chunks"`
	PendingRepos  []searchPendingResp   `json:"pending_repos,omitempty"`
	FailedRepos   []searchFailedResp    `json:"failed_repos,omitempty"`
	StaleFTSRepos []searchStaleFTSRepoR `json:"stale_fts_repos,omitempty"`
}

type searchStaleFTSRepoR struct {
	ProjectPath string `json:"project_path"`
}

// TestWorkspaceSearch_EmptyWorkspace covers the no-repos case — the
// handler must return a well-formed response with empty arrays rather
// than a 500 or a misleading "ok" status. Mirrors what the dashboard
// expects when an operator opens search on a fresh workspace.
func TestWorkspaceSearch_EmptyWorkspace(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "empty")

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=anything", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "empty" {
		t.Fatalf("expected status=empty, got %q", resp.Status)
	}
	if len(resp.Projects) != 0 || len(resp.Chunks) != 0 {
		t.Fatalf("expected empty arrays, got %+v", resp)
	}
}

// TestWorkspaceSearch_ProjectsRankByMeanNotCount verifies the change
// motivated by a workspace where one large repo (many hits, mean
// ≈ 0.5) drowned out a smaller-but-equally-relevant repo (few hits,
// mean ≈ 0.41) with the previous mean×log(1+N_hits) formula. The new
// pure-mean project_score keeps these projects close together so
// both surface in the panel, and the per-project chunk cap stops the
// higher-count project from monopolising the chunks list.
func TestWorkspaceSearch_ProjectsRankByMeanNotCount(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)

	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "rank")

	// "Big" project — many marginal hits. Top mean comes out small
	// because chunks past the first few are well off-axis.
	bigChunks := make([]vectorstore.Chunk, 10)
	bigEmbs := make([][]float32, 10)
	for i := range bigChunks {
		bigChunks[i] = vectorstore.Chunk{
			Content: "b", FilePath: "b.go",
			StartLine: i*10 + 1, EndLine: i*10 + 9,
			ChunkType: "function", SymbolName: "B", Language: "go",
		}
		// X coefficient decays from 0.6 → 0.15 so the top-5 mean is
		// well below the "small" project's top-1 score.
		bigEmbs[i] = l2([]float32{0.6 - 0.05*float32(i), 0.4, 0.0, 0.0})
	}
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/big@main", bigChunks, bigEmbs)

	// "Small" project — one very strong hit, nothing else. Top-N
	// mean is dominated by that one chunk.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/small@main",
		[]vectorstore.Chunk{
			{Content: "s", FilePath: "s.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "S", Language: "go"},
		},
		[][]float32{l2([]float32{1.0, 0.0, 0.0, 0.0})},
	)

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "ok" {
		t.Fatalf("expected status=ok, got %q (body=%s)", resp.Status, rr.Body.String())
	}
	if len(resp.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d (%+v)", len(resp.Projects), resp.Projects)
	}
	// Small project (mean ≈ 1.0) must beat big project (mean of
	// top-5 well below 1.0) — i.e. count alone doesn't determine
	// rank.
	if resp.Projects[0].ProjectPath != "github.com/o/small@main" {
		t.Fatalf("strong-mean project should rank first, got %q (scores=%v)",
			resp.Projects[0].ProjectPath,
			[]float32{resp.Projects[0].ProjectScore, resp.Projects[1].ProjectScore})
	}

	// Chunks are sorted by raw score (no boost), so the small
	// project's strongest hit must lead.
	if len(resp.Chunks) == 0 {
		t.Fatalf("expected chunks, got none")
	}
	if resp.Chunks[0].ProjectPath != "github.com/o/small@main" {
		t.Fatalf("top chunk should be the small project's strong hit, got %+v", resp.Chunks[0])
	}
	for i := 1; i < len(resp.Chunks); i++ {
		if resp.Chunks[i-1].Score < resp.Chunks[i].Score {
			t.Fatalf("chunks not sorted by score at i=%d: %v < %v",
				i, resp.Chunks[i-1].Score, resp.Chunks[i].Score)
		}
	}
}

// TestWorkspaceSearch_PerProjectChunkCap is the regression for the
// "one repo eats every slot" failure mode where a dominant repo's
// hits crowded out every other surviving project from the chunks
// list. Seeds one project with 12 strong hits and another with 1;
// the global chunks list must not exceed the per-project cap for
// the dominant repo.
func TestWorkspaceSearch_PerProjectChunkCap(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "cap")

	// 12 strong chunks in one project — slightly varying scores so
	// they sort cleanly, all well above the small project's hits.
	bigChunks := make([]vectorstore.Chunk, 12)
	bigEmbs := make([][]float32, 12)
	for i := range bigChunks {
		bigChunks[i] = vectorstore.Chunk{
			Content: "b", FilePath: "b.go",
			StartLine: i*10 + 1, EndLine: i*10 + 9,
			ChunkType: "function", SymbolName: "B", Language: "go",
		}
		bigEmbs[i] = l2([]float32{1.0 - 0.01*float32(i), 0.0, 0.0, 0.0})
	}
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/big@main", bigChunks, bigEmbs)

	// One smaller-magnitude chunk in the other project. Its score
	// (~0.5) is below every chunk from the big project, so without
	// the per-project cap it would never make it into top-20.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/small@main",
		[]vectorstore.Chunk{
			{Content: "s", FilePath: "s.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "S", Language: "go"},
		},
		[][]float32{l2([]float32{0.5, 0.5, 0.0, 0.0})},
	)

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	byProject := map[string]int{}
	for _, c := range resp.Chunks {
		byProject[c.ProjectPath]++
	}
	if byProject["github.com/o/big@main"] > 5 {
		t.Fatalf("per-project chunk cap violated: big project got %d slots (cap=5)",
			byProject["github.com/o/big@main"])
	}
	if byProject["github.com/o/small@main"] != 1 {
		t.Fatalf("small project should contribute its one chunk despite lower score; got %+v",
			byProject)
	}
}

// TestWorkspaceSearch_MinScoreFilter verifies the optional
// `?min_score=` query parameter. Default is 0 (everything chromem
// returns is fair game) — set higher to drop low-relevance projects
// from the response.
// TestWorkspaceSearch_MinScoreDropsLowScoringChunks verifies that the
// optional `?min_score=` query param filters the dense list before
// candidacy aggregation: chunks below the floor are excluded from
// dense_signal, which can push a marginal project's candidacy under
// the per-query relative threshold and drop it from the response.
func TestWorkspaceSearch_MinScoreDropsLowScoringChunks(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "minscore")

	// Strong-hit project at cosine ≈ 1.0.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/good@main",
		[]vectorstore.Chunk{
			{Content: "g", FilePath: "g.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "G", Language: "go"},
		},
		[][]float32{l2([]float32{1.0, 0.0, 0.0, 0.0})},
	)
	// Weak-hit project at cosine ≈ 0.45 (above the natural cosine
	// floor chromem applies but well below 0.5).
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/weak@main",
		[]vectorstore.Chunk{
			{Content: "w", FilePath: "w.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "W", Language: "go"},
		},
		[][]float32{l2([]float32{0.45, 0.8, 0.0, 0.0})}, // cosine with q ≈ 0.49
	)

	// min_score=0.5: weak project's only chunk drops, dense_signal=0
	// → candidacy=0 → filtered by the project gate. Only good survives.
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x&min_score=0.5", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var filteredResp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &filteredResp)
	if len(filteredResp.Projects) != 1 {
		t.Fatalf("min_score=0.5: expected 1 project (good only), got %d (%+v)",
			len(filteredResp.Projects), filteredResp.Projects)
	}
	if filteredResp.Projects[0].ProjectPath != "github.com/o/good@main" {
		t.Fatalf("wrong project after filter: %+v", filteredResp.Projects[0])
	}
}

// TestWorkspaceSearch_ProjectGateDropsDeadWeightRepos is the regression
// for the pre-hybrid failure mode that motivated this redesign: in a
// multi-repo workspace, repos with zero literal mentions of the query
// term still surfaced 50 chunks each at noise-level cosine similarity.
// The hybrid gate must drop projects whose normalised candidacy falls
// below 40% of the best project's candidacy, regardless of how many
// chunks chromem happily returned.
func TestWorkspaceSearch_ProjectGateDropsDeadWeightRepos(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "gate")

	// Strong project: 5 chunks all close to the query.
	strongChunks := []vectorstore.Chunk{}
	strongEmbs := [][]float32{}
	for i := 0; i < 5; i++ {
		strongChunks = append(strongChunks, vectorstore.Chunk{
			Content: "strong content", FilePath: "s.go",
			StartLine: i*10 + 1, EndLine: i*10 + 9, ChunkType: "function", Language: "go",
		})
		strongEmbs = append(strongEmbs, l2([]float32{1.0 - 0.01*float32(i), 0.0, 0.0, 0.0}))
	}
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/strong@main", strongChunks, strongEmbs)

	// Dead-weight project: 5 chunks all orthogonal to the query
	// (cosine == 0) and content that shares no token with the query.
	deadChunks := []vectorstore.Chunk{}
	deadEmbs := [][]float32{}
	for i := 0; i < 5; i++ {
		deadChunks = append(deadChunks, vectorstore.Chunk{
			Content: "totally unrelated material", FilePath: "d.go",
			StartLine: i*10 + 1, EndLine: i*10 + 9, ChunkType: "function", Language: "go",
		})
		deadEmbs = append(deadEmbs, l2([]float32{0.0, 1.0, 0.0, 0.0}))
	}
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/dead@main", deadChunks, deadEmbs)

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=strong", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Projects) != 1 {
		t.Fatalf("expected only strong project to survive gate, got %d (%+v)",
			len(resp.Projects), resp.Projects)
	}
	if resp.Projects[0].ProjectPath != "github.com/o/strong@main" {
		t.Fatalf("wrong project survived: %+v", resp.Projects[0])
	}
	for _, c := range resp.Chunks {
		if c.ProjectPath == "github.com/o/dead@main" {
			t.Errorf("dead project chunk leaked into output: %+v", c)
		}
	}
}

// TestWorkspaceSearch_FlagsStaleFTSRepos exercises the pre-FTS5-mirror
// detection. We seed a project the way the old indexer used to —
// chromem + file_hashes populated, chunks_meta left empty — and
// verify the response calls it out in stale_fts_repos.
func TestWorkspaceSearch_FlagsStaleFTSRepos(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "stale")

	// Seed a normal repo (chunks_fts populated via the helper).
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/fresh@main",
		[]vectorstore.Chunk{
			{Content: "needle here", FilePath: "f.go", StartLine: 1, EndLine: 9, ChunkType: "function", Language: "go"},
		},
		[][]float32{l2([]float32{1.0, 0.0, 0.0, 0.0})},
	)

	// Simulate a pre-FTS5-mirror repo: insert project + workspace_repo
	// + chromem chunk + file_hashes row, but skip chunks_fts/meta.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stalePath := "github.com/o/stale@main"
	if _, err := d.Exec(
		`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, last_indexed_at, path_hash)
		 VALUES (?, ?, '[]', '{}', '{}', 'indexed', ?, ?, ?, ?)`,
		stalePath, stalePath, now, now, now, projects.HashPath(stalePath),
	); err != nil {
		t.Fatalf("insert stale project: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO workspace_projects (workspace_id, project_path, added_at)
		 VALUES (?, ?, ?)`, wsID, stalePath, now,
	); err != nil {
		t.Fatalf("insert stale workspace_projects: %v", err)
	}
	_ = uuid.NewString()
	if err := vs.UpsertChunks(context.Background(), stalePath,
		[]vectorstore.Chunk{{Content: "stale chunk", FilePath: "s.go", StartLine: 1, EndLine: 9, Language: "go"}},
		[][]float32{l2([]float32{0.9, 0.1, 0.0, 0.0})},
	); err != nil {
		t.Fatalf("upsert stale chromem chunks: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO file_hashes (project_path, file_path, content_hash, indexed_at)
		 VALUES (?, 's.go', 'hash', ?)`,
		stalePath, now,
	); err != nil {
		t.Fatalf("insert stale file_hashes: %v", err)
	}

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=needle", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.StaleFTSRepos) != 1 || resp.StaleFTSRepos[0].ProjectPath != stalePath {
		t.Fatalf("expected stale_fts_repos to flag %q, got %+v", stalePath, resp.StaleFTSRepos)
	}
	// Sanity: the fresh repo must NOT appear in the stale list.
	for _, s := range resp.StaleFTSRepos {
		if s.ProjectPath == "github.com/o/fresh@main" {
			t.Errorf("fresh repo wrongly flagged as stale: %+v", s)
		}
	}
}

// TestWorkspaceSearch_BM25SurfacesLiteralTokenDenseMissed seeds two
// projects with content the dense embedder ranks identically (we use
// orthogonal vectors so cosine == 0 for both) but where one project
// contains the literal query token. BM25 must promote the literal
// match into the surviving set even though dense gives zero signal.
func TestWorkspaceSearch_BM25SurfacesLiteralTokenDenseMissed(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "bm25-only")

	// Project A: content literally contains "needle"; embedding
	// orthogonal to the query so dense sees nothing.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/literal@main",
		[]vectorstore.Chunk{
			{Content: "the needle is buried here", FilePath: "a.go", StartLine: 1, EndLine: 9, ChunkType: "function", Language: "go"},
		},
		[][]float32{l2([]float32{0.0, 1.0, 0.0, 0.0})},
	)
	// Project B: content unrelated, also orthogonal.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/unrelated@main",
		[]vectorstore.Chunk{
			{Content: "haystack of unrelated material", FilePath: "b.go", StartLine: 1, EndLine: 9, ChunkType: "function", Language: "go"},
		},
		[][]float32{l2([]float32{0.0, 0.0, 1.0, 0.0})},
	)

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=needle", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Projects) != 1 || resp.Projects[0].ProjectPath != "github.com/o/literal@main" {
		t.Fatalf("BM25 should have surfaced only the literal-match project, got %+v", resp.Projects)
	}
	// bm25_score in the response rounds to 4 decimals; with only
	// 2 chunks in the FTS corpus the IDF term collapses near zero so
	// we don't assert a magnitude here — surfacing the chunk at all
	// is what proves the BM25 path fired (dense was orthogonal in
	// both projects so any non-zero ranking signal came from BM25).
	if len(resp.Chunks) != 1 || resp.Chunks[0].FilePath != "a.go" {
		t.Errorf("expected the literal-match chunk in output, got %+v", resp.Chunks)
	}
}

// TestWorkspaceSearch_RRFFusesBothSides exercises the per-project
// RRF: one chunk wins on dense, a different chunk wins on BM25.
// The fused order must put both ahead of a dense-only third chunk
// that has neither distinction.
func TestWorkspaceSearch_RRFFusesBothSides(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "rrf")

	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/fuse@main",
		[]vectorstore.Chunk{
			// dense-strong, no token match
			{Content: "the alpha chunk", FilePath: "f.go", StartLine: 1, EndLine: 9, ChunkType: "function", Language: "go"},
			// dense-weak, contains query token
			{Content: "needle goes here", FilePath: "f.go", StartLine: 11, EndLine: 19, ChunkType: "function", Language: "go"},
			// dense-medium, no token match
			{Content: "the beta filler", FilePath: "f.go", StartLine: 21, EndLine: 29, ChunkType: "function", Language: "go"},
		},
		[][]float32{
			l2([]float32{1.0, 0.0, 0.0, 0.0}), // cosine 1.0 with query
			l2([]float32{0.0, 1.0, 0.0, 0.0}), // cosine 0
			l2([]float32{0.6, 0.8, 0.0, 0.0}), // cosine 0.6
		},
	)

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=needle", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Chunks) < 2 {
		t.Fatalf("expected >= 2 chunks for the project, got %d", len(resp.Chunks))
	}

	// The first two slots must contain the BM25 winner (lines 11-19)
	// and the dense winner (lines 1-9). Order between those two is
	// implementation-dependent; we just require both appear before
	// the filler (lines 21-29).
	top2 := map[string]bool{}
	for _, c := range resp.Chunks[:2] {
		key := strconv.Itoa(c.StartLine) + "-" + strconv.Itoa(c.EndLine)
		top2[key] = true
	}
	if !top2["1-9"] || !top2["11-19"] {
		t.Errorf("RRF should fuse dense-winner (1-9) and bm25-winner (11-19) into top-2; got %+v",
			resp.Chunks[:2])
	}
}

// TestWorkspaceSearch_ParallelFanoutNoRace seeds many small projects
// in one workspace and runs the search. The point is to stress the
// errgroup-bounded fan-out under `go test -race`: any concurrent
// write to the shared results slice without the mutex shows up here.
//
// 32 projects is well above typical NumCPU so the semaphore actually
// queues some goroutines, not just runs them all at once on a fast
// machine.
func TestWorkspaceSearch_ParallelFanoutNoRace(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "race")

	const projects = 32
	for i := 0; i < projects; i++ {
		pp := "github.com/race/p" + strconv.Itoa(i) + "@main"
		seedRepoWithChunks(t, d, vs, wsID, pp,
			[]vectorstore.Chunk{
				{Content: "c", FilePath: "f.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "Fn", Language: "go"},
			},
			[][]float32{l2([]float32{1.0, 0.01 * float32(i), 0.0, 0.0})},
		)
	}

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "ok" {
		t.Fatalf("expected status=ok, got %q", resp.Status)
	}
	// Every seeded project hits exactly one chunk above threshold —
	// the projects list is capped at the default 10, so we expect
	// exactly that.
	if len(resp.Projects) != 10 {
		t.Fatalf("expected 10 projects (top_projects cap), got %d", len(resp.Projects))
	}
}

func TestWorkspaceSearch_MissingQueryReturns422(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "platform")
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=", nil)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on missing q, got %d", rr.Code)
	}
}

func TestWorkspaceSearch_NotFoundWorkspace(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/no-such-id/search?q=anything", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// TestWorkspaceSearch_RejectsEmptyEmbedding covers the early-bail
// guard for misbehaving embedders. Without it a zero-length vector
// would propagate to every per-project chromem call and produce
// failed_repos the size of the workspace, masking the real problem.
func TestWorkspaceSearch_RejectsEmptyEmbedding(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: []float32{}})
	wsID := createWS(t, router, "empty-emb")
	// Need at least one indexed repo, otherwise the early empty-
	// workspace path short-circuits before we hit the embedder check
	// in the order it currently runs.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/r@main",
		[]vectorstore.Chunk{
			{Content: "x", FilePath: "x.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "X", Language: "go"},
		},
		[][]float32{l2([]float32{1.0, 0.0, 0.0, 0.0})},
	)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for empty embedding, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestWorkspaceSearch_Disabled(t *testing.T) {
	router := workspaceRouter(t, false)
	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/any/search?q=x", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

// seedPendingRepo inserts a projects row with a non-`indexed` status
// AND a workspace_projects membership row. Mirrors what the DB looks
// like while clone/index jobs are still in flight (project registered,
// workspace_projects already attached, status not yet 'indexed').
func seedPendingRepo(t *testing.T, d *sql.DB, wsID, projectPath, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(
		`INSERT INTO projects (
			host_path, container_path, languages, settings, stats,
			status, created_at, updated_at, path_hash
		) VALUES (?, ?, '[]', '{}', '{}', ?, ?, ?, 'h')`,
		projectPath, projectPath, status, now, now,
	); err != nil {
		t.Fatalf("insert pending project %q: %v", projectPath, err)
	}
	if _, err := d.Exec(
		`INSERT INTO workspace_projects (workspace_id, project_path, added_at)
		 VALUES (?, ?, ?)`, wsID, projectPath, now,
	); err != nil {
		t.Fatalf("insert workspace_projects %q: %v", projectPath, err)
	}
	_ = uuid.NewString()
}

// TestWorkspaceSearch_SurfacesPendingRepos verifies that projects whose
// projects.status ≠ 'indexed' are reported back in `pending_repos`
// instead of being silently dropped. The dashboard uses this to render
// a "still indexing" banner — without it the operator sees a partial
// result set with no hint that anything's missing.
func TestWorkspaceSearch_SurfacesPendingRepos(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "pending")

	// One indexed repo with a strong hit — must still surface in the
	// happy-path projects/chunks panels.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/ready@main",
		[]vectorstore.Chunk{
			{Content: "ready", FilePath: "r.go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "R", Language: "go"},
		},
		[][]float32{l2([]float32{1.0, 0.0, 0.0, 0.0})},
	)

	// Two repos still in flight — different statuses to make sure
	// every non-indexed value propagates verbatim.
	seedPendingRepo(t, d, wsID, "github.com/o/cloning@main", "cloning")
	seedPendingRepo(t, d, wsID, "github.com/o/indexing@main", "indexing")

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	if resp.Status != "ok" {
		t.Fatalf("expected status=ok (indexed repo had a hit), got %q", resp.Status)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].ProjectPath != "github.com/o/ready@main" {
		t.Fatalf("expected exactly the indexed repo in projects, got %+v", resp.Projects)
	}
	if len(resp.PendingRepos) != 2 {
		t.Fatalf("expected 2 pending repos, got %d (%+v)", len(resp.PendingRepos), resp.PendingRepos)
	}
	gotStatuses := map[string]string{}
	for _, p := range resp.PendingRepos {
		gotStatuses[p.ProjectPath] = p.Status
	}
	if gotStatuses["github.com/o/cloning@main"] != "cloning" {
		t.Fatalf("cloning repo lost its status: %+v", resp.PendingRepos)
	}
	if gotStatuses["github.com/o/indexing@main"] != "indexing" {
		t.Fatalf("indexing repo lost its status: %+v", resp.PendingRepos)
	}
}

// TestWorkspaceSearch_AllPendingReturnsEmpty covers the all-repos-
// still-indexing case. The handler should skip the fan-out entirely
// (chromem would just return nil per missing collection) and let the
// client know via pending_repos that the workspace is alive but not
// yet ready.
func TestWorkspaceSearch_AllPendingReturnsEmpty(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "all-pending")

	seedPendingRepo(t, d, wsID, "github.com/o/p1@main", "pending")
	seedPendingRepo(t, d, wsID, "github.com/o/p2@main", "cloning")

	rr := doJSON(t, router, http.MethodGet, "/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "empty" {
		t.Fatalf("expected status=empty, got %q", resp.Status)
	}
	if len(resp.PendingRepos) != 2 {
		t.Fatalf("expected 2 pending repos, got %d", len(resp.PendingRepos))
	}
	if len(resp.Projects) != 0 || len(resp.Chunks) != 0 {
		t.Fatalf("expected empty projects/chunks, got %+v", resp)
	}
}

// TestWorkspaceSearch_ClampsParams covers the trio of guard rails for
// `top_projects` / `top_chunks` / `min_score`. The OpenAPI spec
// declares min/max but oapi-codegen doesn't enforce them at runtime —
// the handler must clamp itself to keep negative slice indices, zero
// loop bounds, and absurd map allocations from leaking through.
func TestWorkspaceSearch_ClampsParams(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "clamp")

	// Seed three projects so a clamped top_projects=1 has something
	// to bite. Each has one strong chunk on the x-axis so they all
	// rank above the min_score floor.
	for i, name := range []string{"a", "b", "c"} {
		// Slight x/y mix so no chunk sits at cosine=1.0 exactly —
		// the min_score=5 (clamped to 1) assertion below relies on
		// the floor being unreachable.
		seedRepoWithChunks(t, d, vs, wsID, "github.com/o/"+name+"@main",
			[]vectorstore.Chunk{
				{Content: name, FilePath: name + ".go", StartLine: 1, EndLine: 9, ChunkType: "function", SymbolName: "S", Language: "go"},
			},
			[][]float32{l2([]float32{0.9 - 0.01*float32(i), 0.1, 0.0, 0.0})},
		)
	}

	// top_projects=-5 used to slice projects[:-5] and panic. Clamped
	// to 1 it must return exactly one project.
	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&top_projects=-5", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("top_projects=-5: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Projects) != 1 {
		t.Fatalf("top_projects=-5 should clamp to 1 project, got %d", len(resp.Projects))
	}

	// top_chunks=0 used to break the merge loop immediately. Clamped
	// to 1 → at least one chunk surfaces.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&top_chunks=0", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("top_chunks=0: expected 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Chunks) == 0 {
		t.Fatalf("top_chunks=0 should clamp to 1, got empty chunks")
	}

	// top_chunks=999_999_999 must not blow allocations. Clamped to
	// 200, so the response has at most 200 chunks (here far fewer).
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&top_chunks=999999999", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("top_chunks=huge: expected 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Chunks) > 200 {
		t.Fatalf("top_chunks=huge should clamp to 200, got %d", len(resp.Chunks))
	}

	// min_score=-1 → clamped to 0 → every chunk we seeded survives.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&min_score=-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("min_score=-1: expected 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Chunks) == 0 {
		t.Fatalf("min_score=-1 should clamp to 0 and keep chunks, got none")
	}

	// min_score=5 → clamped to 1 → no chunk has cosine=1 exactly here
	// (each seeded chunk loses a tiny bit to the per-i decay), so the
	// response should be empty.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&min_score=5", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("min_score=5: expected 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "empty" {
		t.Fatalf("min_score=5 should clamp to 1 and return empty, got status=%q (%d chunks)",
			resp.Status, len(resp.Chunks))
	}
}

// TestWorkspaceSearch_ChunksOnlyFromPanelProjects guards against the
// `interleaveByRank` vs `projects[]` panel inconsistency: when more
// projects survive the gate than `top_projects` allows in the panel,
// chunks must only come from projects that are actually visible in
// the panel. Otherwise agents see a chunk with a project_path they
// can't look up for bm25_score/dense_score.
func TestWorkspaceSearch_ChunksOnlyFromPanelProjects(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: l2([]float32{1, 0, 0, 0})})
	wsID := createWS(t, router, "panel-vs-chunks")

	// Seed 12 projects with monotonically decreasing strength. All
	// stay above the 0.4*best gate (relative threshold). With
	// top_projects=10 the bottom 2 projects must be dropped from the
	// panel AND from chunks[].
	for i := 0; i < 12; i++ {
		name := "p" + strconv.Itoa(i)
		// First component decays slowly so cosine spread is gentle
		// (0.99 down to ~0.85) — every project survives 0.4 × best.
		x := float32(0.99) - 0.012*float32(i)
		seedRepoWithChunks(t, d, vs, wsID, "github.com/o/"+name+"@main",
			[]vectorstore.Chunk{
				{Content: "rate limit middleware " + name, FilePath: name + ".go",
					StartLine: 1, EndLine: 9, ChunkType: "function",
					SymbolName: "S", Language: "go"},
			},
			[][]float32{l2([]float32{x, 0.1, 0.0, 0.0})},
		)
	}

	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=rate+limit&top_projects=10&min_score=0", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Projects) != 10 {
		t.Fatalf("expected 10 projects in panel (top_projects=10), got %d", len(resp.Projects))
	}
	panel := make(map[string]struct{}, len(resp.Projects))
	for _, p := range resp.Projects {
		panel[p.ProjectPath] = struct{}{}
	}
	for i, c := range resp.Chunks {
		if _, ok := panel[c.ProjectPath]; !ok {
			t.Fatalf("chunk[%d] is from project %q which is NOT in projects[] panel "+
				"(panel has %d entries: %+v)", i, c.ProjectPath, len(resp.Projects), panel)
		}
	}
}

// TestWorkspaceSearch_DefaultMinScoreIs04 verifies the documented
// default has effect: a request without ?min_score=... drops chunks
// whose cosine sits below 0.4, matching the per-project SemanticSearch
// default. A request that explicitly passes ?min_score=0 keeps them.
//
// Geometry: strong cosine 0.5 (lead), weak cosine 0.3 (in the gap
// (0,0.4) the change targets). Both projects must survive the
// relative project gate (0.4 × best = 0.2); the only thing
// differentiating them is the min_score filter.
func TestWorkspaceSearch_DefaultMinScoreIs04(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	vs := openTestVectorStore(t)
	query := l2([]float32{1, 0, 0, 0})
	router := newSearchRouter(t, d, vs, fixedEmbedder{q: query})
	wsID := createWS(t, router, "default-floor")

	// Strong project: cosine = 0.5 with query (l2-normalized vec
	// projects onto x by exactly 0.5).
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/strong@main",
		[]vectorstore.Chunk{
			{Content: "s", FilePath: "s.go", StartLine: 1, EndLine: 9,
				ChunkType: "function", SymbolName: "S", Language: "go"},
		},
		[][]float32{l2([]float32{0.5, 0.866, 0, 0})}, // |v|=1, cos(v,q)=0.5
	)
	// Weak project: cosine = 0.3 — survives min_score=0 (and would
	// survive old default 0) but is filtered by new default 0.4.
	// Relative gate: 0.4 × strong_candidacy. Candidacy is
	// 0.5*dense_norm; dense_norm(weak)=0.3/0.5=0.6 → candidacy(weak)=0.3.
	// Threshold = 0.4 × 0.5 = 0.2 → weak survives the gate at
	// min_score=0.
	seedRepoWithChunks(t, d, vs, wsID, "github.com/o/weak@main",
		[]vectorstore.Chunk{
			{Content: "w", FilePath: "w.go", StartLine: 1, EndLine: 9,
				ChunkType: "function", SymbolName: "W", Language: "go"},
		},
		[][]float32{l2([]float32{0.3, 0.9539, 0, 0})}, // cos = 0.3
	)

	// Default: no min_score param → new 0.4 floor → weak's only chunk
	// (cosine 0.3) is filtered before candidacy aggregation, weak's
	// dense_signal drops to 0, gate drops the project.
	rr := doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("default min_score: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var defaultResp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &defaultResp)
	if len(defaultResp.Projects) != 1 || defaultResp.Projects[0].ProjectPath != "github.com/o/strong@main" {
		t.Fatalf("default min_score=0.4: expected only strong project, got %+v",
			defaultResp.Projects)
	}

	// Explicit override: min_score=0 → weak project survives gate.
	rr = doJSON(t, router, http.MethodGet,
		"/api/v1/workspaces/"+wsID+"/search?q=x&min_score=0", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("min_score=0 override: expected 200, got %d", rr.Code)
	}
	var openResp searchResp
	_ = json.Unmarshal(rr.Body.Bytes(), &openResp)
	if len(openResp.Projects) != 2 {
		t.Fatalf("min_score=0: expected both projects to survive, got %d (%+v)",
			len(openResp.Projects), openResp.Projects)
	}
}
