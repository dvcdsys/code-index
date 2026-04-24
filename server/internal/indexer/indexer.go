// Package indexer ports api/app/services/indexer.py three-phase protocol to Go.
// It orchestrates chunker → embeddings → vectorstore + symbolindex on top of
// SQLite session state. Handlers call BeginIndexing, ProcessFiles (one or more
// times), then FinishIndexing using a shared run_id.
package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/chunker"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// sessionTTL mirrors Python's 1-hour session garbage collector.
const sessionTTL = time.Hour

// cleanupDelay mirrors Python's 60s post-finish cleanup window.
const cleanupDelay = 60 * time.Second

// FilePayload matches api/app/schemas/indexing.py FilePayload.
type FilePayload struct {
	Path        string
	Content     string
	ContentHash string
	Language    string
	Size        int
}

// Progress mirrors Python IndexProgress for GET /index/status.
type Progress struct {
	Status           string
	Phase            string
	FilesDiscovered  int
	FilesProcessed   int
	FilesTotal       int
	ChunksCreated    int
	ElapsedSeconds   float64
	RunID            string
}

// Session is the in-memory state of an active indexing run.
type session struct {
	runID           string
	projectPath     string
	filesDiscovered int // last CLI-reported total from /index/finish or batch payloads
	filesProcessed  int
	chunksCreated   int
	languagesSeen   map[string]struct{}
	startTime       time.Time
	status          string // active|completed
	phase           string // receiving|completed
}

// Embedder is the minimal embeddings surface the indexer consumes. The real
// implementation is *embeddings.Service; tests substitute a fake.
type Embedder interface {
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// TokenAwareEmbedder extends Embedder with the token-level pipeline:
// tokenize → split-at-token-boundary if needed → embed by token IDs.
// *embeddings.Service satisfies this interface; fakeEmbedder in tests does
// not, so ProcessFiles falls back to EmbedTexts for unit tests.
type TokenAwareEmbedder interface {
	Embedder
	TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error)
}

// Service owns sessions and wires dependencies for the three-phase protocol.
type Service struct {
	db     *sql.DB
	vs     *vectorstore.Store
	emb    Embedder
	logger *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*session // runID → state

	// stopCh is closed when Shutdown is called. Housekeeping goroutines
	// (ttlCleanup, delayedCleanup) select on it so they unblock promptly
	// instead of leaking for up to sessionTTL on server shutdown.
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New constructs a Service. All deps are required except logger (falls back to
// slog.Default).
func New(db *sql.DB, vs *vectorstore.Store, emb Embedder, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db:       db,
		vs:       vs,
		emb:      emb,
		logger:   logger,
		sessions: make(map[string]*session),
		stopCh:   make(chan struct{}),
	}
}

// Shutdown signals all housekeeping goroutines to exit. Safe to call multiple
// times. Callers should invoke this before closing the DB.
func (s *Service) Shutdown() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// ---------------------------------------------------------------------------
// Phase 1 — begin
// ---------------------------------------------------------------------------

// BeginIndexing creates a run row, returns stored file hashes for diffing, and
// wipes the project's data if full=true. Mirrors indexer.py begin_indexing.
//
// Concurrency: at most one active session per project is allowed. A second
// concurrent /index/begin for the same project returns ErrSessionConflict,
// which the HTTP handler maps to 409 Conflict. Python coincidentally serialises
// this via single-threaded asyncio; Go uses explicit guard.
func (s *Service) BeginIndexing(ctx context.Context, projectPath string, full bool) (string, map[string]string, error) {
	// C2 — reject a second /index/begin for the same project while another run
	// is active. Must hold the write lock across check-and-insert so two racing
	// callers cannot both see "no active session" and both proceed.
	runID := uuid.NewString()
	s.mu.Lock()
	for _, e := range s.sessions {
		if e.projectPath == projectPath && e.status == "active" {
			s.mu.Unlock()
			return "", nil, fmt.Errorf("%w: project=%q existing_run=%q",
				ErrSessionConflict, projectPath, e.runID)
		}
	}
	// Reserve the session slot before any DB work so a parallel call sees it
	// immediately. The session is finalised with languagesSeen, startTime
	// after we know the begin succeeded.
	s.sessions[runID] = &session{
		runID:         runID,
		projectPath:   projectPath,
		languagesSeen: map[string]struct{}{},
		startTime:     time.Now(),
		status:        "active",
		phase:         "receiving",
	}
	s.mu.Unlock()

	// Clean up the reservation on any error path.
	commit := false
	defer func() {
		if !commit {
			s.mu.Lock()
			delete(s.sessions, runID)
			s.mu.Unlock()
		}
	}()

	now := nowUTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO index_runs (id, project_path, started_at, status) VALUES (?, ?, ?, ?)`,
		runID, projectPath, now, "running",
	); err != nil {
		return "", nil, fmt.Errorf("insert index_runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'indexing', updated_at = ? WHERE host_path = ?`,
		now, projectPath,
	); err != nil {
		return "", nil, fmt.Errorf("update project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}

	storedHashes := map[string]string{}

	if full {
		// M1 — commit the DB wipe first; DeleteCollection is irreversible and
		// must run last so a DB failure does not leave file_hashes pointing at
		// already-deleted vectors (would skip re-indexing on next incremental).
		tx2, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", nil, fmt.Errorf("begin tx (full): %w", err)
		}
		defer tx2.Rollback() //nolint:errcheck
		for _, q := range []string{
			`DELETE FROM file_hashes WHERE project_path = ?`,
			`DELETE FROM symbols WHERE project_path = ?`,
			`DELETE FROM refs WHERE project_path = ?`,
		} {
			if _, err := tx2.ExecContext(ctx, q, projectPath); err != nil {
				return "", nil, fmt.Errorf("full wipe: %w", err)
			}
		}
		if err := tx2.Commit(); err != nil {
			return "", nil, fmt.Errorf("commit (full): %w", err)
		}
		if s.vs != nil {
			if err := s.vs.DeleteCollection(projectPath); err != nil {
				// Not fatal: collection may not exist yet. Worst case: vectors
				// stay but DB is empty, and the next full reindex cleans up.
				s.logger.Warn("delete collection on full reindex", "err", err)
			}
		}
	} else {
		rows, err := s.db.QueryContext(ctx,
			`SELECT file_path, content_hash FROM file_hashes WHERE project_path = ?`,
			projectPath,
		)
		if err != nil {
			return "", nil, fmt.Errorf("query file_hashes: %w", err)
		}
		for rows.Next() {
			var fp, hash string
			if err := rows.Scan(&fp, &hash); err != nil {
				rows.Close()
				return "", nil, fmt.Errorf("scan file_hashes: %w", err)
			}
			storedHashes[fp] = hash
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", nil, fmt.Errorf("iterate file_hashes: %w", err)
		}
		rows.Close()
	}

	commit = true
	go s.ttlCleanup(runID)

	return runID, storedHashes, nil
}

// ---------------------------------------------------------------------------
// Phase 2 — process files
// ---------------------------------------------------------------------------

// ProcessFiles chunks, embeds, and stores a batch of files. Returns
// (filesAccepted, chunksCreated, filesProcessedTotal, err).
//
// On embeddings.ErrBusy the error is returned unchanged so the HTTP handler can
// emit 503 + Retry-After.
//
// Transactions (M2+M3): every per-file DB write (file_hashes upsert + symbols
// delete + refs delete) lives inside a SAVEPOINT. On any error for that file
// the savepoint is rolled back — the vector store side is reverted via
// DeleteByFile best-effort, but we accept it may leak vectors since vectorstore
// has no transactions. End-of-batch batchSymbols/batchRefs are written inside
// the outer transaction so a late error rolls back the whole batch cleanly.
func (s *Service) ProcessFiles(
	ctx context.Context,
	projectPath, runID string,
	files []FilePayload,
) (int, int, int, error) {
	sess, err := s.requireSession(runID, projectPath)
	if err != nil {
		return 0, 0, 0, err
	}

	s.logger.Info("indexer: processing batch", "run_id", runID, "files", len(files))

	now := nowUTC()
	filesAccepted := 0
	batchChunks := 0
	var batchSymbols []symbolindex.Symbol
	var batchRefs []symbolindex.Reference

	// maxContentBytes guards against files that grew past the CLI's MaxFileSize
	// filter between discovery and indexing (e.g. a log file written in-flight).
	// 512 KB matches the CLI default; above this the tokenise loop would hold
	// the queue slot for tens of seconds per file.
	const maxContentBytes = 512 * 1024

	// Open the per-batch transaction. Every per-file DB change lives inside a
	// SAVEPOINT of this tx so a single bad file only rolls back that file's
	// rows, not the whole batch.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin batch tx: %w", err)
	}
	txCommitted := false
	defer func() {
		if !txCommitted {
			_ = tx.Rollback()
		}
	}()

	for _, fp := range files {
		if strings.TrimSpace(fp.Content) == "" {
			continue
		}
		if len(fp.Content) > maxContentBytes {
			s.logger.Warn("indexer: file too large, skipping", "path", fp.Path, "size_bytes", len(fp.Content))
			continue
		}

		language := fp.Language
		if language == "" {
			language = "text"
		}

		chunks, refs, err := chunker.ChunkFile(fp.Path, fp.Content, language, 0)
		if err != nil {
			s.logger.Warn("indexer: chunk file failed", "path", fp.Path, "err", err)
			continue
		}
		if len(chunks) == 0 {
			continue
		}

		// Symbol extraction — mirrors Python: function|class|method|type with a name.
		fileSymbols := make([]symbolindex.Symbol, 0, len(chunks))
		for _, c := range chunks {
			if c.SymbolName == nil {
				continue
			}
			switch c.ChunkType {
			case "function", "class", "method", "type":
			default:
				continue
			}
			fileSymbols = append(fileSymbols, symbolindex.Symbol{
				Name:       *c.SymbolName,
				Kind:       c.ChunkType,
				FilePath:   c.FilePath,
				Line:       c.StartLine,
				EndLine:    c.EndLine,
				Language:   c.Language,
				Signature:  c.SymbolSignature,
				ParentName: c.ParentName,
			})
		}

		fileRefs := make([]symbolindex.Reference, 0, len(refs))
		for _, r := range refs {
			fileRefs = append(fileRefs, symbolindex.Reference{
				Name:     r.Name,
				FilePath: r.FilePath,
				Line:     r.Line,
				Col:      r.Col,
				Language: r.Language,
			})
		}

		// Embed. Python prefixes with "{chunk_type}: {content}".
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.ChunkType + ": " + c.Content
		}
		var embs [][]float32
		if tae, ok := s.emb.(TokenAwareEmbedder); ok {
			embs, err = tae.TokenizeAndEmbed(ctx, texts)
		} else {
			embs, err = s.emb.EmbedTexts(ctx, texts)
		}
		if err != nil {
			// Propagate ErrBusy so handler can map to 503 + Retry-After.
			if _, busy := embeddings.IsBusy(err); busy {
				return filesAccepted, batchChunks, sess.filesProcessed, err
			}
			if errors.Is(err, embeddings.ErrDisabled) ||
				errors.Is(err, embeddings.ErrSupervisor) ||
				errors.Is(err, embeddings.ErrNotReady) {
				return filesAccepted, batchChunks, sess.filesProcessed, err
			}
			s.logger.Error("indexer: embed texts failed", "path", fp.Path, "err", err)
			continue
		}

		// Per-file SAVEPOINT so a partial failure rolls back only this file.
		// savepointName is derived from filesAccepted (monotonically increasing
		// within the tx) so nested savepoints cannot collide.
		savepointName := fmt.Sprintf("f%d", filesAccepted)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepointName); err != nil {
			return filesAccepted, batchChunks, sess.filesProcessed, fmt.Errorf("savepoint: %w", err)
		}
		// Rollback helper for the failure path below.
		rollback := func() {
			_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
			_, _ = tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepointName)
		}

		// Delete old symbols/refs before insert (matches Python).
		if err := symbolindex.DeleteByFileTx(ctx, tx, projectPath, fp.Path); err != nil {
			s.logger.Error("indexer: symbols delete by file", "path", fp.Path, "err", err)
			rollback()
			continue
		}
		if err := symbolindex.DeleteRefsByFileTx(ctx, tx, projectPath, fp.Path); err != nil {
			s.logger.Error("indexer: refs delete by file", "path", fp.Path, "err", err)
			rollback()
			continue
		}

		// Vector store has no transactions — delete is best-effort. If the
		// savepoint rolls back below we leave any vectors in place; they get
		// overwritten on the next successful indexing of this file.
		if s.vs != nil {
			if err := s.vs.DeleteByFile(ctx, projectPath, fp.Path); err != nil {
				s.logger.Error("indexer: vectorstore delete by file", "path", fp.Path, "err", err)
				rollback()
				continue
			}
		}

		// Upsert chunks.
		vsChunks := make([]vectorstore.Chunk, len(chunks))
		for i, c := range chunks {
			sym := ""
			if c.SymbolName != nil {
				sym = *c.SymbolName
			}
			vsChunks[i] = vectorstore.Chunk{
				Content:    c.Content,
				FilePath:   c.FilePath,
				StartLine:  c.StartLine,
				EndLine:    c.EndLine,
				ChunkType:  c.ChunkType,
				SymbolName: sym,
				Language:   c.Language,
			}
		}
		if s.vs != nil {
			if err := s.vs.UpsertChunks(ctx, projectPath, vsChunks, embs); err != nil {
				s.logger.Error("indexer: vectorstore upsert", "path", fp.Path, "err", err)
				rollback()
				continue
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO file_hashes
			 (project_path, file_path, content_hash, indexed_at)
			 VALUES (?, ?, ?, ?)`,
			projectPath, fp.Path, fp.ContentHash, now,
		); err != nil {
			s.logger.Error("indexer: file_hashes upsert", "path", fp.Path, "err", err)
			rollback()
			continue
		}

		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepointName); err != nil {
			return filesAccepted, batchChunks, sess.filesProcessed, fmt.Errorf("release savepoint: %w", err)
		}

		batchChunks += len(chunks)
		batchSymbols = append(batchSymbols, fileSymbols...)
		batchRefs = append(batchRefs, fileRefs...)

		s.mu.Lock()
		sess.languagesSeen[language] = struct{}{}
		s.mu.Unlock()
		filesAccepted++
	}

	// M2 — these upserts are part of the outer tx. Any failure returns the
	// whole batch's work via deferred tx.Rollback, so the session counters
	// below only advance on a successful commit.
	if len(batchSymbols) > 0 {
		if err := symbolindex.UpsertSymbolsTx(ctx, tx, projectPath, batchSymbols); err != nil {
			return filesAccepted, batchChunks, sess.filesProcessed, fmt.Errorf("upsert symbols: %w", err)
		}
	}
	if len(batchRefs) > 0 {
		if err := symbolindex.UpsertReferencesTx(ctx, tx, projectPath, batchRefs); err != nil {
			return filesAccepted, batchChunks, sess.filesProcessed, fmt.Errorf("upsert refs: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return filesAccepted, batchChunks, sess.filesProcessed, fmt.Errorf("commit batch: %w", err)
	}
	txCommitted = true

	s.mu.Lock()
	sess.filesProcessed += filesAccepted
	sess.chunksCreated += batchChunks
	total := sess.filesProcessed
	s.mu.Unlock()

	s.logger.Info("indexer: batch done",
		"run_id", runID,
		"files_accepted", filesAccepted,
		"chunks", batchChunks,
		"total_files", total,
	)

	return filesAccepted, batchChunks, total, nil
}

// ---------------------------------------------------------------------------
// Phase 3 — finish
// ---------------------------------------------------------------------------

// FinishIndexing deletes `deletedPaths`, updates project stats, closes the run.
// Returns (status, filesProcessed, chunksCreated, err).
func (s *Service) FinishIndexing(
	ctx context.Context,
	projectPath, runID string,
	deletedPaths []string,
	totalFilesDiscovered int,
) (string, int, int, error) {
	sess, err := s.requireSession(runID, projectPath)
	if err != nil {
		return "", 0, 0, err
	}

	// Record the CLI's discovery count for GET /index/status responses
	// received between here and cleanup. m4 fix.
	s.mu.Lock()
	sess.filesDiscovered = totalFilesDiscovered
	s.mu.Unlock()

	now := nowUTC()

	for _, dp := range deletedPaths {
		if s.vs != nil {
			if err := s.vs.DeleteByFile(ctx, projectPath, dp); err != nil {
				s.logger.Warn("indexer: vectorstore delete by file (finish)", "path", dp, "err", err)
			}
		}
		if err := symbolindex.DeleteByFile(ctx, s.db, projectPath, dp); err != nil {
			s.logger.Warn("indexer: symbols delete by file (finish)", "path", dp, "err", err)
		}
		if err := symbolindex.DeleteRefsByFile(ctx, s.db, projectPath, dp); err != nil {
			s.logger.Warn("indexer: refs delete by file (finish)", "path", dp, "err", err)
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM file_hashes WHERE project_path = ? AND file_path = ?`,
			projectPath, dp,
		); err != nil {
			s.logger.Warn("indexer: file_hashes delete (finish)", "path", dp, "err", err)
		}
	}

	// Accurate totals from DB.
	var totalIndexedFiles int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file_hashes WHERE project_path = ?`, projectPath,
	).Scan(&totalIndexedFiles); err != nil {
		totalIndexedFiles = sess.filesProcessed
	}

	var totalSymbols int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM symbols WHERE project_path = ?`, projectPath,
	).Scan(&totalSymbols); err != nil {
		totalSymbols = 0
	}

	totalChunks := sess.chunksCreated
	if s.vs != nil {
		totalChunks = s.vs.Count(projectPath)
	}

	// Collect all languages from indexed files (from disk-based detect).
	langs, err := s.collectLanguages(ctx, projectPath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("collect languages: %w", err)
	}

	statsJSON := fmt.Sprintf(
		`{"total_files":%d,"indexed_files":%d,"total_chunks":%d,"total_symbols":%d}`,
		totalFilesDiscovered, totalIndexedFiles, totalChunks, totalSymbols,
	)
	langsJSON := marshalJSONStringArray(langs)

	if _, err := s.db.ExecContext(ctx,
		`UPDATE projects
		 SET stats = ?, languages = ?, status = 'indexed',
		     last_indexed_at = ?, updated_at = ?
		 WHERE host_path = ?`,
		statsJSON, langsJSON, now, now, projectPath,
	); err != nil {
		return "", 0, 0, fmt.Errorf("update project stats: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE index_runs
		 SET status = 'completed', completed_at = ?,
		     files_processed = ?, chunks_created = ?
		 WHERE id = ?`,
		now, sess.filesProcessed, sess.chunksCreated, runID,
	); err != nil {
		return "", 0, 0, fmt.Errorf("update index_run: %w", err)
	}

	s.mu.Lock()
	sess.status = "completed"
	sess.phase = "completed"
	filesProcessed := sess.filesProcessed
	chunksCreated := sess.chunksCreated
	s.mu.Unlock()

	go s.delayedCleanup(runID)

	return "completed", filesProcessed, chunksCreated, nil
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

// CancelIndexing terminates any active session for the given project. It is
// idempotent: returns (false, nil) when no active session exists. Used by the
// CLI watcher's stale-session guard at startup (prior `cix watch` that crashed
// between begin and finish would otherwise leave a live session blocking the
// next begin with 409 Conflict).
//
// Cancelling does not roll back chunks/symbols already persisted by
// ProcessFiles batches that committed before the cancel — the next reindex
// will overwrite them. This matches Python's cancel semantics.
func (s *Service) CancelIndexing(ctx context.Context, projectPath string) (bool, error) {
	s.mu.Lock()
	var cancelledRunID string
	for id, sess := range s.sessions {
		if sess.projectPath == projectPath && sess.status == "active" {
			cancelledRunID = id
			break
		}
	}
	if cancelledRunID == "" {
		s.mu.Unlock()
		return false, nil
	}
	delete(s.sessions, cancelledRunID)
	s.mu.Unlock()

	now := nowUTC()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE index_runs SET status = 'cancelled', completed_at = ? WHERE id = ?`,
		now, cancelledRunID,
	); err != nil {
		return true, fmt.Errorf("update index_runs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE projects SET status = 'indexed', updated_at = ? WHERE host_path = ?`,
		now, projectPath,
	); err != nil {
		return true, fmt.Errorf("update project: %w", err)
	}

	s.logger.Info("indexer: session cancelled", "run_id", cancelledRunID, "project", projectPath)
	return true, nil
}

// ---------------------------------------------------------------------------
// Status + session helpers
// ---------------------------------------------------------------------------

// GetProgress returns the active session progress for a project, or nil if no
// active session. Mirrors Python get_progress.
func (s *Service) GetProgress(projectPath string) *Progress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.projectPath == projectPath {
			return &Progress{
				RunID:           sess.runID,
				Status:          sessStatusToHTTP(sess.status),
				Phase:           sess.phase,
				FilesDiscovered: sess.filesDiscovered,
				FilesProcessed:  sess.filesProcessed,
				FilesTotal:      sess.filesDiscovered, // CLI's reported total, best-known estimate mid-run
				ChunksCreated:   sess.chunksCreated,
				ElapsedSeconds:  time.Since(sess.startTime).Seconds(),
			}
		}
	}
	return nil
}

// ErrNoSession signals that a request references an unknown run_id.
var ErrNoSession = errors.New("indexer: no active session for run_id")

// ErrProjectMismatch signals that the run_id belongs to a different project.
var ErrProjectMismatch = errors.New("indexer: run_id does not match project")

// ErrSessionConflict signals that /index/begin was called for a project that
// already has an active session. HTTP handlers should map this to 409 Conflict.
var ErrSessionConflict = errors.New("indexer: session already active for project")

func (s *Service) requireSession(runID, projectPath string) (*session, error) {
	s.mu.RLock()
	sess, ok := s.sessions[runID]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNoSession
	}
	if sess.projectPath != projectPath {
		return nil, ErrProjectMismatch
	}
	return sess, nil
}

// ttlCleanup drops the session after sessionTTL if it is still active.
// Returns early without any DB work when Shutdown() is called.
func (s *Service) ttlCleanup(runID string) {
	t := time.NewTimer(sessionTTL)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.stopCh:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[runID]; ok && sess.status == "active" {
		s.logger.Warn("indexer: session timed out", "run_id", runID)
		delete(s.sessions, runID)
	}
}

// delayedCleanup removes a completed session from the in-memory map after
// cleanupDelay so a slow client can still fetch GetProgress for ~60s post-
// finish. Returns early without any DB work when Shutdown() is called.
func (s *Service) delayedCleanup(runID string) {
	t := time.NewTimer(cleanupDelay)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.stopCh:
		return
	}
	s.mu.Lock()
	delete(s.sessions, runID)
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (s *Service) collectLanguages(ctx context.Context, projectPath string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path FROM file_hashes WHERE project_path = ?`, projectPath,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	set := map[string]struct{}{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		if lang := langdetect.Detect(fp); lang != "" {
			set[lang] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out, nil
}

func sessStatusToHTTP(s string) string {
	if s == "active" {
		return "indexing"
	}
	return s
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// marshalJSONStringArray encodes a []string as a JSON array. Used to avoid a
// dependency on encoding/json just for this call site.
func marshalJSONStringArray(langs []string) string {
	if len(langs) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, l := range langs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range l {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

// Unused but kept for symmetry with Python: filepath.Base is used by callers.
var _ = filepath.Base
