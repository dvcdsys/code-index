package vectorstore

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
)

// scanSlots caps how many collection scans run at once across the WHOLE
// process, not per query.
//
// A search is a single-threaded scan: measured, splitting one query across
// several workers buys nothing in the low-memory configuration (109 ms at 1
// worker vs 110 ms at 4) because the scan is bound by per-row streaming cost,
// not by arithmetic. What does need bounding is fan-out: thirty concurrent
// agent queries, each over a handful of projects, must queue on a small number
// of scanners rather than spawn a hundred threads and a hundred page caches.
var scanSlots = make(chan struct{}, max(2, runtime.NumCPU()))

// acquireScanSlot takes one of the process-wide scan slots, returning the
// release function. A cancelled context gives up the wait rather than the
// query: a caller that has already gone away must not hold a scanner.
func acquireScanSlot(ctx context.Context) (func(), error) {
	select {
	case scanSlots <- struct{}{}:
		return func() { <-scanSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// scanSQL streams one collection.
//
// INDEXED BY is not an optimisation hint, it is a guarantee, and the choice of
// index is not arbitrary either. Both were measured on a real 312k-document
// index, scanning its largest (74k-row) collection:
//
//	idx_vec_coll       137 ms   keys are (collection_id, rowid)
//	idx_vec_coll_file  244 ms   keys are (collection_id, file_path, rowid)
//	no index           267 ms   walks all 312k rows and discards 76%
//
// idx_vec_coll visits only this collection's rows — however fragmented the
// table has become, and delete-by-file followed by reinsert (every file the
// watcher touches) fragments it continuously — and yields them in table order,
// so the row lookups are sequential. Driving the same scan from the file-path
// index costs 1.8x because its keys are ordered by file_path, scattering the
// lookups across the collection's whole rowid span.
// TestScanUsesCollectionIndex pins the plan.
const scanSQL = `SELECT doc_id, embedding FROM vectors INDEXED BY idx_vec_coll WHERE collection_id = ?`

// scanQ8SQL is the same walk over the compact copy, and it is the one a search
// normally takes. Same INDEXED BY guarantee, same reason: idx_q8_coll's keys
// are (collection_id, rowid), so it visits only this collection's rows and
// yields them in table order.
//
// The row it reads is ~2.1 kB instead of ~8.2 kB, which is the whole point —
// three rows to a leaf page and no overflow chain, against one leaf slice plus
// a dedicated overflow page each. Measured with dbstat: 2731 vs 9216 bytes
// read per vector at 2048 dimensions.
const scanQ8SQL = `SELECT doc_id, scale, embedding FROM vectors_q8 INDEXED BY idx_q8_coll WHERE collection_id = ?`

// rescoreSQL reads the exact vectors of the shortlist.
const rescoreSQL = `SELECT doc_id, embedding FROM vectors WHERE collection_id = ? AND doc_id IN (%s)`

// hydrateSQL fetches the metadata and chunk text of the winners only. The
// LEFT JOIN keeps a result whose content row is somehow missing (which should
// be impossible — both are written in one transaction) instead of dropping it.
const hydrateSQL = `SELECT v.doc_id, v.file_path, v.start_line, v.end_line,
       v.chunk_type, v.symbol_name, v.language, COALESCE(c.content, '')
  FROM vectors v
  LEFT JOIN vector_contents c ON c.collection_id = v.collection_id AND c.doc_id = v.doc_id
 WHERE v.collection_id = ? AND v.doc_id IN (%s)`

// whereColumns maps chromem metadata keys to their SQL column. start_line and
// end_line are integers in the schema but were strings in chromem's metadata,
// so they are compared as text to keep "10" matching 10 and " 10" not matching.
var whereColumns = map[string]string{
	"file_path":   "file_path",
	"chunk_type":  "chunk_type",
	"symbol_name": "symbol_name",
	"language":    "language",
	"start_line":  "CAST(start_line AS TEXT)",
	"end_line":    "CAST(end_line AS TEXT)",
}

// buildWhere translates the metadata filter into SQL.
//
// The semantics mirror chromem-go's documentMatchesFilters exactly: a document
// matches when metadata[k] == v for every entry. Two consequences that look odd
// in SQL and are deliberate:
//
//   - An UNKNOWN key with a non-empty value matches nothing (chromem compared
//     against the zero value of a missing map entry), so we short-circuit the
//     whole query rather than error. Returning no results is what the caller
//     used to get.
//   - An unknown key with an EMPTY value matches everything, so the clause is
//     dropped. Same reason: "" == "" in chromem.
func buildWhere(where map[string]string) (clauses []string, args []any, matchNothing bool) {
	for k, v := range where {
		col, known := whereColumns[k]
		if !known {
			if v == "" {
				continue
			}
			return nil, nil, true
		}
		clauses = append(clauses, col+" = ?")
		args = append(args, v)
	}
	return clauses, args, false
}

// Search performs a nearest-neighbour search using a pre-computed query
// embedding. where is an optional metadata filter (e.g. {"language": "go"}).
//
// limit is honoured as given (0 or less means 10). Large limits are close to
// free: the top-K heap rejects a losing row with one comparison, so k=500
// costs ~1.6% more than k=10.
func (s *Store) Search(ctx context.Context, projectPath string, queryEmbedding []float32, limit int, where map[string]string) ([]SearchResult, error) {
	if !s.acquire() {
		return nil, ErrClosed
	}
	defer s.release()

	if len(queryEmbedding) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	collID, ok, err := s.collectionID(ctx, collectionName(projectPath))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	clauses, args, matchNothing := buildWhere(where)
	if matchNothing {
		return nil, nil
	}
	// Stored vectors are normalised, so cosine similarity is a dot product;
	// the query has to be normalised too. Same tolerance chromem used.
	q := queryEmbedding
	if !isNormalized(q) {
		q = normalizeVector(q)
	}

	best, err := s.rank(ctx, collID, q, limit, where, clauses, args)
	if err != nil {
		return nil, fmt.Errorf("vectorstore search: %w", err)
	}
	if len(best) == 0 {
		return nil, nil
	}
	return s.hydrate(ctx, collID, best)
}

// q8Shortlist is how many candidates the compact scan hands to the rescorer.
//
// The int8 ranking is not the answer, it is a filter: it puts the right
// documents in the shortlist but misorders near-ties, so the shortlist has to
// be wide enough that everything belonging in the top K is inside it. Measured
// on 60k vectors of the fixture's largest collection (ziglang/zig,
// voyage-code-3 @2048) against 50 real query-side embeddings, recall of the
// exact float32 top-K after rescoring:
//
//	shortlist    k=10     k=20
//	        20   0.998    0.994
//	        40   0.998    0.999
//	        60   1.000    1.000
//	       200   1.000    1.000
//
// (Without rescoring at all, the int8 ranking alone gives 0.994 at both k.)
// 60 is where both columns reach 1.000, so the floor is 64 and the multiple is
// 4x for larger k. The cost of a wider shortlist is one float32 row each —
// 9 kB — against a scan that just read thousands of times that, which is why
// the floor is generous rather than tight.
//
// A fixed width cannot be exact in the worst case, and it is worth stating
// which case that is: topK rejects boundary ties strictly, so a collection
// holding more than `shortlist` documents within one quantisation step of each
// other — a file vendored a hundred times, the near-duplicate clusters
// q8Corpus models — truncates the tie in scan order, and the rescore cannot
// recover a document that never reached it. Extending the shortlist to
// swallow ties at the boundary would close it. No corpus measured so far has
// needed that, and the documentation says "measured 1.000", not "exact",
// because of it.
func q8Shortlist(limit int) int {
	if n := 4 * limit; n > 64 {
		return n
	}
	return 64
}

// q8Filterable reports whether the compact scan can answer this filter.
//
// vectors_q8 carries one metadata column, language, because that is the only
// filter any caller actually produces (fetchVectorResults in the HTTP layer,
// from the `languages` query parameter). Anything else — a file_path or
// symbol_name filter, reachable through the Go API but not through HTTP —
// falls back to the float32 scan, which has every column. Slower and correct
// beats fast and wrong.
func q8Filterable(where map[string]string) bool {
	for k, v := range where {
		if k == "language" {
			continue
		}
		// An unknown key with an empty value matches everything and is dropped
		// by buildWhere, so it does not disqualify the fast path.
		if _, known := whereColumns[k]; !known && v == "" {
			continue
		}
		return false
	}
	return true
}

// rank produces the final ordered candidates, by whichever route this
// collection supports.
func (s *Store) rank(ctx context.Context, collID int64, q []float32, limit int,
	where map[string]string, clauses []string, args []any) ([]candidate, error) {

	if !q8Filterable(where) {
		// Correct, and quietly ~3.4x more expensive per vector. Said out loud
		// because the way this gets slow is a new filter key appearing in the
		// HTTP layer: nothing breaks, nothing errors, large collections just
		// go back to reading 9 kB per vector. TestQ8FilterableCoversEveryFilter
		// is the compile-time half of the same guard.
		s.logger.Debug("vectorstore: filter not supported by the compact scan, using the exact one",
			"collection_id", collID, "filter_keys", len(where))
	}
	if q8Filterable(where) && s.q8Ready(ctx, collID) {
		shortlist, err := s.scanQ8(ctx, collID, q, q8Shortlist(limit), where)
		if err != nil {
			return nil, err
		}
		if len(shortlist) == 0 {
			return nil, nil
		}
		return s.rescore(ctx, collID, q, shortlist, limit)
	}

	query := scanSQL
	queryArgs := append([]any{collID}, args...)
	if len(clauses) > 0 {
		query += " AND " + strings.Join(clauses, " AND ")
	}
	top, err := s.scan(ctx, query, queryArgs, q, limit)
	if err != nil {
		return nil, err
	}
	return top.sorted(), nil
}

// scan streams the collection past the dot product, keeping the top K.
func (s *Store) scan(ctx context.Context, query string, args []any, q []float32, k int) (*topK, error) {
	release, err := acquireScanSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	top := newTopK(k)
	if err := streamExact(rows, q, top); err != nil {
		return nil, err
	}
	return top, nil
}

// streamExact drives (doc_id, embedding) rows past the exact dot product and
// keeps the top K. Shared by the float32 scan and by the rescore, which are
// the same loop over the same columns with different WHERE clauses — and the
// float32 decoding protocol is exactly the kind of thing that rots when it
// exists in two places.
//
// Both columns are read as RawBytes, which is the whole point: the driver
// hands back a view of its own buffer, valid only until the next Next(). The
// embedding is consumed immediately by the dot product, and the doc_id becomes
// a Go string only for a row that actually enters the heap — K allocations
// over a scan instead of one per row, which at 1.9M rows per workspace query
// is the difference between a few thousand strings and sixty megabytes of
// garbage.
func streamExact(rows *sql.Rows, q []float32, top *topK) error {
	var (
		docID   sql.RawBytes
		raw     sql.RawBytes
		scratch []float32
	)
	dim := len(q)
	for rows.Next() {
		if err := rows.Scan(&docID, &raw); err != nil {
			return err
		}
		if len(raw)/4 != dim {
			// A row from a different embedding model (namespaces are supposed
			// to isolate dimensions, so this means the directory was mixed by
			// hand). Skipping is safer than scoring it 0, which would let it
			// win a slot in an otherwise empty result.
			continue
		}
		var vec []float32
		vec, scratch = blobFloats(raw, scratch)
		score := dot(q, vec)
		if top.qualifies(score) {
			top.add(candidate{docID: string(docID), score: score})
		}
	}
	return rows.Err()
}

// scanQ8 streams the compact copy and returns the shortlist in approximate
// score order.
//
// The scores it produces are NOT returned to anyone: they rank the shortlist
// and are then thrown away by rescore, which recomputes them on the exact
// vectors. That is deliberate — an int8 dot product is a good enough ordering
// to choose 64 documents out of 350,000 and not good enough to be shown as a
// similarity.
func (s *Store) scanQ8(ctx context.Context, collID int64, q []float32, n int, where map[string]string) ([]candidate, error) {
	release, err := acquireScanSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// The query is quantised the same way the stored vectors were, and its
	// scale is constant across the scan, so it cancels out of every
	// comparison. Only the per-row scale has to be applied.
	//
	// A zero query (scale 0, every component 0) is NOT short-circuited here.
	// It scores every row 0, the heap fills with the first rows it sees, and
	// the caller gets `limit` results at score 0 — which is exactly what the
	// float32 scan does with the same input. Returning nothing instead would
	// be more defensible in isolation and wrong in context: the two paths are
	// chosen per collection, so a workspace fan-out would answer the same
	// broken query with hits from the collections still on float32 and
	// silence from the converted ones.
	qq, _ := quantizeInt8(q)

	query := scanQ8SQL
	args := []any{collID}
	// Presence, not emptiness: chromem compared metadata["language"] against
	// the filter value, so {"language": ""} asks for rows whose language is
	// empty — a real query, not an absent filter. buildWhere gets this right
	// for the float32 path by mapping the key to a column and binding
	// whatever value came with it; treating "" as "no filter" here would make
	// the two paths disagree on the one filter this one supports.
	if language, ok := where["language"]; ok {
		query += " AND language = ?"
		args = append(args, language)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	top := newTopK(n)
	var (
		docID sql.RawBytes
		scale float64
		raw   sql.RawBytes
	)
	dim := len(q)
	for rows.Next() {
		if err := rows.Scan(&docID, &scale, &raw); err != nil {
			return nil, err
		}
		if len(raw) != dim {
			// Same guard as the float32 scan: a row left by a different model.
			continue
		}
		score := float32(scale) * float32(dotInt8(raw, qq))
		if top.qualifies(score) {
			// Both columns alias the driver's buffer until the next Next();
			// the string is materialised only for a row that survives. See
			// streamExact for why that matters at this row count.
			top.add(candidate{docID: string(docID), score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return top.sorted(), nil
}

// rescore recomputes the shortlist's scores on the exact float32 vectors and
// returns the true top `limit`.
//
// This is what makes the compact scan lossless in practice: measured against
// exact search over 50 real queries, the shortlist contained every document of
// the exact top-K, and rescoring restored the order the approximation blurred
// (see q8Shortlist for the table, and for the boundary case a fixed shortlist
// width cannot rule out).
func (s *Store) rescore(ctx context.Context, collID int64, q []float32, shortlist []candidate, limit int) ([]candidate, error) {
	top := newTopK(limit)
	for start := 0; start < len(shortlist); start += hydrateBatch {
		batch := shortlist[start:min(start+hydrateBatch, len(shortlist))]
		query, args := docIDInList(rescoreSQL, collID, batch)
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("rescore: %w", err)
		}
		err = streamExact(rows, q, top)
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("rescore: %w", err)
		}
	}
	return top.sorted(), nil
}

// docIDInList fills a %s-templated IN-list with one placeholder per candidate
// and returns the statement with its arguments, collection first.
//
// Shared by the rescore and the hydrate because they ask the same question of
// the same key — "these doc_ids, in this collection" — and both are already
// chunked by hydrateBatch so neither can exceed SQLite's bound-parameter
// ceiling.
func docIDInList(tmpl string, collID int64, batch []candidate) (string, []any) {
	placeholders := make([]string, len(batch))
	args := make([]any, 0, len(batch)+1)
	args = append(args, collID)
	for i, c := range batch {
		placeholders[i] = "?"
		args = append(args, c.docID)
	}
	return fmt.Sprintf(tmpl, strings.Join(placeholders, ",")), args
}

// hydrateBatch bounds the IN-list so a caller asking for an enormous limit
// cannot exceed SQLite's bound-parameter ceiling.
const hydrateBatch = 500

// hydrate fetches metadata and chunk text for the winning rows and returns
// them in score order.
func (s *Store) hydrate(ctx context.Context, collID int64, best []candidate) ([]SearchResult, error) {
	byDocID := make(map[string]SearchResult, len(best))
	for start := 0; start < len(best); start += hydrateBatch {
		batch := best[start:min(start+hydrateBatch, len(best))]
		if err := s.hydrateInto(ctx, collID, batch, byDocID); err != nil {
			return nil, err
		}
	}

	out := make([]SearchResult, 0, len(best))
	for _, c := range best {
		r, ok := byDocID[c.docID]
		if !ok {
			// Deleted between the scan and the hydrate. Dropping it is the
			// honest answer — the chunk no longer exists.
			continue
		}
		r.Score = round4(c.score)
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// hydrateInto reads one batch of winners into dst.
func (s *Store) hydrateInto(ctx context.Context, collID int64, batch []candidate, dst map[string]SearchResult) error {
	query, args := docIDInList(hydrateSQL, collID, batch)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("vectorstore search hydrate: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			docID string
			r     SearchResult
		)
		if err := rows.Scan(&docID, &r.FilePath, &r.StartLine, &r.EndLine,
			&r.ChunkType, &r.SymbolName, &r.Language, &r.Content); err != nil {
			return fmt.Errorf("vectorstore search hydrate: %w", err)
		}
		dst[docID] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("vectorstore search hydrate: %w", err)
	}
	return nil
}
