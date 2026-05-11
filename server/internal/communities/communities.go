// Package communities turns a workspace's combined call-graph into a
// flat set of structural communities + their centroid embeddings — the
// pieces stage 1 of two-stage workspace search consumes.
//
// Pipeline (Build):
//
//  1. Load every workspace_repo's project_path → call_edges for the
//     workspace into one undirected weighted graph (gonum/graph/simple).
//  2. Run Louvain (gonum/graph/community) with deterministic seed.
//  3. Persist communities + community_members (DELETE then INSERT —
//     wholesale rebuild; sub-second SQL at typical workspace sizes).
//  4. Compute one centroid per community by mean-pooling member chunks'
//     embeddings (fetched via vectorstore.FetchProjectChunkEmbeddings),
//     L2-normalising the result.
//  5. ReplaceCentroids into the workspace's chromem collection.
//
// What's deliberately NOT here in v1:
//   - Recursive split for >50-chunk communities — accept Louvain output
//     as-is; revisit if eval shows large communities hurt recall.
//   - Small-community merging — singletons just contribute their lone
//     symbol's vector; the unified centroid space competes fairly.
//   - Overlapping communities (BigCLAM, link clustering) — single
//     assignment per symbol keeps the centroid table tight.
//
// Caller (workspacejobs.compute_workspace_communities) provides
// debouncing via dedupe_key + Delay so a burst of repo indexes coalesces
// into one rebuild. That keeps churn off Chroma during catch-up after a
// long downtime.
package communities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"

	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// Resolution is the gamma parameter handed to Louvain. >1 yields more,
// smaller communities; <1 fewer, larger. 1.0 is the natural / default
// behaviour and matches what the Plan agent recommended for code graphs.
const Resolution = 1.0

// LouvainSeed makes the modularisation deterministic — same edges in,
// same partitioning out. Useful for: stable centroid storage across
// reindexes, reproducible tests, "why did the centroid move" debugging.
const LouvainSeed int64 = 42

// MaxLabelSize bounds the auto-generated community label length.
const MaxLabelSize = 80

// Errors.
var (
	ErrNoWorkspace = errors.New("workspace not found")
)

// BuildResult is what Build reports back, surfaced in job logs + the
// dashboard's workspace detail card.
type BuildResult struct {
	WorkspaceID     string
	Nodes           int
	Edges           int
	CommunityCount  int
	CentroidsStored int
	Modularity      float64
}

// Build runs the full pipeline against one workspace. Idempotent —
// every output is rebuilt from scratch so partial failure on a previous
// run can't leave stale state.
//
// vs may be nil — in that case centroid computation is skipped and the
// communities tables alone are populated. Useful for unit tests of the
// SQL layer without a chromem store on disk.
func Build(ctx context.Context, db *sql.DB, vs *vectorstore.Store, workspaceID string, logger *slog.Logger) (BuildResult, error) {
	if logger == nil {
		logger = slog.Default()
	}
	res := BuildResult{WorkspaceID: workspaceID}

	// 1. Resolve project_paths in this workspace.
	projectPaths, err := listProjectPaths(ctx, db, workspaceID)
	if err != nil {
		return res, err
	}
	if len(projectPaths) == 0 {
		// Empty workspace — clear any prior state and exit clean.
		if err := clearCommunities(ctx, db, workspaceID); err != nil {
			return res, err
		}
		if vs != nil {
			_ = vs.DeleteCentroids(workspaceID)
		}
		return res, nil
	}

	// 2. Build the in-memory graph from call_edges.
	g := simple.NewWeightedUndirectedGraph(0, 0)
	idToNode, nodeToID, nodeProject, err := loadGraph(ctx, db, projectPaths, g)
	if err != nil {
		return res, err
	}
	res.Nodes = len(idToNode)
	res.Edges = g.Edges().Len()
	if res.Nodes == 0 {
		// No call_edges yet (perhaps PR4 hasn't run for any repo). Same
		// cleanup path as empty workspace.
		if err := clearCommunities(ctx, db, workspaceID); err != nil {
			return res, err
		}
		if vs != nil {
			_ = vs.DeleteCentroids(workspaceID)
		}
		return res, nil
	}

	// 3. Louvain.
	rng := rand.New(rand.NewSource(LouvainSeed))
	reducer := community.Modularize(g, Resolution, rng)
	communitiesOut := reducer.Communities()
	res.Modularity = community.Q(g, communitiesOut, Resolution)
	res.CommunityCount = len(communitiesOut)

	// 4. Persist communities + members in one transaction.
	persisted, err := persistCommunities(ctx, db, workspaceID, communitiesOut, nodeToID, nodeProject)
	if err != nil {
		return res, fmt.Errorf("persist communities: %w", err)
	}

	// 5. Centroids — only when a vectorstore is configured.
	if vs == nil {
		return res, nil
	}
	docs, err := buildCentroidDocs(ctx, db, vs, workspaceID, persisted, logger)
	if err != nil {
		return res, fmt.Errorf("build centroids: %w", err)
	}
	if err := vs.ReplaceCentroids(ctx, workspaceID, docs); err != nil {
		return res, fmt.Errorf("replace centroids: %w", err)
	}
	res.CentroidsStored = len(docs)
	return res, nil
}

// --- step helpers ---

func listProjectPaths(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT project_path FROM workspace_repos WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list project paths: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadGraph populates `g` with edges from every project_path's
// call_edges row. Returns the symbol_id ↔ graph-node-id mappings plus a
// node→project lookup so persisted community_members rows know their
// project_path.
func loadGraph(ctx context.Context, db *sql.DB, projectPaths []string, g *simple.WeightedUndirectedGraph) (idToNode map[string]int64, nodeToID map[int64]string, nodeProject map[int64]string, err error) {
	idToNode = map[string]int64{}
	nodeToID = map[int64]string{}
	nodeProject = map[int64]string{}
	nextNode := int64(0)
	getNode := func(symID, projectPath string) int64 {
		if n, ok := idToNode[symID]; ok {
			return n
		}
		n := nextNode
		nextNode++
		idToNode[symID] = n
		nodeToID[n] = symID
		nodeProject[n] = projectPath
		g.AddNode(simple.Node(n))
		return n
	}

	for _, pp := range projectPaths {
		rows, qerr := db.QueryContext(ctx,
			`SELECT caller_symbol, callee_symbol, weight FROM call_edges WHERE project_path = ?`, pp)
		if qerr != nil {
			return nil, nil, nil, fmt.Errorf("load call_edges: %w", qerr)
		}
		for rows.Next() {
			var caller, callee string
			var w float64
			if serr := rows.Scan(&caller, &callee, &w); serr != nil {
				rows.Close()
				return nil, nil, nil, serr
			}
			a := getNode(caller, pp)
			b := getNode(callee, pp)
			if a == b {
				continue
			}
			existing := g.WeightedEdge(a, b)
			if existing != nil {
				w += existing.Weight()
			}
			g.SetWeightedEdge(simple.WeightedEdge{F: simple.Node(a), T: simple.Node(b), W: w})
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return nil, nil, nil, rerr
		}
	}
	return idToNode, nodeToID, nodeProject, nil
}

// persistedCommunity carries enough state for the centroid pass to look
// up member symbol names without re-querying the DB.
type persistedCommunity struct {
	ID           string
	Members      []memberRow
	ProjectPaths []string
	Label        string
	Size         int
}

type memberRow struct {
	ProjectPath string
	SymbolID    string
	Name        string
}

// persistCommunities deletes all prior rows for the workspace and inserts
// the Louvain output. Wrapped in a single transaction so a mid-run abort
// leaves the workspace's community state empty rather than partial.
func persistCommunities(
	ctx context.Context,
	db *sql.DB,
	workspaceID string,
	louvainOut [][]graph.Node,
	nodeToID map[int64]string,
	nodeProject map[int64]string,
) ([]persistedCommunity, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM community_members WHERE community_id IN
		   (SELECT id FROM communities WHERE workspace_id = ?)`, workspaceID); err != nil {
		return nil, fmt.Errorf("delete prior members: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM communities WHERE workspace_id = ?`, workspaceID); err != nil {
		return nil, fmt.Errorf("delete prior communities: %w", err)
	}

	// Bulk-load symbol names so we can label communities + drive the
	// centroid lookup later. One query gathers them all (single
	// project-scoped IN; we union per project).
	symbolIDs := map[string]struct{}{}
	for n := range nodeToID {
		symbolIDs[nodeToID[n]] = struct{}{}
	}
	names, err := loadSymbolNames(ctx, tx, symbolIDs)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	out := make([]persistedCommunity, 0, len(louvainOut))

	for _, group := range louvainOut {
		if len(group) == 0 {
			continue
		}
		commID := uuid.NewString()
		pc := persistedCommunity{ID: commID, Size: len(group)}

		projectSet := map[string]struct{}{}
		topNames := []string{}
		for _, n := range group {
			gid := n.ID()
			symID := nodeToID[gid]
			pp := nodeProject[gid]
			projectSet[pp] = struct{}{}
			pc.Members = append(pc.Members, memberRow{
				ProjectPath: pp,
				SymbolID:    symID,
				Name:        names[symID],
			})
			if len(topNames) < 5 && names[symID] != "" {
				topNames = append(topNames, names[symID])
			}
		}
		for pp := range projectSet {
			pc.ProjectPaths = append(pc.ProjectPaths, pp)
		}
		sort.Strings(pc.ProjectPaths)
		pc.Label = buildLabel(topNames)

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO communities (id, workspace_id, label, size, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			commID, workspaceID, pc.Label, pc.Size, now,
		); err != nil {
			return nil, fmt.Errorf("insert community: %w", err)
		}
		for _, m := range pc.Members {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO community_members (community_id, project_path, symbol_id)
				 VALUES (?, ?, ?)`,
				commID, m.ProjectPath, m.SymbolID,
			); err != nil {
				return nil, fmt.Errorf("insert member: %w", err)
			}
		}
		out = append(out, pc)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return out, nil
}

// loadSymbolNames returns a map symbol_id → name for every id in the
// set. Uses a single SQL query with chunked IN clauses (modernc.org/sqlite
// caps variable count around 999).
func loadSymbolNames(ctx context.Context, tx *sql.Tx, ids map[string]struct{}) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	const chunkSize = 500
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	for i := 0; i < len(idList); i += chunkSize {
		end := i + chunkSize
		if end > len(idList) {
			end = len(idList)
		}
		chunk := idList[i:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for j, id := range chunk {
			args[j] = id
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id, name FROM symbols WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("load names: %w", err)
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = name
		}
		rows.Close()
	}
	return out, nil
}

// buildLabel derives a human-readable label from the community's top
// symbol names. Picked for the dashboard sidebar — operators glance and
// recognise "auth, login, ..." without clicking through.
func buildLabel(names []string) string {
	if len(names) == 0 {
		return ""
	}
	label := strings.Join(names, ", ")
	if len(label) > MaxLabelSize {
		label = label[:MaxLabelSize-3] + "..."
	}
	return label
}

// buildCentroidDocs computes one CentroidDoc per persisted community by
// fetching member chunks' embeddings from the per-project chromem
// collections, mean-pooling, and L2-normalising.
//
// Communities whose members have no chunks in the vectorstore (e.g.
// recently-indexed repo whose embeddings haven't been written yet) are
// silently skipped — they reappear on the next compute cycle. This is
// preferable to writing zero-vector centroids which would shadow
// legitimate ones in search.
func buildCentroidDocs(
	ctx context.Context,
	db *sql.DB,
	vs *vectorstore.Store,
	workspaceID string,
	persisted []persistedCommunity,
	logger *slog.Logger,
) ([]vectorstore.CentroidDoc, error) {
	_ = db // unused — kept in signature for symmetry / future extension
	out := make([]vectorstore.CentroidDoc, 0, len(persisted))

	for _, pc := range persisted {
		// Group member names by project_path so we make one
		// FetchProjectChunkEmbeddings call per project.
		byProject := map[string][]string{}
		for _, m := range pc.Members {
			if m.Name == "" {
				continue
			}
			byProject[m.ProjectPath] = append(byProject[m.ProjectPath], m.Name)
		}
		all := [][]float32{}
		for pp, names := range byProject {
			vecs, err := vs.FetchProjectChunkEmbeddings(ctx, pp, names)
			if err != nil {
				logger.Warn("communities: fetch embeddings failed",
					"workspace_id", workspaceID,
					"project_path", pp,
					"err", err)
				continue
			}
			all = append(all, vecs...)
		}
		if len(all) == 0 {
			continue
		}
		centroid := meanPool(all)
		l2Normalise(centroid)
		out = append(out, vectorstore.CentroidDoc{
			CommunityID:  pc.ID,
			Label:        pc.Label,
			ProjectPaths: pc.ProjectPaths,
			MemberCount:  pc.Size,
			Embedding:    centroid,
		})
	}
	return out, nil
}

// meanPool averages vectors element-wise. All inputs must share length;
// the function panics otherwise (caller guarantees consistent
// dimensionality from the embeddings model).
func meanPool(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	dim := len(vs[0])
	out := make([]float32, dim)
	for _, v := range vs {
		for i := range dim {
			out[i] += v[i]
		}
	}
	inv := float32(1) / float32(len(vs))
	for i := range dim {
		out[i] *= inv
	}
	return out
}

// l2Normalise unit-scales in place. Cosine similarity in chromem is
// independent of vector magnitude already, but normalising keeps the
// centroid bounded so accumulated FP error doesn't drift across
// rebuilds.
func l2Normalise(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	inv := float32(1) / float32(math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
}

// clearCommunities wipes both communities and community_members for a
// workspace. CASCADE on community_members would do it automatically,
// but explicit deletes keep the operation transparent in the SQL log.
func clearCommunities(ctx context.Context, db *sql.DB, workspaceID string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM community_members WHERE community_id IN
		   (SELECT id FROM communities WHERE workspace_id = ?)`, workspaceID); err != nil {
		return fmt.Errorf("clear members: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM communities WHERE workspace_id = ?`, workspaceID); err != nil {
		return fmt.Errorf("clear communities: %w", err)
	}
	return nil
}
