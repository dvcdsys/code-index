package vectorstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// The compact scan copy.
//
// vectors_q8 holds every vector at one byte per component. A search scans it
// instead of the float32 table (3.4x fewer bytes, measured — see schemaSQL),
// takes a shortlist, and rescores that shortlist against the float32
// originals, which is what keeps the answer exact.
//
// Everything here exists to answer one question cheaply and correctly: does
// this collection have a q8 row for every vector it has? The answer must not
// cost a COUNT per query, and it must never be "yes" when it is not — a
// half-built q8 table would silently hide documents from search, and a store
// that quietly returns fewer results is worse than a slow one.
//
// The invariant is maintained from both ends:
//
//   - A collection created by this code is born complete: it has no rows, so
//     the empty q8 side matches it, and ensureCollection records that. Every
//     upsert afterwards writes both tables in one transaction, so the property
//     is preserved by construction and never has to be re-checked.
//   - A collection that predates this table has rows and no q8_state row. It
//     stays on the float32 scan — correct, just slower — until the backfill
//     has quantised all of it and records completion in the same transaction
//     as the last batch.
//
// Nothing sets the flag optimistically, and nothing reads q8 without it.
// ---------------------------------------------------------------------------

// q8BackfillBatch is how many vectors one backfill transaction converts.
//
// At 2048 dimensions this reads ~16 MB and writes ~4 MB per batch. Small
// enough that a writer (the indexer, the file watcher) never waits long for
// the write lock, large enough that the per-transaction overhead is noise.
const q8BackfillBatch = 2000

// q8BackfillDuty is the fraction of wall-clock the backfill is allowed to
// spend working. It sleeps for the rest.
//
// The backfill competes with live searches for the same disk and the same two
// vCPUs on the production box, and it is never urgent: until it finishes, the
// affected collections simply search the way they did before. Yielding half
// the time turns "the server is unusable for ten minutes after an upgrade"
// into "it is a bit slower for twenty".
const q8BackfillDuty = 0.5

// markQ8Ready records that a collection's q8 rows are complete.
//
// INSERT OR IGNORE, so calling it for a collection already marked is free and
// keeps the original timestamp — which is the one that says when the data was
// actually built.
func markQ8Ready(ctx context.Context, tx *sql.Tx, collID int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO q8_state(collection_id, built_at) VALUES(?, ?)`,
		collID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("vectorstore: mark q8 ready for collection %d: %w", collID, err)
	}
	return nil
}

// markCollectionQ8Ready records completion outside a caller-owned
// transaction, and updates the in-memory cache so the very next search on a
// freshly created collection takes the fast path.
func (s *Store) markCollectionQ8Ready(ctx context.Context, collID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := markQ8Ready(ctx, tx, collID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.q8Mu.Lock()
	s.q8State[collID] = true
	s.q8Mu.Unlock()
	return nil
}

// clearQ8Ready withdraws a collection's completion flag.
func clearQ8Ready(ctx context.Context, tx *sql.Tx, collID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM q8_state WHERE collection_id = ?`, collID); err != nil {
		return fmt.Errorf("vectorstore: clear q8 state for collection %d: %w", collID, err)
	}
	return nil
}

// q8Ready reports whether the scan may read vectors_q8 for this collection.
//
// Cached in memory because it is consulted on every search and the answer only
// ever changes in one direction (not ready -> ready, once, when the backfill
// finishes). A false answer is therefore worth re-checking; a true one is not.
func (s *Store) q8Ready(ctx context.Context, collID int64) bool {
	if !s.scanQuant {
		return false
	}
	s.q8Mu.Lock()
	ready, known := s.q8State[collID]
	s.q8Mu.Unlock()
	if known && ready {
		return true
	}

	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM q8_state WHERE collection_id = ?`, collID).Scan(&one)
	switch {
	case err == nil:
		ready = true
	case err == sql.ErrNoRows:
		ready = false
	default:
		// A probe that errors must not upgrade the scan: falling back to the
		// float32 path answers the query correctly.
		s.logger.Warn("vectorstore: q8 readiness probe failed", "collection_id", collID, "err", err)
		return false
	}
	s.q8Mu.Lock()
	s.q8State[collID] = ready
	s.q8Mu.Unlock()
	return ready
}

// forgetQ8 drops a collection's cached readiness (after a delete). The next
// collection to be handed this id — which AUTOINCREMENT guarantees is never
// this one — must not inherit its answer.
func (s *Store) forgetQ8(collID int64) {
	s.q8Mu.Lock()
	delete(s.q8State, collID)
	s.q8Mu.Unlock()
}

// startQ8Backfill converts collections written before vectors_q8 existed.
//
// Runs in the background and returns immediately: the store is fully usable
// while it works, because an unconverted collection is not broken, only slow.
// That is the whole reason this is a background job and not part of Open — the
// alternative is a server that answers nothing for the minutes it takes to
// rewrite a multi-gigabyte store, which is exactly the failure mode the schema
// rebuild already has and which took three false "the server is down" reports
// to diagnose.
func (s *Store) startQ8Backfill(ctx context.Context) {
	go func() {
		if err := s.backfillQ8(ctx); err != nil && ctx.Err() == nil {
			// Warn, not fatal: every collection it failed to convert keeps
			// searching the float32 way.
			s.logger.Warn("vectorstore: building the compact scan index stopped early", "err", err)
		}
	}()
}

// pendingQ8Collections lists collections that have vectors but no completed q8
// copy, largest first — so the collection whose searches hurt most is the
// first one to get faster.
func (s *Store) pendingQ8Collections(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.collection_id, COUNT(*) n
		  FROM vectors v
		 WHERE v.collection_id NOT IN (SELECT collection_id FROM q8_state)
		 GROUP BY v.collection_id
		 ORDER BY n DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// backfillQ8 quantises every pending collection.
func (s *Store) backfillQ8(ctx context.Context) error {
	if !s.acquire() {
		return nil
	}
	pending, err := s.pendingQ8Collections(ctx)
	s.release()
	if err != nil {
		return fmt.Errorf("list collections needing a scan copy: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	// The scan copy is a quarter of the float32 bytes it is derived from.
	// Refusing up front beats discovering it a gigabyte in: a failed backfill
	// leaves the store fully working, but it also leaves the disk fuller than
	// it needs to be and the failure buried in a log line.
	if need, err := s.pendingQ8Bytes(ctx); err == nil {
		if err := checkFreeSpace(s.dir, need); err != nil {
			s.logger.Warn("vectorstore: skipping the compact scan index — not enough free disk",
				"db", s.dbPath, "need_mb", need/(1<<20), "err", err)
			return nil
		}
	}

	// WARN for the same reason the schema rebuild and the legacy import log at
	// warn: production runs at warn level, and unexplained background I/O on a
	// box that was just restarted is indistinguishable from a problem.
	started := time.Now()
	s.logger.Warn("vectorstore: building the compact scan index in the background",
		"db", s.dbPath, "collections", len(pending))

	var converted int64
	for _, collID := range pending {
		n, err := s.backfillCollection(ctx, collID)
		converted += n
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("collection %d: %w", collID, err)
		}
	}
	s.logger.Warn("vectorstore: compact scan index built",
		"db", s.dbPath, "collections", len(pending), "vectors", converted,
		"took", time.Since(started).Round(time.Second))
	return nil
}

// pendingQ8Bytes estimates the disk the backfill will add: one byte per
// component of every vector it has to convert, plus the row overhead the size
// accounting already uses for the compact table.
func (s *Store) pendingQ8Bytes(ctx context.Context) (int64, error) {
	if !s.acquire() {
		return 0, ErrClosed
	}
	defer s.release()
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(LENGTH(embedding)/4 + LENGTH(doc_id) + LENGTH(language) + 16), 0)
		  FROM vectors
		 WHERE collection_id NOT IN (SELECT collection_id FROM q8_state)`).Scan(&n)
	return n, err
}

// backfillCollection walks one collection in doc_id order, quantising as it
// goes, and marks the collection ready in the same transaction as its last
// batch — so a kill at any point leaves a collection that is unmarked and
// therefore still searchable the float32 way, never one that is marked and
// incomplete.
func (s *Store) backfillCollection(ctx context.Context, collID int64) (int64, error) {
	var (
		after     string
		converted int64
	)
	for {
		if ctx.Err() != nil {
			return converted, ctx.Err()
		}
		batchStart := time.Now()
		n, last, err := s.backfillBatch(ctx, collID, after)
		if err != nil {
			return converted, err
		}
		converted += n
		if n == 0 {
			return converted, nil
		}
		after = last

		// Yield. Sleeping proportionally to the work just done keeps the duty
		// cycle honest whether a batch took 40 ms on an NVMe laptop or four
		// seconds on a network disk.
		select {
		case <-ctx.Done():
			return converted, ctx.Err()
		case <-time.After(time.Duration(float64(time.Since(batchStart)) * (1 - q8BackfillDuty) / q8BackfillDuty)):
		}
	}
}

// backfillBatch converts up to q8BackfillBatch vectors whose doc_id sorts
// after `after`, and returns how many it converted and the last doc_id it saw.
//
// Keyset pagination rather than OFFSET: the walk must resume where it stopped
// without re-reading everything before it, and doc_ids are unique within a
// collection, which makes them a total order to page over.
func (s *Store) backfillBatch(ctx context.Context, collID int64, after string) (int64, string, error) {
	if !s.acquire() {
		return 0, "", ErrClosed
	}
	defer s.release()

	type q8Row struct {
		docID    string
		language string
		scale    float32
		blob     []byte
	}
	batch := make([]q8Row, 0, q8BackfillBatch)
	last := after

	// Read and quantise first, write second. Holding a write transaction open
	// across a streaming read would keep the write lock for the whole batch,
	// and the thing most likely to want that lock is the file watcher
	// reindexing a file someone just saved.
	rows, err := s.db.QueryContext(ctx, `
		SELECT doc_id, language, embedding FROM vectors
		 WHERE collection_id = ? AND doc_id > ?
		 ORDER BY doc_id LIMIT ?`, collID, after, q8BackfillBatch)
	if err != nil {
		return 0, "", fmt.Errorf("read vectors: %w", err)
	}
	var scratch []float32
	for rows.Next() {
		var (
			docID, language string
			raw             sql.RawBytes
		)
		if err := rows.Scan(&docID, &language, &raw); err != nil {
			rows.Close()
			return 0, "", fmt.Errorf("scan vector: %w", err)
		}
		var vec []float32
		vec, scratch = blobFloats(raw, scratch)
		// quantizeInt8 allocates its own output, so nothing here outlives the
		// RawBytes it was derived from.
		blob, scale := quantizeInt8(vec)
		batch = append(batch, q8Row{docID: docID, language: language, scale: scale, blob: blob})
		last = docID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, "", fmt.Errorf("read vectors: %w", err)
	}
	rows.Close()
	if len(batch) == 0 {
		// The collection is fully converted. Marking it here — rather than
		// after the loop in the caller — keeps "the data is complete" and "the
		// flag says so" in the same transaction as the query that proved it.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, "", err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
		if err := markQ8Ready(ctx, tx, collID); err != nil {
			return 0, "", err
		}
		if err := tx.Commit(); err != nil {
			return 0, "", err
		}
		s.q8Mu.Lock()
		s.q8State[collID] = true
		s.q8Mu.Unlock()
		return 0, last, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	stmt, err := tx.PrepareContext(ctx, upsertQ8SQL)
	if err != nil {
		return 0, "", err
	}
	defer stmt.Close()
	for _, r := range batch {
		if _, err := stmt.ExecContext(ctx, collID, r.docID, r.language, r.scale, r.blob); err != nil {
			return 0, "", fmt.Errorf("write q8 row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return int64(len(batch)), last, nil
}
