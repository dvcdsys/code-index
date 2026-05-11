package communities

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	wsID := uuid.NewString()
	if _, err := d.Exec(`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		wsID, "test", now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return d, wsID
}

// seedRepo inserts a workspace_repo + the corresponding project +
// symbols + call_edges so Louvain has something to chew on.
func seedRepo(t *testing.T, d *sql.DB, wsID, projectPath string, symbols []struct{ Name string }, edges [][2]int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if _, err := d.Exec(`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'created', ?, ?, 'abc')`,
		projectPath, projectPath, now, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	// Synthesise a unique github_url + branch per projectPath so multiple
	// repos in one workspace don't collide on the UNIQUE constraint.
	if _, err := d.Exec(`INSERT INTO workspace_repos
		(id, workspace_id, github_url, branch, project_path, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, 'main', ?, 'sec', ?, ?)`,
		uuid.NewString(), wsID, "https://"+projectPath, projectPath, now, now); err != nil {
		t.Fatalf("insert workspace_repo: %v", err)
	}

	symIDs := make([]string, len(symbols))
	for i, sym := range symbols {
		id := uuid.NewString()
		symIDs[i] = id
		if _, err := d.Exec(`INSERT INTO symbols (id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name)
			VALUES (?, ?, ?, 'function', 'main.go', ?, ?, 'go', '', NULL)`,
			id, projectPath, sym.Name, 10+i*10, 15+i*10); err != nil {
			t.Fatalf("insert symbol: %v", err)
		}
	}
	for _, e := range edges {
		if _, err := d.Exec(`INSERT INTO call_edges (project_path, caller_symbol, callee_symbol, weight, source)
			VALUES (?, ?, ?, 1.0, 'test')`,
			projectPath, symIDs[e[0]], symIDs[e[1]]); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
	}
}

// Verify Louvain produces multiple communities on a graph with two
// disconnected clusters, persists them, and emits a non-zero modularity.
func TestBuild_TwoClusters(t *testing.T) {
	d, wsID := openTestDB(t)
	const pp = "github.com/test/repo@main"
	// Cluster A: 0↔1↔2, cluster B: 3↔4↔5, no edges between.
	seedRepo(t, d, wsID, pp,
		[]struct{ Name string }{
			{"a1"}, {"a2"}, {"a3"},
			{"b1"}, {"b2"}, {"b3"},
		},
		[][2]int{
			{0, 1}, {1, 2}, {2, 0}, // triangle A
			{3, 4}, {4, 5}, {5, 3}, // triangle B
		},
	)
	res, err := Build(context.Background(), d, nil, wsID, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Nodes != 6 {
		t.Fatalf("expected 6 nodes, got %d", res.Nodes)
	}
	if res.CommunityCount < 2 {
		t.Fatalf("expected ≥2 communities, got %d (modularity %.3f)", res.CommunityCount, res.Modularity)
	}
	if res.Modularity <= 0 {
		t.Fatalf("expected positive modularity, got %v", res.Modularity)
	}

	// Verify SQL rows.
	var commCount, memberCount int
	_ = d.QueryRow(`SELECT COUNT(*) FROM communities WHERE workspace_id = ?`, wsID).Scan(&commCount)
	_ = d.QueryRow(`SELECT COUNT(*) FROM community_members`).Scan(&memberCount)
	if commCount != res.CommunityCount {
		t.Fatalf("SQL community count %d != Build result %d", commCount, res.CommunityCount)
	}
	if memberCount != 6 {
		t.Fatalf("expected 6 members total, got %d", memberCount)
	}
}

// Empty workspace → no communities and clean exit.
func TestBuild_EmptyWorkspace(t *testing.T) {
	d, wsID := openTestDB(t)
	res, err := Build(context.Background(), d, nil, wsID, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Nodes != 0 || res.CommunityCount != 0 {
		t.Fatalf("expected zero output for empty workspace, got %+v", res)
	}
}

// Idempotency — running Build twice yields the same community sizes
// (membership may shuffle ids on rebuild; only structural shape is
// stable).
func TestBuild_Idempotent(t *testing.T) {
	d, wsID := openTestDB(t)
	const pp = "github.com/test/repo@main"
	seedRepo(t, d, wsID, pp,
		[]struct{ Name string }{{"a"}, {"b"}, {"c"}, {"d"}},
		[][2]int{{0, 1}, {2, 3}},
	)
	r1, err := Build(context.Background(), d, nil, wsID, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := Build(context.Background(), d, nil, wsID, nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if r1.CommunityCount != r2.CommunityCount {
		t.Fatalf("non-idempotent community count: %d → %d", r1.CommunityCount, r2.CommunityCount)
	}
}

// Cross-project edges aren't drawn (PR4 is intra-project only), but a
// workspace with two REPOS yields two clusters of communities — verify
// the project_path lookup populates correctly so centroids know which
// project_paths to advertise.
func TestBuild_TwoRepoWorkspaceTracksProjectPaths(t *testing.T) {
	d, wsID := openTestDB(t)
	seedRepo(t, d, wsID, "github.com/o/a@main",
		[]struct{ Name string }{{"a1"}, {"a2"}},
		[][2]int{{0, 1}},
	)
	seedRepo(t, d, wsID, "github.com/o/b@main",
		[]struct{ Name string }{{"b1"}, {"b2"}},
		[][2]int{{0, 1}},
	)
	res, err := Build(context.Background(), d, nil, wsID, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.CommunityCount < 2 {
		t.Fatalf("expected at least 2 communities across 2 repos, got %d", res.CommunityCount)
	}
	// Each community should map to exactly one project_path since edges
	// don't cross repos in PR4.
	rows, _ := d.Query(`SELECT cm.community_id, cm.project_path FROM community_members cm
		JOIN communities c ON c.id = cm.community_id WHERE c.workspace_id = ?`, wsID)
	seen := map[string]map[string]struct{}{}
	for rows.Next() {
		var cid, pp string
		_ = rows.Scan(&cid, &pp)
		if seen[cid] == nil {
			seen[cid] = map[string]struct{}{}
		}
		seen[cid][pp] = struct{}{}
	}
	rows.Close()
	for cid, paths := range seen {
		if len(paths) != 1 {
			t.Fatalf("community %s spans %d project_paths (cross-project edges shouldn't exist in PR4)", cid, len(paths))
		}
	}
}

func TestMeanPool(t *testing.T) {
	got := meanPool([][]float32{
		{1, 2, 3},
		{3, 4, 5},
	})
	want := []float32{2, 3, 4}
	for i, v := range got {
		if v != want[i] {
			t.Fatalf("meanPool[%d] = %v, want %v", i, v, want[i])
		}
	}
}

func TestL2Normalise(t *testing.T) {
	v := []float32{3, 4}
	l2Normalise(v)
	// 3-4-5 triangle → normalised to (0.6, 0.8).
	if v[0] < 0.59 || v[0] > 0.61 || v[1] < 0.79 || v[1] > 0.81 {
		t.Fatalf("normalised vector wrong: %v", v)
	}
}
