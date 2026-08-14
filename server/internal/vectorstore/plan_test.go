package vectorstore

import (
	"context"
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN rows for a statement, joined.
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return strings.Join(lines, "\n")
}

// TestScanUsesCollectionIndex pins the one thing the scan's performance
// depends on: it must walk idx_vec_coll — whose keys are (collection_id,
// rowid) — and not the table btree, and not the file-path index.
//
// Delete-by-file plus reinsert (every save the file watcher sees) scatters a
// collection's rows across the whole table, so a plain scan reads and discards
// rows belonging to other collections. idx_vec_coll keeps the scan
// proportional to the collection AND in table order; the otherwise-usable
// idx_vec_coll_file costs 1.8x because it hands the rows back in file_path
// order. If a schema change ever makes SQLite pick something else, this fails
// instead of the server quietly getting slower.
func TestScanUsesCollectionIndex(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	chunks, embs := makeChunks(20, "a.go", "go")
	if err := s.UpsertChunks(ctx, "/plan", chunks, embs); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	plan := queryPlan(t, s, scanSQL, 1)
	if !strings.Contains(plan, "idx_vec_coll ") && !strings.HasSuffix(plan, "idx_vec_coll") &&
		!strings.Contains(plan, "idx_vec_coll (collection_id") {
		t.Errorf("scan plan does not use idx_vec_coll:\n%s", plan)
	}
	if strings.Contains(plan, "idx_vec_coll_file") {
		t.Errorf("scan plan uses the file-path index (1.8x slower):\n%s", plan)
	}
	if strings.Contains(plan, "SCAN vectors\n") || strings.HasSuffix(plan, "SCAN vectors") {
		t.Errorf("scan plan falls back to a full table scan:\n%s", plan)
	}

	// The same must hold with a metadata filter appended — the filter is an
	// extra predicate, not a reason to pick another index.
	filtered := scanSQL + " AND language = ?"
	plan = queryPlan(t, s, filtered, 1, "go")
	if strings.Contains(plan, "idx_vec_coll_file") || !strings.Contains(plan, "idx_vec_coll") {
		t.Errorf("filtered scan plan does not use idx_vec_coll:\n%s", plan)
	}

	// Delete-by-file must be an index seek, not a scan.
	plan = queryPlan(t, s, `DELETE FROM vectors WHERE collection_id = ? AND file_path = ?`, 1, "a.go")
	if !strings.Contains(plan, "idx_vec_coll_file") {
		t.Errorf("delete-by-file plan does not use idx_vec_coll_file:\n%s", plan)
	}
}
