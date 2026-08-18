package vectorstore

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The compact scan copy. What has to be true of it:
//
//   - the scores it reports are the exact ones, because the shortlist is
//     rescored on the float32 originals;
//   - it returns the same documents an exact scan would, which is an empirical
//     property of the shortlist width and is therefore measured, not assumed;
//   - a collection it has not covered yet still answers correctly;
//   - deleting data deletes it here too, in both directions.
// ---------------------------------------------------------------------------

// q8Corpus builds a collection that is hostile to quantisation: half the
// vectors are random, and the other half are near-duplicates of a few cluster
// centres, differing by less than the quantisation step. Random unit vectors
// in 2048 dimensions are almost orthogonal to each other and to any query, so
// a corpus of only those has no near-ties to misorder and would let any
// approximation look perfect. Real code corpora are the opposite: boilerplate,
// generated files and copied blocks produce exactly these clusters.
func q8Corpus(t *testing.T, s *Store, project string, n, dim int) ([]Chunk, [][]float32) {
	t.Helper()
	r := rand.New(rand.NewSource(11))
	centres := make([][]float32, 8)
	for i := range centres {
		centres[i] = randNorm(r, dim)
	}

	chunks := make([]Chunk, n)
	embs := make([][]float32, n)
	langs := []string{"go", "python", "rust"}
	for i := range chunks {
		var v []float32
		if i%2 == 0 {
			v = randNorm(r, dim)
		} else {
			base := centres[i%len(centres)]
			v = make([]float32, dim)
			for j := range v {
				v[j] = base[j] + float32(r.NormFloat64())*1e-4
			}
			v = normalizeVector(v)
		}
		chunks[i] = Chunk{
			Content:    fmt.Sprintf("chunk %d", i),
			FilePath:   fmt.Sprintf("src/pkg%02d/f%04d.go", i%20, i),
			StartLine:  i*10 + 1,
			EndLine:    i*10 + 9,
			ChunkType:  "function",
			SymbolName: fmt.Sprintf("Fn%04d", i),
			Language:   langs[i%len(langs)],
		}
		embs[i] = v
	}
	if err := s.UpsertChunks(context.Background(), project, chunks, embs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return chunks, embs
}

// exactTopK is the oracle: the ranking a full float32 scan produces, computed
// in Go from the embeddings the test itself wrote. Deliberately not computed
// by asking the store to scan the other way — an oracle that shares code with
// the thing under test can agree with it about a shared mistake.
func exactTopK(chunks []Chunk, embs [][]float32, q []float32, k int, language string) []string {
	type sc struct {
		key   string
		score float32
	}
	var all []sc
	for i, e := range embs {
		if language != "" && chunks[i].Language != language {
			continue
		}
		all = append(all, sc{locKey(chunks[i]), dot(q, e)})
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].score > all[j-1].score; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	out := make([]string, 0, k)
	for i := 0; i < k && i < len(all); i++ {
		out = append(out, all[i].key)
	}
	return out
}

// locKey identifies a chunk the way a caller sees it — the doc_id is internal.
func locKey(c Chunk) string { return fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine) }

func resultKeys(rs []SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = fmt.Sprintf("%s:%d-%d", r.FilePath, r.StartLine, r.EndLine)
	}
	return out
}

// TestSearchScoresAreExact is the property that does not depend on the corpus,
// the query or the shortlist width: whatever documents come back, the number
// attached to each is the true cosine against the stored float32 vector, not
// the int8 approximation that selected it.
//
// It matters because the score is not decoration. Callers threshold on it
// (min_score), the workspace fan-out normalises across projects with it, and
// hybrid search blends it with BM25. An approximate score would move results
// between projects in ways no per-project test would catch.
func TestSearchScoresAreExact(t *testing.T) {
	const dim = 512
	s := openStore(t)
	ctx := context.Background()
	chunks, embs := q8Corpus(t, s, "/exact", 1500, dim)

	r := rand.New(rand.NewSource(99))
	for qi := 0; qi < 10; qi++ {
		q := randNorm(r, dim)
		got, err := s.Search(ctx, "/exact", q, 10, nil)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("no results")
		}
		byKey := map[string]float32{}
		for i := range chunks {
			byKey[locKey(chunks[i])] = dot(q, embs[i])
		}
		for _, res := range got {
			key := fmt.Sprintf("%s:%d-%d", res.FilePath, res.StartLine, res.EndLine)
			want := round4(byKey[key])
			if math.Abs(float64(res.Score-want)) > 1e-4 {
				t.Errorf("query %d: %s scored %v, exact cosine is %v — "+
					"the reported score came from the int8 approximation, not the rescore",
					qi, key, res.Score, want)
			}
		}
	}
}

// TestSearchMatchesExactRanking measures what the shortlist width buys.
//
// The compact scan is an approximation and could in principle drop a document
// that belongs in the top K. q8Shortlist picks its width from a measurement on
// real data (see its comment); this is the same measurement in miniature, on a
// corpus built to contain the near-ties that quantisation actually confuses,
// and it fails loudly if a change to the width, the quantisation or the
// rescore starts losing documents.
func TestSearchMatchesExactRanking(t *testing.T) {
	const (
		dim = 512
		k   = 10
	)
	s := openStore(t)
	ctx := context.Background()
	chunks, embs := q8Corpus(t, s, "/rank", 2000, dim)

	collID, ok, err := s.collectionID(ctx, collectionName("/rank"))
	if err != nil || !ok {
		t.Fatalf("collection id: %v ok=%v", err, ok)
	}
	if !s.q8Ready(ctx, collID) {
		t.Fatal("collection is not on the compact scan — this test would be measuring the float32 path")
	}

	r := rand.New(rand.NewSource(7))
	for qi := 0; qi < 25; qi++ {
		q := randNorm(r, dim)
		got, err := s.Search(ctx, "/rank", q, k, nil)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		want := exactTopK(chunks, embs, q, k, "")
		if strings.Join(resultKeys(got), ",") != strings.Join(want, ",") {
			t.Errorf("query %d: compact scan returned a different top-%d than an exact scan\n got: %v\nwant: %v",
				qi, k, resultKeys(got), want)
		}
	}
}

// TestSearchLanguageFilterOnCompactScan pins the one metadata column the
// compact copy carries. It is duplicated from `vectors`, so it can drift; a
// filter that silently matched nothing would look like "no results for that
// language", which is a plausible answer and therefore an invisible bug.
func TestSearchLanguageFilterOnCompactScan(t *testing.T) {
	const dim = 512
	s := openStore(t)
	ctx := context.Background()
	chunks, embs := q8Corpus(t, s, "/lang", 1200, dim)

	r := rand.New(rand.NewSource(3))
	q := randNorm(r, dim)
	got, err := s.Search(ctx, "/lang", q, 10, map[string]string{"language": "rust"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("language filter returned nothing")
	}
	for _, res := range got {
		if res.Language != "rust" {
			t.Fatalf("language filter leaked a %q result", res.Language)
		}
	}
	if want := exactTopK(chunks, embs, q, 10, "rust"); strings.Join(resultKeys(got), ",") != strings.Join(want, ",") {
		t.Errorf("filtered compact scan disagrees with an exact filtered scan\n got: %v\nwant: %v",
			resultKeys(got), want)
	}
}

// TestSearchUnsupportedFilterFallsBack covers the other half of q8Filterable:
// a filter the compact copy cannot express must take the float32 scan, which
// has every column, rather than be ignored.
func TestSearchUnsupportedFilterFallsBack(t *testing.T) {
	const dim = 256
	s := openStore(t)
	ctx := context.Background()
	chunks, _ := q8Corpus(t, s, "/filter", 600, dim)

	r := rand.New(rand.NewSource(5))
	q := randNorm(r, dim)
	target := chunks[123].FilePath
	got, err := s.Search(ctx, "/filter", q, 10, map[string]string{"file_path": target})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("file_path filter returned nothing — the fallback did not run")
	}
	for _, res := range got {
		if res.FilePath != target {
			t.Fatalf("file_path filter leaked %q", res.FilePath)
		}
	}
}

// stripQ8 turns a store back into what an older binary would have left behind:
// float32 vectors, no compact copy, no completion flag.
func stripQ8(t *testing.T, s *Store) {
	t.Helper()
	for _, stmt := range []string{`DELETE FROM vectors_q8`, `DELETE FROM q8_state`} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	s.q8Mu.Lock()
	s.q8State = map[int64]bool{}
	s.q8Mu.Unlock()
}

// TestSearchWithoutCompactCopy is the guarantee that makes the backfill safe
// to run in the background: a collection with no compact copy answers the same
// queries, correctly, the slow way.
func TestSearchWithoutCompactCopy(t *testing.T) {
	const dim = 512
	s := openStore(t)
	ctx := context.Background()
	chunks, embs := q8Corpus(t, s, "/nocopy", 800, dim)

	r := rand.New(rand.NewSource(21))
	q := randNorm(r, dim)
	before, err := s.Search(ctx, "/nocopy", q, 10, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	stripQ8(t, s)
	after, err := s.Search(ctx, "/nocopy", q, 10, nil)
	if err != nil {
		t.Fatalf("search after strip: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("no results without the compact copy")
	}
	if strings.Join(resultKeys(after), ",") != strings.Join(exactTopK(chunks, embs, q, 10, ""), ",") {
		t.Errorf("fallback scan disagrees with an exact scan: %v", resultKeys(after))
	}
	if strings.Join(resultKeys(before), ",") != strings.Join(resultKeys(after), ",") {
		t.Errorf("compact and fallback scans disagree\ncompact:  %v\nfallback: %v",
			resultKeys(before), resultKeys(after))
	}
}

// TestBackfillConvertsAnOldStore walks the upgrade path end to end: a store
// with no compact copy is opened, the background pass converts it, and
// searches then take the fast path and still agree with an exact scan.
func TestBackfillConvertsAnOldStore(t *testing.T) {
	const dim = 512
	dir := t.TempDir()
	ctx := context.Background()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chunks, embs := q8Corpus(t, s, "/old", 900, dim)
	stripQ8(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	collID, ok, err := s2.collectionID(ctx, collectionName("/old"))
	if err != nil || !ok {
		t.Fatalf("collection id: %v ok=%v", err, ok)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !s2.q8Ready(ctx, collID) {
		if time.Now().After(deadline) {
			t.Fatal("backfill did not finish within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var nVec, nQ8 int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM vectors`).Scan(&nVec); err != nil {
		t.Fatal(err)
	}
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM vectors_q8`).Scan(&nQ8); err != nil {
		t.Fatal(err)
	}
	if nVec != nQ8 {
		t.Fatalf("backfill left %d of %d vectors unconverted", nVec-nQ8, nVec)
	}

	r := rand.New(rand.NewSource(31))
	q := randNorm(r, dim)
	got, err := s2.Search(ctx, "/old", q, 10, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := exactTopK(chunks, embs, q, 10, ""); strings.Join(resultKeys(got), ",") != strings.Join(want, ",") {
		t.Errorf("backfilled scan disagrees with an exact scan\n got: %v\nwant: %v", resultKeys(got), want)
	}
}

// TestDeletesReachTheCompactCopy checks the direction that fails silently.
//
// A leftover q8 row is a document the scan keeps shortlisting and the rescore
// can no longer score, so it vanishes from results without anything logging a
// word — and it also keeps occupying disk that the "reclaimed" number says was
// freed.
func TestDeletesReachTheCompactCopy(t *testing.T) {
	const dim = 256
	s := openStore(t)
	ctx := context.Background()
	chunks, _ := q8Corpus(t, s, "/del", 400, dim)

	victim := chunks[7].FilePath
	if err := s.DeleteByFile(ctx, "/del", victim); err != nil {
		t.Fatalf("delete by file: %v", err)
	}
	var orphans int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM vectors_q8 q
		 WHERE NOT EXISTS (SELECT 1 FROM vectors v
		                    WHERE v.collection_id = q.collection_id AND v.doc_id = q.doc_id)`).
		Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("delete-by-file left %d orphaned rows in the compact copy", orphans)
	}

	if err := s.DeleteCollection("/del"); err != nil {
		t.Fatalf("delete collection: %v", err)
	}
	var left, states int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vectors_q8`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM q8_state`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if left != 0 || states != 0 {
		t.Errorf("delete-collection left %d compact rows and %d state rows", left, states)
	}
}

// TestQuantizeRoundTrip pins the encoding itself, away from any database.
func TestQuantizeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for _, dim := range []int{1, 7, 256, 2048} {
		v := randNorm(r, dim)
		blob, scale := quantizeInt8(v)
		if len(blob) != dim {
			t.Fatalf("dim %d: blob is %d bytes, want one per component", dim, len(blob))
		}
		var maxErr float64
		for i, b := range blob {
			got := float64(int8(b)) * float64(scale)
			if e := math.Abs(got - float64(v[i])); e > maxErr {
				maxErr = e
			}
		}
		// Half a quantisation step is the theoretical bound for round-to-
		// nearest; anything above it means the scale or the rounding is wrong.
		if bound := float64(scale)/2 + 1e-9; maxErr > bound {
			t.Errorf("dim %d: worst component error %g exceeds half a step (%g)", dim, maxErr, bound)
		}
	}

	// The zero vector must not produce a NaN scale or a panic: it scores 0
	// against everything, which is what the float32 dot product also gives it.
	blob, scale := quantizeInt8(make([]float32, 16))
	if scale != 0 || len(blob) != 16 {
		t.Errorf("zero vector: scale=%v len=%d, want 0 and 16", scale, len(blob))
	}
}

// TestDotInt8MatchesFloat checks the integer dot product against the float one
// on the same quantised values, so a mistake in the unrolled loop cannot hide
// behind quantisation error.
func TestDotInt8MatchesFloat(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	for _, dim := range []int{3, 4, 5, 64, 2048} {
		a, _ := quantizeInt8(randNorm(r, dim))
		b, _ := quantizeInt8(randNorm(r, dim))
		var want int32
		for i := range a {
			want += int32(int8(a[i])) * int32(int8(b[i]))
		}
		if got := dotInt8(a, b); got != want {
			t.Errorf("dim %d: dotInt8 = %d, want %d", dim, got, want)
		}
	}
	if got := dotInt8(make([]byte, 4), make([]byte, 5)); got != 0 {
		t.Errorf("length mismatch returned %d, want 0", got)
	}
}

// TestScanQuantOffThenOn is the toggle nobody tests until it corrupts
// something.
//
// Turning the compact copy off has to be more than "stop reading it": a store
// that keeps its completion flag while writing rows the copy never sees is a
// store that, once the knob comes back on, answers searches from a copy
// missing everything written in between. Nothing errors, nothing logs — the
// results are just quietly incomplete, which is the failure mode that survives
// review.
func TestScanQuantOffThenOn(t *testing.T) {
	const dim = 256
	dir := t.TempDir()
	ctx := context.Background()

	on, err := OpenWith(Options{Dir: dir, ScanQuant: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	firstHalf, embs1 := q8Corpus(t, on, "/toggle", 200, dim)
	if err := on.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second half written with the copy disabled.
	off, err := OpenWith(Options{Dir: dir, ScanQuant: false})
	if err != nil {
		t.Fatalf("reopen off: %v", err)
	}
	r := rand.New(rand.NewSource(77))
	secondHalf := make([]Chunk, 200)
	embs2 := make([][]float32, 200)
	for i := range secondHalf {
		secondHalf[i] = Chunk{
			Content:    fmt.Sprintf("late %d", i),
			FilePath:   fmt.Sprintf("late/f%04d.go", i),
			StartLine:  i*10 + 1,
			EndLine:    i*10 + 9,
			ChunkType:  "function",
			SymbolName: fmt.Sprintf("Late%04d", i),
			Language:   "go",
		}
		embs2[i] = randNorm(r, dim)
	}
	if err := off.UpsertChunks(ctx, "/toggle", secondHalf, embs2); err != nil {
		t.Fatalf("upsert while off: %v", err)
	}
	if err := off.Close(); err != nil {
		t.Fatalf("close off: %v", err)
	}

	back, err := OpenWith(Options{Dir: dir, ScanQuant: true})
	if err != nil {
		t.Fatalf("reopen on: %v", err)
	}
	defer back.Close()

	collID, ok, err := back.collectionID(ctx, collectionName("/toggle"))
	if err != nil || !ok {
		t.Fatalf("collection id: %v ok=%v", err, ok)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !back.q8Ready(ctx, collID) {
		if time.Now().After(deadline) {
			t.Fatal("backfill did not re-cover the collection within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	allChunks := append(append([]Chunk{}, firstHalf...), secondHalf...)
	allEmbs := append(append([][]float32{}, embs1...), embs2...)

	// Query at one of the vectors written while the copy was off: if that
	// window were lost, this is what would go missing.
	q := allEmbs[len(embs1)+5]
	got, err := back.Search(ctx, "/toggle", q, 10, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := exactTopK(allChunks, allEmbs, q, 10, ""); strings.Join(resultKeys(got), ",") != strings.Join(want, ",") {
		t.Errorf("rows written while the compact copy was off are missing from search\n got: %v\nwant: %v",
			resultKeys(got), want)
	}
}
