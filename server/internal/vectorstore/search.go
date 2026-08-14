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
const scanSQL = `SELECT rowid, embedding FROM vectors INDEXED BY idx_vec_coll WHERE collection_id = ?`

// hydrateSQL fetches the metadata and chunk text of the winners only. The
// LEFT JOIN keeps a result whose content row is somehow missing (which should
// be impossible — both are written in one transaction) instead of dropping it.
const hydrateSQL = `SELECT v.rowid, v.file_path, v.start_line, v.end_line,
       v.chunk_type, v.symbol_name, v.language, COALESCE(c.content, '')
  FROM vectors v
  LEFT JOIN vector_contents c ON c.collection_id = v.collection_id AND c.doc_id = v.doc_id
 WHERE v.rowid IN (%s)`

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

	query := scanSQL
	queryArgs := append([]any{collID}, args...)
	if len(clauses) > 0 {
		query += " AND " + strings.Join(clauses, " AND ")
	}

	top, err := s.scan(ctx, query, queryArgs, q, limit)
	if err != nil {
		return nil, fmt.Errorf("vectorstore search: %w", err)
	}
	best := top.sorted()
	if len(best) == 0 {
		return nil, nil
	}
	return s.hydrate(ctx, best)
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
		rowID   int64
		raw     sql.RawBytes
		scratch []float32
	)
	dim := len(q)
	for rows.Next() {
		// RawBytes avoids a copy of every 3 kB embedding; it is only valid
		// until the next Next(), which is fine because the dot product
		// consumes it immediately.
		if err := rows.Scan(&rowID, &raw); err != nil {
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
			top.add(candidate{rowID: rowID, score: score})
		}
	}
	return top, rows.Err()
}

// hydrateBatch bounds the IN-list so a caller asking for an enormous limit
// cannot exceed SQLite's bound-parameter ceiling.
const hydrateBatch = 500

// hydrate fetches metadata and chunk text for the winning rows and returns
// them in score order.
func (s *Store) hydrate(ctx context.Context, best []candidate) ([]SearchResult, error) {
	byRowID := make(map[int64]SearchResult, len(best))
	for start := 0; start < len(best); start += hydrateBatch {
		batch := best[start:min(start+hydrateBatch, len(best))]
		if err := s.hydrateInto(ctx, batch, byRowID); err != nil {
			return nil, err
		}
	}

	out := make([]SearchResult, 0, len(best))
	for _, c := range best {
		r, ok := byRowID[c.rowID]
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
func (s *Store) hydrateInto(ctx context.Context, batch []candidate, dst map[int64]SearchResult) error {
	placeholders := make([]string, len(batch))
	args := make([]any, len(batch))
	for i, c := range batch {
		placeholders[i] = "?"
		args[i] = c.rowID
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(hydrateSQL, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return fmt.Errorf("vectorstore search hydrate: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			rowID int64
			r     SearchResult
		)
		if err := rows.Scan(&rowID, &r.FilePath, &r.StartLine, &r.EndLine,
			&r.ChunkType, &r.SymbolName, &r.Language, &r.Content); err != nil {
			return fmt.Errorf("vectorstore search hydrate: %w", err)
		}
		dst[rowID] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("vectorstore search hydrate: %w", err)
	}
	return nil
}
