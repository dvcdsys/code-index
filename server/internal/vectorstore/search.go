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
	select {
	case scanSlots <- struct{}{}:
		defer func() { <-scanSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	top := newTopK(k)
	var (
		docID   string
		raw     sql.RawBytes
		scratch []float32
	)
	dim := len(q)
	for rows.Next() {
		// RawBytes avoids a copy of every embedding; it is only valid until
		// the next Next(), which is fine because the dot product consumes it
		// immediately.
		if err := rows.Scan(&docID, &raw); err != nil {
			return nil, err
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
			top.add(candidate{docID: docID, score: score})
		}
	}
	return top, rows.Err()
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
	select {
	case scanSlots <- struct{}{}:
		defer func() { <-scanSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// The query is quantised the same way the stored vectors were, and its
	// scale is constant across the scan, so it cancels out of every
	// comparison. Only the per-row scale has to be applied.
	qq, qScale := quantizeInt8(q)
	if qScale == 0 {
		return nil, nil
	}

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
		docID string
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
			// raw aliases the driver's buffer, docID does not — Scan copies
			// into a string. Nothing kept here outlives this iteration.
			top.add(candidate{docID: docID, score: score})
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
// exact search over 50 real queries, the shortlist contains every document of
// the exact top-K, and rescoring restores the order the approximation blurred
// (see q8Shortlist for the table).
func (s *Store) rescore(ctx context.Context, collID int64, q []float32, shortlist []candidate, limit int) ([]candidate, error) {
	top := newTopK(limit)
	dim := len(q)
	var scratch []float32

	for start := 0; start < len(shortlist); start += hydrateBatch {
		batch := shortlist[start:min(start+hydrateBatch, len(shortlist))]
		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch)+1)
		args = append(args, collID)
		for i, c := range batch {
			placeholders[i] = "?"
			args = append(args, c.docID)
		}
		rows, err := s.db.QueryContext(ctx,
			fmt.Sprintf(rescoreSQL, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return nil, fmt.Errorf("rescore: %w", err)
		}
		for rows.Next() {
			var (
				docID string
				raw   sql.RawBytes
			)
			if err := rows.Scan(&docID, &raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("rescore: %w", err)
			}
			if len(raw)/4 != dim {
				continue
			}
			var vec []float32
			vec, scratch = blobFloats(raw, scratch)
			score := dot(q, vec)
			if top.qualifies(score) {
				top.add(candidate{docID: docID, score: score})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("rescore: %w", err)
		}
		rows.Close()
	}
	return top.sorted(), nil
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
	placeholders := make([]string, len(batch))
	args := make([]any, 0, len(batch)+1)
	args = append(args, collID)
	for i, c := range batch {
		placeholders[i] = "?"
		args = append(args, c.docID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(hydrateSQL, strings.Join(placeholders, ",")), args...)
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
