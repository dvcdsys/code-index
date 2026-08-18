package vectorstore

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// refHit is one result of the brute-force reference implementation the store
// is checked against.
type refHit struct {
	key   string // filePath:startLine-endLine — unique per document here
	score float32
}

// referenceSearch is the obvious O(n) implementation: score every candidate
// with a plain (non-unrolled) float32 dot product, sort, take K. It shares no
// code with the store's scan path, which is the point.
func referenceSearch(docs []Chunk, embs [][]float32, q []float32, k int, lang string) []refHit {
	var hits []refHit
	for i, c := range docs {
		if lang != "" && c.Language != lang {
			continue
		}
		var sum float32
		for j := range q {
			sum += q[j] * embs[i][j]
		}
		hits = append(hits, refHit{key: chunkKey(c), score: sum})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].key < hits[b].key
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func chunkKey(c Chunk) string {
	return fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
}

func resultKey(r SearchResult) string {
	return fmt.Sprintf("%s:%d-%d", r.FilePath, r.StartLine, r.EndLine)
}

// TestSearchParityWithBruteForce is the correctness gate on the scan: the
// streamed SQLite scan plus top-K heap must agree with a naive in-memory
// search on the same data, at a small and a large K, filtered and unfiltered.
func TestSearchParityWithBruteForce(t *testing.T) {
	const (
		nDocs = 500
		dim   = 8
	)
	ctx := context.Background()
	s := openStore(t)
	const project = "/parity"

	r := rand.New(rand.NewSource(7))
	docs := make([]Chunk, nDocs)
	embs := make([][]float32, nDocs)
	for i := range docs {
		lang := "go"
		if i%2 == 1 {
			lang = "python"
		}
		docs[i] = Chunk{
			Content:    fmt.Sprintf("chunk %d", i),
			FilePath:   fmt.Sprintf("pkg/f%03d.go", i),
			StartLine:  i*10 + 1,
			EndLine:    i*10 + 9,
			ChunkType:  "function",
			SymbolName: fmt.Sprintf("Fn%03d", i),
			Language:   lang,
		}
		embs[i] = randNorm(r, dim)
	}
	if err := s.UpsertChunks(ctx, project, docs, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	queries := make([][]float32, 5)
	for i := range queries {
		queries[i] = randNorm(r, dim)
	}

	cases := []struct {
		name  string
		k     int
		where map[string]string
		lang  string
	}{
		{"k10", 10, nil, ""},
		{"k200", 200, nil, ""},
		{"k10_lang_go", 10, map[string]string{"language": "go"}, "go"},
		{"k200_lang_python", 200, map[string]string{"language": "python"}, "python"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for qi, q := range queries {
				got, err := s.Search(ctx, project, q, tc.k, tc.where)
				if err != nil {
					t.Fatalf("query %d: Search: %v", qi, err)
				}
				want := referenceSearch(docs, embs, q, tc.k, tc.lang)
				if len(got) != len(want) {
					t.Fatalf("query %d: got %d results, want %d", qi, len(got), len(want))
				}
				for i := range got {
					if resultKey(got[i]) != want[i].key {
						t.Fatalf("query %d result %d: got %q (score %.6f), want %q (score %.6f)",
							qi, i, resultKey(got[i]), got[i].Score, want[i].key, want[i].score)
					}
					if d := math.Abs(float64(got[i].Score - round4(want[i].score))); d > 1e-6 {
						t.Fatalf("query %d result %d: score %.8f vs reference %.8f (delta %g)",
							qi, i, got[i].Score, round4(want[i].score), d)
					}
				}
			}
		})
	}
}

// TestSearchContentAndMetadataRoundTrip proves the hydrate step returns the
// stored chunk text and every metadata column, not just the ranking.
func TestSearchContentAndMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/hydrate"

	chunks := []Chunk{{
		Content:    "func Answer() int { return 42 }",
		FilePath:   "pkg/answer.go",
		StartLine:  10,
		EndLine:    12,
		ChunkType:  "function",
		SymbolName: "Answer",
		Language:   "go",
	}}
	embs := [][]float32{{1, 0, 0, 0}}
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	got, err := s.Search(ctx, project, []float32{1, 0, 0, 0}, 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	want := SearchResult{
		FilePath: "pkg/answer.go", StartLine: 10, EndLine: 12,
		Content: chunks[0].Content, Score: 1, ChunkType: "function",
		SymbolName: "Answer", Language: "go",
	}
	if got[0] != want {
		t.Errorf("result = %+v, want %+v", got[0], want)
	}
}

// The where filter mirrors chromem-go's documentMatchesFilters, including its
// two surprising cases for keys the metadata does not have.
func TestSearchWhereFilterMirrorsChromemSemantics(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/where"

	chunks, embs := makeChunks(4, "a.go", "go")
	if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	// Unknown key with a non-empty value matched nothing under chromem
	// (metadata[k] was the zero string), and must return nothing here — not
	// an error.
	got, err := s.Search(ctx, project, embs[0], 5, map[string]string{"nonsense": "x"})
	if err != nil {
		t.Fatalf("unknown key: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unknown filter key returned %d results, want 0", len(got))
	}

	// Unknown key with an EMPTY value matched everything under chromem.
	got, err = s.Search(ctx, project, embs[0], 5, map[string]string{"nonsense": ""})
	if err != nil {
		t.Fatalf("unknown key, empty value: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("unknown filter key with empty value returned %d results, want 4", len(got))
	}

	// Numeric metadata was compared as text.
	got, err = s.Search(ctx, project, embs[0], 5, map[string]string{"start_line": "11"})
	if err != nil {
		t.Fatalf("start_line filter: %v", err)
	}
	if len(got) != 1 || got[0].StartLine != 11 {
		t.Errorf("start_line filter returned %+v, want the single chunk starting at 11", got)
	}

	// A KNOWN key with an empty value is a filter, not the absence of one:
	// chromem compared metadata["language"] to "", which matches only rows
	// whose language is empty. The compact scan supports exactly this one
	// column, so it is the one place the two scan paths could disagree about
	// what an empty value means.
	got, err = s.Search(ctx, project, embs[0], 5, map[string]string{"language": ""})
	if err != nil {
		t.Fatalf("empty language filter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("language=\"\" returned %d results, want 0 — every chunk here is Go", len(got))
	}

	// Several keys must all match.
	got, err = s.Search(ctx, project, embs[0], 5,
		map[string]string{"language": "go", "file_path": "b.go"})
	if err != nil {
		t.Fatalf("multi-key filter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("multi-key filter returned %d results, want 0", len(got))
	}
}

// TestChurnKeepsSearchCorrect is the file-watcher hot path: the same file is
// deleted and reindexed over and over. Results must stay correct and the row
// count must not drift, however fragmented the table becomes.
func TestChurnKeepsSearchCorrect(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const project = "/churn"

	other, otherEmbs := makeChunks(50, "stable.go", "go")
	if err := s.UpsertChunks(ctx, project, other, otherEmbs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := rand.New(rand.NewSource(3))
	var lastEmb []float32
	for round := 0; round < 20; round++ {
		if err := s.DeleteByFile(ctx, project, "churn.go"); err != nil {
			t.Fatalf("round %d: DeleteByFile: %v", round, err)
		}
		chunks := make([]Chunk, 30)
		embs := make([][]float32, 30)
		for i := range chunks {
			chunks[i] = Chunk{
				Content:   fmt.Sprintf("round %d chunk %d", round, i),
				FilePath:  "churn.go",
				StartLine: i*4 + 1,
				EndLine:   i*4 + 3,
				Language:  "go",
			}
			embs[i] = randNorm(r, testDim)
		}
		if err := s.UpsertChunks(ctx, project, chunks, embs); err != nil {
			t.Fatalf("round %d: UpsertChunks: %v", round, err)
		}
		lastEmb = embs[7]

		if got, want := s.Count(project), 80; got != want {
			t.Fatalf("round %d: Count = %d, want %d", round, got, want)
		}
		res, err := s.Search(ctx, project, lastEmb, 3, nil)
		if err != nil {
			t.Fatalf("round %d: Search: %v", round, err)
		}
		if len(res) == 0 {
			t.Fatalf("round %d: no results", round)
		}
		if res[0].FilePath != "churn.go" || res[0].StartLine != 29 {
			t.Fatalf("round %d: top hit = %+v, want churn.go:29", round, res[0])
		}
		if res[0].Content != fmt.Sprintf("round %d chunk 7", round) {
			t.Fatalf("round %d: top hit content = %q (stale content survived a delete)", round, res[0].Content)
		}
	}

	// The deleted rows must not have left content behind.
	var contents, vectors int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vector_contents`).Scan(&contents); err != nil {
		t.Fatalf("count contents: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vectors`).Scan(&vectors); err != nil {
		t.Fatalf("count vectors: %v", err)
	}
	if contents != vectors {
		t.Errorf("vector_contents has %d rows, vectors has %d — delete-by-file leaked", contents, vectors)
	}
}

// BenchmarkSearch measures the scan on a collection large enough to be
// representative (20k documents of 768 dimensions ≈ 60 MB of embeddings).
// Not run by `go test`; invoke it explicitly:
//
//	go test ./internal/vectorstore -run xxx -bench Search -benchtime 20x
func BenchmarkSearch(b *testing.B) {
	const (
		nDocs = 20000
		dim   = 768
	)
	dir := b.TempDir()
	s, err := Open(dir)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	r := rand.New(rand.NewSource(5))
	chunks := make([]Chunk, nDocs)
	embs := make([][]float32, nDocs)
	for i := range chunks {
		chunks[i] = Chunk{
			Content:   "package main\n\nfunc main() { println(\"hello\") }\n",
			FilePath:  fmt.Sprintf("pkg/f%04d.go", i%400),
			StartLine: i*6 + 1, EndLine: i*6 + 5,
			ChunkType: "function", Language: "go",
		}
		embs[i] = randNorm(r, dim)
	}
	if err := s.UpsertChunks(ctx, "/bench", chunks, embs); err != nil {
		b.Fatalf("upsert: %v", err)
	}
	q := randNorm(r, dim)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search(ctx, "/bench", q, 10, nil); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}
