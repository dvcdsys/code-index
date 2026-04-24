package vectorstore

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

const testDim = 768

// randNorm returns a random L2-normalised float32 vector.
func randNorm(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var sum float64
	for i := range v {
		x := float32(r.NormFloat64())
		v[i] = x
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		scale := float32(1.0 / math.Sqrt(sum))
		for i := range v {
			v[i] *= scale
		}
	}
	return v
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func makeChunks(n int, filePath, lang string) ([]Chunk, [][]float32) {
	r := rand.New(rand.NewSource(42))
	chunks := make([]Chunk, n)
	embeddings := make([][]float32, n)
	for i := 0; i < n; i++ {
		chunks[i] = Chunk{
			Content:    "some code content",
			FilePath:   filePath,
			StartLine:  i*10 + 1,
			EndLine:    i*10 + 9,
			ChunkType:  "function",
			SymbolName: "fn" + string(rune('A'+i%26)),
			Language:   lang,
		}
		embeddings[i] = randNorm(r, testDim)
	}
	return chunks, embeddings
}

// --------------------------------------------------------------------------
// tests
// --------------------------------------------------------------------------

func TestUpsertAndSearch(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/home/user/myproject"

	chunks, embs := makeChunks(10, "main.go", "go")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	if got := s.Count(project); got != 10 {
		t.Errorf("Count after upsert = %d, want 10", got)
	}

	// Query with the first embedding — should be the top result.
	results, err := s.Search(ctx, project, embs[0], 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}
	if results[0].FilePath != "main.go" {
		t.Errorf("top result FilePath = %q, want %q", results[0].FilePath, "main.go")
	}
	if results[0].Score < 0.99 {
		t.Errorf("exact-match score = %.4f, want ≥ 0.99", results[0].Score)
	}
}

func TestScoreRounding(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	chunks, embs := makeChunks(5, "a.py", "python")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	results, err := s.Search(ctx, project, embs[0], 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		rounded := round4(r.Score)
		if r.Score != rounded {
			t.Errorf("score %v not rounded to 4 dp (got %v)", r.Score, rounded)
		}
	}
}

func TestUpsertOverwrites(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	chunks, embs := makeChunks(3, "f.go", "go")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("second upsert (overwrite): %v", err)
	}
	// Count must stay 3 (not 6) since IDs are deterministic.
	if got := s.Count(project); got != 3 {
		t.Errorf("Count after double upsert = %d, want 3", got)
	}
}

func TestDeleteByFile(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	chunksA, embsA := makeChunks(4, "a.go", "go")
	chunksB, embsB := makeChunks(3, "b.go", "go")

	if err := s.UpsertChunks(ctx, project, chunksA, embsA); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChunks(ctx, project, chunksB, embsB); err != nil {
		t.Fatal(err)
	}
	if got := s.Count(project); got != 7 {
		t.Fatalf("pre-delete count = %d, want 7", got)
	}

	if err := s.DeleteByFile(ctx, project, "a.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	if got := s.Count(project); got != 3 {
		t.Errorf("post-delete count = %d, want 3", got)
	}

	// Search must not return a.go chunks.
	results, err := s.Search(ctx, project, embsA[0], 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.FilePath == "a.go" {
			t.Errorf("deleted file %q still appears in search results", r.FilePath)
		}
	}
}

func TestDeleteCollection(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	chunks, embs := makeChunks(5, "x.py", "python")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteCollection(project); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	if got := s.Count(project); got != 0 {
		t.Errorf("Count after DeleteCollection = %d, want 0", got)
	}
}

func TestSearchWithWhereFilter(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	chunksGo, embsGo := makeChunks(5, "main.go", "go")
	chunksPy, embsPy := makeChunks(5, "main.py", "python")

	if err := s.UpsertChunks(ctx, project, chunksGo, embsGo); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChunks(ctx, project, chunksPy, embsPy); err != nil {
		t.Fatal(err)
	}

	// Query with a Go embedding and restrict to language=python — should not
	// surface any go results.
	results, err := s.Search(ctx, project, embsGo[0], 10, map[string]string{"language": "python"})
	if err != nil {
		t.Fatalf("Search with where: %v", err)
	}
	for _, r := range results {
		if r.Language != "python" {
			t.Errorf("where filter failed: got language=%q, want python", r.Language)
		}
	}
}

func TestBatchUpsert(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/p"

	// 1200 chunks — forces 3 batches of 500/500/200.
	chunks, embs := makeChunks(1200, "big.go", "go")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("batch upsert: %v", err)
	}
	if got := s.Count(project); got != 1200 {
		t.Errorf("Count = %d, want 1200", got)
	}
}

// TestSearchLatencyGate is the Phase 4 exit criterion.
// 1000 pre-embedded chunks, 50 queries — P95 must be < 200ms.
// This mirrors the Phase 0 gate but runs via normal `go test` (no build tag)
// because the data is synthetic and needs no llama-server.
func TestSearchLatencyGate(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/latency-gate"

	r := rand.New(rand.NewSource(99))

	const (
		nDocs    = 1000
		nQueries = 50
		topK     = 10
	)

	chunks := make([]Chunk, nDocs)
	embs := make([][]float32, nDocs)
	for i := 0; i < nDocs; i++ {
		chunks[i] = Chunk{
			Content:   "synthetic chunk content",
			FilePath:  "file.go",
			StartLine: i*5 + 1,
			EndLine:   i*5 + 4,
			ChunkType: "function",
			Language:  "go",
		}
		embs[i] = randNorm(r, testDim)
	}
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	queries := make([][]float32, nQueries)
	for i := range queries {
		queries[i] = randNorm(r, testDim)
	}

	latencies := make([]float64, nQueries)
	for i, q := range queries {
		t0 := time.Now()
		if _, err := s.Search(ctx, project, q, topK, nil); err != nil {
			t.Fatalf("Search[%d]: %v", i, err)
		}
		latencies[i] = float64(time.Since(t0).Microseconds()) / 1000.0
	}

	sort.Float64s(latencies)
	p95idx := int(float64(len(latencies)) * 0.95)
	p95 := latencies[p95idx]
	t.Logf("P95=%.1fms (gate <200ms) over %d queries on %d docs", p95, nQueries, nDocs)

	if p95 >= 200 {
		t.Errorf("P95 latency %.1fms ≥ 200ms gate", p95)
	}
}
