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
	"github.com/dvcdsys/code-index/server/internal/chunksfts"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/langdetect"
	"github.com/dvcdsys/code-index/server/internal/symbolindex"
	"github.com/dvcdsys/code-index/server/internal/tokenizer"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// sessionTTL bounds how long an indexing session may sit IDLE (no
// ProcessFiles activity) before the housekeeping goroutine reaps it. It is a
// leak guard for abandoned sessions (client called /index/begin then crashed
// without /index/finish) — NOT a cap on total indexing time. ttlCleanup
// measures against the session's lastActivity, which every ProcessFiles batch
// bumps, so an actively-progressing index (including multi-hour in-process
// repo indexing) is never reaped.
//
// 10 minutes: comfortably exceeds the worst-case gap between two files
// finishing (a single huge file parse + slow remote embeddings), so a healthy
// index never trips it, while still reclaiming a genuinely abandoned session
// reasonably quickly.
const sessionTTL = 10 * time.Minute

// cleanupDelay mirrors Python's 60s post-finish cleanup window.
const cleanupDelay = 60 * time.Second

// wipeBatchSize bounds one full-reindex DELETE batch on symbols/refs. Sized so
// each implicit transaction stays in the low-milliseconds (these are plain
// b-tree deletes, much cheaper than FTS), keeping the SQLite writer available
// for concurrent transactions during a big project wipe.
const wipeBatchSize = 20000

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
	Status          string
	Phase           string
	FilesDiscovered int
	FilesProcessed  int
	FilesTotal      int
	ChunksCreated   int
	ElapsedSeconds  float64
	RunID           string
	RecentFiles     []string // most recent files processed, newest first; up to recentFilesCap
}

// recentFilesCap bounds the per-session ring of recently-processed file paths
// surfaced via GetProgress / GET /index/status so a UI can show forward motion.
const recentFilesCap = 3

// Session is the in-memory state of an active indexing run.
type session struct {
	runID           string
	projectPath     string
	filesDiscovered int // last CLI-reported total from /index/finish or batch payloads
	filesProcessed  int
	chunksCreated   int
	languagesSeen   map[string]struct{}
	startTime       time.Time
	lastActivity    time.Time // bumped each ProcessFiles file; drives idle-based reaping
	status          string    // active|completed
	phase           string    // receiving|completed
	recentFiles     []string  // ring of last recentFilesCap processed paths, oldest first
	full            bool      // this run wiped the index (full rebuild); drives full_sync_required clear on finish
}

// goneEntry is a tombstone for a removed session: why it went away and when.
type goneEntry struct {
	reason string // "user-cancel" | "idle-timeout"
	at     time.Time
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

// TokenBudgetSource is the capability of telling the chunker what a chunk
// costs in the active model's tokens. Named rather than asserted inline so a
// rename of TokenBudget is a compile error somewhere instead of a silent
// return to byte-sized chunking everywhere.
//
// *embeddings.Service satisfies it; test fakes generally do not, and get the
// byte path.
type TokenBudgetSource interface {
	TokenBudget() tokenizer.Budget
}

// Service owns sessions and wires dependencies for the three-phase protocol.
type Service struct {
	db     *sql.DB
	vs     vectorstore.Interface
	emb    Embedder
	logger *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*session // runID → state

	// gone is a tombstone map keyed by projectPath recording why a session
	// disappeared (user cancel vs idle reap). Lets a caller that hits
	// ErrNoSession mid-run tell a deliberate force-stop from an involuntary
	// loss. Consumed on read, pruned by age.
	gone map[string]goneEntry

	// stopCh is closed when Shutdown is called. Housekeeping goroutines
	// (ttlCleanup, delayedCleanup) select on it so they unblock promptly
	// instead of leaking for up to sessionTTL on server shutdown.
	stopCh   chan struct{}
	stopOnce sync.Once

	// embedIncludePath, when true, makes ProcessFiles wrap each chunk with
	// a "File: <relpath>\nLanguage: <lang>\n..." preamble before embedding.
	// Set via SetEmbedIncludePath; default false preserves Python-parity
	// "<chunk_type>: <content>" formatting for projects that have not been
	// reindexed under the new format.
	embedIncludePath bool

	// maxChunkTokens is the per-chunk token target (CIX_MAX_CHUNK_TOKENS).
	// 0 means the chunker's own default.
	maxChunkTokens int

	// embeddingModel is the active embedding model identifier persisted on
	// projects.indexed_with_model at FinishIndexing. Set via
	// SetEmbeddingModel from main; empty string keeps the column NULL so
	// unit tests that skip the setter don't need to know about drift.
	embeddingModel string

	// embeddingModelLookup, when non-nil, takes precedence over the static
	// embeddingModel string above. Used by main.go to bind the indexer
	// to a live function (embeddings.Service.EmbeddingModel) so a provider
	// switch made at runtime is reflected in the next FinishIndexing write
	// without requiring a process restart. Tests typically use the static
	// SetEmbeddingModel API and leave this nil.
	embeddingModelLookup func() string

	// embedConcurrency caps how many embed calls ProcessFiles issues in
	// parallel within one batch. The embeddings.Service queue independently
	// throttles real provider calls to MaxEmbeddingConcurrency, so sizing
	// this to the same value avoids spawning goroutines that only block on
	// the queue. <=1 → sequential (legacy behaviour). Set via
	// SetEmbedConcurrency from main, fed by runtimecfg.
	embedConcurrency int

	// embedBatchChunks packs chunks from consecutive files into a single
	// embed call (cross-file batching) up to this many chunks, cutting the
	// number of round-trips on repos full of small files. <=0 → one embed
	// call per file (no cross-file batching). Set via SetEmbedBatchChunks.
	embedBatchChunks int

	// embedTuningLookup, when set, takes precedence over the static
	// embedConcurrency / embedBatchChunks fields so a dashboard runtime-config
	// change takes effect on the next ProcessFiles batch without a restart.
	// main binds it to runtimecfg; tests use the static setters and leave it
	// nil. Returns (concurrency, batchChunks).
	embedTuningLookup func() (int, int)
}

// New constructs a Service. All deps are required except logger (falls back to
// slog.Default).
func New(db *sql.DB, vs vectorstore.Interface, emb Embedder, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db:       db,
		vs:       vs,
		emb:      emb,
		logger:   logger,
		sessions: make(map[string]*session),
		gone:     make(map[string]goneEntry),
		stopCh:   make(chan struct{}),
	}
}

// Shutdown signals all housekeeping goroutines to exit. Safe to call multiple
// times. Callers should invoke this before closing the DB.
func (s *Service) Shutdown() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// SetEmbedIncludePath toggles the path+language+symbol preamble that
// ProcessFiles prepends to chunk content before embedding. Toggling between
// runs requires a full reindex — vectors trained against the new preamble
// are not interchangeable with vectors trained on bare content.
func (s *Service) SetEmbedIncludePath(v bool) {
	s.embedIncludePath = v
}

// SetMaxChunkTokens sets the per-chunk token target used when the active
// embedding provider can count tokens exactly.
func (s *Service) SetMaxChunkTokens(n int) {
	s.maxChunkTokens = n
}

// SetEmbeddingModel records the model identifier the indexer will write to
// projects.indexed_with_model at FinishIndexing. Called from main once the
// runtime config is resolved; empty string disables the write (the column
// stays NULL — desired for tests that don't care about drift tracking).
//
// In production this is superseded by SetEmbeddingModelLookup, which binds
// the indexer to a live function so provider switches at runtime take
// effect without a process restart. The static setter remains for tests.
func (s *Service) SetEmbeddingModel(model string) {
	s.embeddingModel = model
}

// SetEmbeddingModelLookup binds the indexer to a live function returning
// the current Provider.ID() — typically embeddings.Service.EmbeddingModel.
// When set, this takes precedence over SetEmbeddingModel so a runtime
// provider switch (admin PUT /admin/embedding-providers/active) flows into
// the next FinishIndexing write without a process restart.
func (s *Service) SetEmbeddingModelLookup(fn func() string) {
	s.embeddingModelLookup = fn
}

// EmbeddingModel returns the current embedding-model fingerprint. Prefers
// the live lookup when one is bound (production); falls back to the static
// string set via SetEmbeddingModel (tests). Used by callers (repojobs) that
// need to compare the live model against projects.indexed_with_model to
// decide whether an incremental reindex is safe (same model = vectors
// comparable) or whether a full reindex is required (model change =
// embedding-space drift, all vectors must be regenerated).
func (s *Service) EmbeddingModel() string {
	if s.embeddingModelLookup != nil {
		return s.embeddingModelLookup()
	}
	return s.embeddingModel
}

// SetEmbedConcurrency sets how many embed calls ProcessFiles issues in
// parallel within one batch. Fed from runtimecfg (mirrors
// MaxEmbeddingConcurrency). <=1 keeps the legacy sequential behaviour.
func (s *Service) SetEmbedConcurrency(n int) { s.embedConcurrency = n }

// SetEmbedBatchChunks sets the cross-file embed-batch size (max chunks
// packed into a single embed call). Fed from runtimecfg. <=0 disables
// cross-file batching (one embed call per file).
func (s *Service) SetEmbedBatchChunks(n int) { s.embedBatchChunks = n }

// SetEmbedTuningLookup binds the indexer to a live function returning the
// current (embedConcurrency, embedBatchChunks). When set it overrides the
// static setters, so a dashboard runtime-config change is picked up on the
// next ProcessFiles batch without a process restart.
func (s *Service) SetEmbedTuningLookup(fn func() (int, int)) { s.embedTuningLookup = fn }

// embedTuning resolves the effective (concurrency, batchChunks): the live
// lookup when bound, else the static fields. Concurrency is floored at 1.
func (s *Service) embedTuning() (concurrency, batchChunks int) {
	if s.embedTuningLookup != nil {
		concurrency, batchChunks = s.embedTuningLookup()
	} else {
		concurrency, batchChunks = s.embedConcurrency, s.embedBatchChunks
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return concurrency, batchChunks
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
		lastActivity:  time.Now(),
		status:        "active",
		phase:         "receiving",
		full:          full,
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
		// M1 — run the DB wipe first; DeleteCollection is irreversible and
		// must run last so a DB failure does not leave file_hashes pointing at
		// already-deleted vectors (would skip re-indexing on next incremental).
		//
		// The wipe is BATCHED, not one transaction. SQLite has a single writer,
		// and a monolithic wipe of a big project (vscode: ~445k refs + tens of
		// thousands of trigram-FTS rows) held the write lock for minutes —
		// starving every concurrent writer past busy_timeout (prod symptom:
		// jobs-worker `claim failed: SQLITE_BUSY` on every poll tick until the
		// wipe committed). Batches release the writer between transactions.
		//
		// Crash-safety without whole-wipe atomicity: file_hashes goes FIRST in
		// its own statement. Once it's gone every file looks dirty, so a crash
		// midway just means the restarted full run re-deletes the survivors
		// (per-file DeleteByFileTx during reindex, or the next wipe attempt).
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM file_hashes WHERE project_path = ?`, projectPath,
		); err != nil {
			return "", nil, fmt.Errorf("full wipe file_hashes: %w", err)
		}
		for _, table := range []string{"symbols", "refs"} {
			// The rowid subselect rides the (project_path, …) index; each
			// DELETE statement is its own implicit transaction.
			q := fmt.Sprintf(
				`DELETE FROM %s WHERE rowid IN
				   (SELECT rowid FROM %s WHERE project_path = ? LIMIT %d)`,
				table, table, wipeBatchSize)
			for {
				res, err := s.db.ExecContext(ctx, q, projectPath)
				if err != nil {
					return "", nil, fmt.Errorf("full wipe %s: %w", table, err)
				}
				if n, _ := res.RowsAffected(); n < wipeBatchSize {
					break
				}
			}
		}
		if err := chunksfts.DeleteByProject(ctx, s.db, projectPath); err != nil {
			return "", nil, fmt.Errorf("full wipe chunks_fts: %w", err)
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

// preparedFile is one file's fully-chunked state, carried between the
// prepare → embed → write stages of ProcessFilesStreaming.
type preparedFile struct {
	fp       FilePayload
	language string
	texts    []string            // chunk texts to embed (len == len(vsChunks))
	vsChunks []vectorstore.Chunk // chunk payloads for the vector store + FTS
	symbols  []symbolindex.Symbol
	refs     []symbolindex.Reference
	embs     [][]float32 // filled by the embed stage; nil until then
	embedErr error       // non-fatal embed failure → file skipped in write stage
	embedMS  int64
}

// embedGroup is a set of consecutive prepared files whose chunks are embedded
// in a single provider call (cross-file batching).
type embedGroup struct {
	fileIdx []int // indices into the prepared slice
	nchunks int   // sum of len(texts) across fileIdx
}

// planEmbedGroups packs consecutive prepared files into embed groups of at
// most maxChunks chunks each. maxChunks<=0 → one group per file (no cross-file
// batching). A single file whose chunk count already exceeds maxChunks forms
// its own group (the provider splits it internally).
func planEmbedGroups(prep []*preparedFile, maxChunks int) []embedGroup {
	var groups []embedGroup
	if maxChunks <= 0 {
		for i := range prep {
			groups = append(groups, embedGroup{fileIdx: []int{i}, nchunks: len(prep[i].texts)})
		}
		return groups
	}
	cur := embedGroup{}
	for i := range prep {
		n := len(prep[i].texts)
		if len(cur.fileIdx) > 0 && cur.nchunks+n > maxChunks {
			groups = append(groups, cur)
			cur = embedGroup{}
		}
		cur.fileIdx = append(cur.fileIdx, i)
		cur.nchunks += n
	}
	if len(cur.fileIdx) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// isFatalEmbedErr reports whether an embed error must abort the whole batch
// (vs. skipping just the affected file). Mirrors the original sequential
// loop's fatal set: queue-busy (→ 503 + Retry-After), provider disabled,
// supervisor down, or not-yet-ready.
func isFatalEmbedErr(err error) bool {
	if _, busy := embeddings.IsBusy(err); busy {
		return true
	}
	return errors.Is(err, embeddings.ErrDisabled) ||
		errors.Is(err, embeddings.ErrSupervisor) ||
		errors.Is(err, embeddings.ErrNotReady)
}

// embedPrepared runs the embed stage: it embeds every prepared file's chunks,
// grouping chunks across files (planEmbedGroups) and running groups
// concurrently up to effEmbedConcurrency(). On success each file's embs is
// populated; a non-fatal error marks that group's files (embedErr) so the
// write stage skips them; the first fatal error is returned so the caller
// aborts the whole batch. sess.lastActivity is bumped as each group finishes
// so a long embed phase never trips the idle reaper.
//
// Note: cross-file batching couples the fate of a group on a NON-fatal error
// — one file's failed embed marks every file in its group (embedErr), so all
// are skipped this pass rather than just the offender. This is acceptable
// because skipped files don't get their file_hashes updated, so the next
// reconcile pass retries them; the trade is that a persistently-failing file
// can repeatedly poison its (deterministically-grouped) neighbours. Rare in
// practice — the fatal set already covers the common transient causes
// (queue-busy, provider down) and size-driven failures are handled inside the
// provider (e.g. Voyage adaptive split).
func (s *Service) embedPrepared(ctx context.Context, sess *session, prep []*preparedFile) error {
	concurrency, batchChunks := s.embedTuning()
	groups := planEmbedGroups(prep, batchChunks)
	if len(groups) == 0 {
		return nil
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var fatal error

	for _, g := range groups {
		wg.Add(1)
		go func(g embedGroup) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return
			}
			defer func() { <-sem }()
			if gctx.Err() != nil {
				return
			}
			texts := make([]string, 0, g.nchunks)
			for _, fi := range g.fileIdx {
				texts = append(texts, prep[fi].texts...)
			}
			start := time.Now()
			var (
				embs [][]float32
				err  error
			)
			if tae, ok := s.emb.(TokenAwareEmbedder); ok {
				embs, err = tae.TokenizeAndEmbed(gctx, texts)
			} else {
				embs, err = s.emb.EmbedTexts(gctx, texts)
			}
			s.mu.Lock()
			sess.lastActivity = time.Now()
			s.mu.Unlock()
			if err != nil {
				if isFatalEmbedErr(err) {
					mu.Lock()
					if fatal == nil {
						fatal = err
					}
					mu.Unlock()
					cancel()
					return
				}
				for _, fi := range g.fileIdx {
					prep[fi].embedErr = err
				}
				return
			}
			if len(embs) != g.nchunks {
				e := fmt.Errorf("embed returned %d vectors, want %d", len(embs), g.nchunks)
				for _, fi := range g.fileIdx {
					prep[fi].embedErr = e
				}
				return
			}
			ms := time.Since(start).Milliseconds()
			off := 0
			for _, fi := range g.fileIdx {
				n := len(prep[fi].texts)
				prep[fi].embs = embs[off : off+n]
				prep[fi].embedMS = ms
				off += n
			}
		}(g)
	}
	wg.Wait()
	return fatal
}

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
	return s.ProcessFilesStreaming(ctx, projectPath, runID, files, nil)
}

// ProcessFilesStreaming is ProcessFiles with an optional progress channel. The
// streaming HTTP handler passes a channel that forwards each event as an
// NDJSON line; non-streaming callers use ProcessFiles which passes nil.
//
// The terminal event (batch_done on success, error fatal=true on failure) is
// sent with a guaranteed-blocking send so the consumer always sees it.
// Per-file progress events use a non-blocking send and may be dropped if the
// consumer is slower than the embed loop — that is acceptable because the
// final summary is what callers depend on.
//
// When progress is non-nil, the channel is left open on return; the caller
// is expected to close it after collecting the terminal event.
func (s *Service) ProcessFilesStreaming(
	ctx context.Context,
	projectPath, runID string,
	files []FilePayload,
	progress chan<- ProgressEvent,
) (int, int, int, error) {
	sess, err := s.requireSession(runID, projectPath)
	if err != nil {
		emitTerminal(progress, ProgressEvent{
			Event:   EventError,
			Message: err.Error(),
			Fatal:   true,
			RunID:   runID,
		})
		return 0, 0, 0, err
	}

	s.logger.Info("indexer: processing batch", "run_id", runID, "files", len(files))

	now := nowUTC()
	filesAccepted := 0
	batchChunks := 0

	// maxContentBytes guards against files that grew past the CLI's MaxFileSize
	// filter between discovery and indexing (e.g. a log file written in-flight).
	// 512 KB matches the CLI default; above this the tokenise loop would hold
	// the queue slot for tens of seconds per file.
	const maxContentBytes = 512 * 1024

	// Per-file transactions (not per-batch). Earlier revisions wrapped the
	// whole loop in a single BeginTx and used SAVEPOINTs per file, which held
	// SQLite's WAL writer lock across every embed call (a network RTT to
	// llama-server per file). On a multi-minute batch any concurrent write —
	// most visibly POST /projects from the dashboard add-repo flow — timed
	// out against busy_timeout=5s with `database is locked (5) (SQLITE_BUSY)`.
	// Per-file tx caps lock-holding to the actual DB writes (sub-ms) and
	// releases the writer between files so other connections can interleave.
	// Side benefit: a fatal mid-batch error (embed ErrBusy, etc.) no longer
	// rolls back all of this batch's work — successfully-indexed files stay
	// committed and the next batch resumes from where this one stopped.

	// ---- Stage 1: PREPARE (sequential, local) ----------------------------
	// Chunk every file and build its symbols/refs/texts/vector payloads. This
	// is CPU-local and cheap, so it stays sequential to keep progress-event
	// order; the expensive embed work is parallelised in stage 2.
	prep := make([]*preparedFile, 0, len(files))
	budgetSrc, _ := s.emb.(TokenBudgetSource)
	for fi, fp := range files {
		// file_started — emit even for files we'll skip below, so the client
		// counter advances monotonically and rendering stays aligned with N.
		progressSend(progress, ProgressEvent{
			Event:     EventFileStarted,
			Path:      fp.Path,
			FileIndex: fi + 1,
			BatchSize: len(files),
			RunID:     runID,
		})

		// Record the current file in the session ring so GET /index/status
		// can surface live forward motion, and bump idle activity.
		s.mu.Lock()
		sess.lastActivity = time.Now()
		sess.recentFiles = append(sess.recentFiles, fp.Path)
		if len(sess.recentFiles) > recentFilesCap {
			sess.recentFiles = sess.recentFiles[len(sess.recentFiles)-recentFilesCap:]
		}
		s.mu.Unlock()

		if strings.TrimSpace(fp.Content) == "" {
			continue
		}
		if len(fp.Content) > maxContentBytes {
			s.logger.Warn("indexer: file too large, skipping", "path", fp.Path, "size_bytes", len(fp.Content))
			progressSend(progress, ProgressEvent{
				Event:   EventFileError,
				Path:    fp.Path,
				Message: fmt.Sprintf("file too large (%d bytes)", len(fp.Content)),
				Fatal:   false,
			})
			continue
		}

		language := fp.Language
		if language == "" {
			language = "text"
		}

		// The budget is re-read per file: a provider swap between files is
		// legitimate, mixing two models' limits inside one file's chunks is
		// not. The type assertion itself is hoisted out of the loop.
		var budget tokenizer.Budget
		if budgetSrc != nil {
			budget = budgetSrc.TokenBudget()
		}
		chunks, refs, err := chunker.ChunkFileTokens(fp.Path, fp.Content, language, 0, budget, s.maxChunkTokens)
		if err != nil {
			s.logger.Warn("indexer: chunk file failed", "path", fp.Path, "err", err)
			progressSend(progress, ProgressEvent{
				Event:   EventFileError,
				Path:    fp.Path,
				Message: "chunk: " + err.Error(),
				Fatal:   false,
			})
			continue
		}
		if len(chunks) == 0 {
			continue
		}
		progressSend(progress, ProgressEvent{
			Event:  EventFileChunked,
			Path:   fp.Path,
			Chunks: len(chunks),
		})

		// Relative path for the path-aware embedding preamble — computed once
		// per file and reused for all its chunks.
		relPath := fp.Path
		if s.embedIncludePath {
			if rp, rerr := filepath.Rel(projectPath, fp.Path); rerr == nil {
				relPath = rp
			}
		}

		// Build embed texts + vector-store payloads in a single pass over the
		// chunks. Format depends on embedIncludePath: legacy Python-parity
		// "{chunk_type}: {content}" when false, or path+language+symbol
		// preamble + content when true (see embeddings.FormatChunkForEmbedding).
		texts := make([]string, len(chunks))
		vsChunks := make([]vectorstore.Chunk, len(chunks))
		for i, c := range chunks {
			texts[i] = embeddings.FormatChunkForEmbedding(c, relPath, s.embedIncludePath)
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

		prep = append(prep, &preparedFile{
			fp:       fp,
			language: language,
			texts:    texts,
			vsChunks: vsChunks,
			symbols:  fileSymbols,
			refs:     fileRefs,
		})
	}

	// ---- Stage 2: EMBED (parallel + cross-file batched) ------------------
	// embedPrepared fills each prepared file's embs, or returns a fatal error
	// (queue-busy → 503, provider disabled/down) that aborts the whole batch.
	// Non-fatal per-group failures are recorded on the file and skipped below.
	if ferr := s.embedPrepared(ctx, sess, prep); ferr != nil {
		emitTerminal(progress, ProgressEvent{
			Event:   EventError,
			Message: ferr.Error(),
			Fatal:   true,
		})
		s.mu.RLock()
		total := sess.filesProcessed
		s.mu.RUnlock()
		return 0, 0, total, ferr
	}

	// ---- Stage 3: WRITE (serial, ordered) --------------------------------
	// Vector-store + per-file DB writes run on this single goroutine: the
	// store is thread-safe, but serialising keeps SQLite's WAL writer
	// uncontended and
	// preserves deterministic progress-event ordering. Each write is local and
	// sub-ms, so serialising costs nothing next to the (now parallel) embeds.
	for _, p := range prep {
		if p.embedErr != nil {
			s.logger.Error("indexer: embed texts failed", "path", p.fp.Path, "err", p.embedErr)
			progressSend(progress, ProgressEvent{
				Event:   EventFileError,
				Path:    p.fp.Path,
				Message: "embed: " + p.embedErr.Error(),
				Fatal:   false,
			})
			continue
		}
		progressSend(progress, ProgressEvent{
			Event:   EventFileEmbedded,
			Path:    p.fp.Path,
			Chunks:  len(p.vsChunks),
			EmbedMS: p.embedMS,
		})

		// Vector store has no transactions — do its writes BEFORE opening the
		// DB tx so the writer lock is held strictly for the DB part. If the DB
		// tx fails we leave the new vectors in place; the next reindex sees
		// file_hashes was not updated and re-processes the file, overwriting
		// them. Acceptable for an infrequent failure mode.
		if s.vs != nil {
			if err := s.vs.DeleteByFile(ctx, projectPath, p.fp.Path); err != nil {
				s.logger.Error("indexer: vectorstore delete by file", "path", p.fp.Path, "err", err)
				progressSend(progress, ProgressEvent{
					Event:   EventFileError,
					Path:    p.fp.Path,
					Message: "vectorstore delete: " + err.Error(),
					Fatal:   false,
				})
				continue
			}
			if err := s.vs.UpsertChunks(ctx, projectPath, p.vsChunks, p.embs); err != nil {
				s.logger.Error("indexer: vectorstore upsert", "path", p.fp.Path, "err", err)
				progressSend(progress, ProgressEvent{
					Event:   EventFileError,
					Path:    p.fp.Path,
					Message: "vectorstore upsert: " + err.Error(),
					Fatal:   false,
				})
				continue
			}
		}

		// Build chunksfts payload from the same chunks pushed to the vector store.
		ftsChunks := make([]chunksfts.Chunk, len(p.vsChunks))
		for i, c := range p.vsChunks {
			ftsChunks[i] = chunksfts.Chunk{
				Content:    c.Content,
				FilePath:   c.FilePath,
				StartLine:  c.StartLine,
				EndLine:    c.EndLine,
				ChunkType:  c.ChunkType,
				SymbolName: c.SymbolName,
				Language:   c.Language,
			}
		}

		// Per-file DB tx: delete-old + insert-new symbols/refs + chunks_fts
		// + file_hashes commit atomically. Anonymous func so the deferred
		// rollback fires per file rather than at function return.
		fileErr := func() error {
			ftx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin file tx: %w", err)
			}
			defer ftx.Rollback() //nolint:errcheck // no-op after commit

			if err := symbolindex.DeleteByFileTx(ctx, ftx, projectPath, p.fp.Path); err != nil {
				return fmt.Errorf("symbols delete: %w", err)
			}
			if err := symbolindex.DeleteRefsByFileTx(ctx, ftx, projectPath, p.fp.Path); err != nil {
				return fmt.Errorf("refs delete: %w", err)
			}
			if len(p.symbols) > 0 {
				if err := symbolindex.UpsertSymbolsTx(ctx, ftx, projectPath, p.symbols); err != nil {
					return fmt.Errorf("upsert symbols: %w", err)
				}
			}
			if len(p.refs) > 0 {
				if err := symbolindex.UpsertReferencesTx(ctx, ftx, projectPath, p.refs); err != nil {
					return fmt.Errorf("upsert refs: %w", err)
				}
			}
			if err := chunksfts.UpsertByFileTx(ctx, ftx, projectPath, p.fp.Path, ftsChunks); err != nil {
				return fmt.Errorf("upsert chunks_fts: %w", err)
			}
			if _, err := ftx.ExecContext(ctx,
				`INSERT OR REPLACE INTO file_hashes
				 (project_path, file_path, content_hash, indexed_at)
				 VALUES (?, ?, ?, ?)`,
				projectPath, p.fp.Path, p.fp.ContentHash, now,
			); err != nil {
				return fmt.Errorf("file_hashes upsert: %w", err)
			}
			return ftx.Commit()
		}()
		if fileErr != nil {
			s.logger.Error("indexer: file tx failed", "path", p.fp.Path, "err", fileErr)
			progressSend(progress, ProgressEvent{
				Event:   EventFileError,
				Path:    p.fp.Path,
				Message: fileErr.Error(),
				Fatal:   false,
			})
			continue
		}

		batchChunks += len(p.vsChunks)

		s.mu.Lock()
		sess.languagesSeen[p.language] = struct{}{}
		s.mu.Unlock()
		filesAccepted++

		progressSend(progress, ProgressEvent{
			Event:  EventFileDone,
			Path:   p.fp.Path,
			Chunks: len(p.vsChunks),
		})
	}

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

	emitTerminal(progress, ProgressEvent{
		Event:               EventBatchDone,
		FilesAccepted:       filesAccepted,
		ChunksCreated:       batchChunks,
		FilesProcessedTotal: total,
	})

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
		if err := deleteChunksFTSByFile(ctx, s.db, projectPath, dp); err != nil {
			s.logger.Warn("indexer: chunks_fts delete by file (finish)", "path", dp, "err", err)
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

	// PR-E — capture the active embedding model so the dashboard can flag
	// projects whose vectors were produced under a different model than the
	// one currently loaded in the sidecar. NULLIF keeps the column NULL when
	// SetEmbeddingModel was never called (tests / pre-PR-E codepaths).
	// Reads through EmbeddingModel() so live provider switches (set via
	// SetEmbeddingModelLookup) are honoured at write time — the value goes
	// into the row in its prefixed form ("ollama:<model>" / "voyage:..."),
	// matching the format the drift-detector and dashboard compare against.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE projects
		 SET stats = ?, languages = ?, status = 'indexed',
		     last_indexed_at = ?, updated_at = ?,
		     indexed_with_model = NULLIF(?, '')
		 WHERE host_path = ?`,
		statsJSON, langsJSON, now, now, s.EmbeddingModel(), projectPath,
	); err != nil {
		return "", 0, 0, fmt.Errorf("update project stats: %w", err)
	}

	// Clear the "full sync required" flag iff this run was a full rebuild. The
	// flag is informational (it drives the dashboard "out of sync" badge, set by
	// migration 18 / format changes) and is satisfied only by a completed full
	// run — incremental/reconcile runs leave it set. We only reach here on
	// success, so a full run that crashed mid-way keeps the flag (crash-safe).
	// sess.full is set once at BeginIndexing and never mutated, so no lock.
	if sess.full {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE projects SET full_sync_required = 0, full_sync_reason = NULL WHERE host_path = ?`,
			projectPath,
		); err != nil {
			return "", 0, 0, fmt.Errorf("clear full_sync_required: %w", err)
		}
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
	s.gone[projectPath] = goneEntry{reason: "user-cancel", at: time.Now()}
	s.pruneGoneLocked()
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

// FailIndexing releases the in-memory session for runID after the in-process
// repo indexer (package repoindexer) aborts mid-run — e.g. a transient
// "embedding queue saturated" backpressure error or a walk failure. Without
// this, an aborted run leaves its session status="active" until ttlCleanup
// reaps it (sessionTTL of idle), and every immediate retry / manual Sync
// bounces off ErrSessionConflict in the meantime — the failure that triggers
// the retry also blocks it. Releasing the session here lets the retry call
// BeginIndexing again and resume via the reconcile path.
//
// Unlike CancelIndexing this is NOT a user cancellation: it does NOT flip
// projects.status to 'indexed' (repojobs owns the project's terminal state and
// will mark it 'error' / requeue) and it sets no "user-cancel" tombstone, so a
// later ErrNoSession is correctly read as an involuntary loss (retry/resume)
// rather than a deliberate stop. Idempotent and keyed by runID, so it no-ops
// when the session was already removed (force-stop, idle reap, or success).
func (s *Service) FailIndexing(ctx context.Context, projectPath, runID string) {
	s.mu.Lock()
	sess, ok := s.sessions[runID]
	if !ok || sess.projectPath != projectPath {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, runID)
	s.mu.Unlock()

	// Detach from ctx for the bookkeeping write: the abort path is often a
	// cancelled context (shutdown), but the run row should still be marked
	// failed rather than left 'running'. The session release above — the part
	// that unblocks retries — already happened and never touched ctx.
	now := nowUTC()
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`UPDATE index_runs SET status = 'failed', completed_at = ? WHERE id = ?`,
		now, runID,
	); err != nil {
		s.logger.Warn("indexer: mark run failed", "run_id", runID, "project", projectPath, "err", err)
	}
	s.logger.Info("indexer: session released after failure", "run_id", runID, "project", projectPath)
}

// ---------------------------------------------------------------------------
// Status + session helpers
// ---------------------------------------------------------------------------

// ActiveSessions counts index runs currently in flight.
//
// The three-phase protocol (begin → files → finish) lives here and nowhere
// else: a CLI push creates no row in the jobs table, so anything that asks
// "is this server busy indexing?" by counting jobs sees an idle server in the
// middle of a run. Database compaction asks exactly that before taking the
// server read-only, which is why this exists.
func (s *Service) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.status == "active" {
			n++
		}
	}
	return n
}

// GetProgress returns the active session progress for a project, or nil if no
// active session. Mirrors Python get_progress.
func (s *Service) GetProgress(projectPath string) *Progress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.projectPath == projectPath {
			// Copy the ring newest-first so the UI shows the current file on top.
			recent := make([]string, 0, len(sess.recentFiles))
			for i := len(sess.recentFiles) - 1; i >= 0; i-- {
				recent = append(recent, sess.recentFiles[i])
			}
			return &Progress{
				RunID:           sess.runID,
				Status:          sessStatusToHTTP(sess.status),
				Phase:           sess.phase,
				FilesDiscovered: sess.filesDiscovered,
				FilesProcessed:  sess.filesProcessed,
				FilesTotal:      sess.filesDiscovered, // CLI's reported total, best-known estimate mid-run
				ChunksCreated:   sess.chunksCreated,
				ElapsedSeconds:  time.Since(sess.startTime).Seconds(),
				RecentFiles:     recent,
			}
		}
	}
	return nil
}

// SetDiscoveredTotal publishes the known file total for a project's active
// session before the run finishes, so GET /index/status can report a real
// denominator mid-run. Only raises the value (floor semantics) and no-ops when
// no active session exists for the project. Used by the in-process repo indexer
// for incremental runs, where the change-set size is known up front.
func (s *Service) SetDiscoveredTotal(projectPath string, total int) {
	if total <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.projectPath == projectPath && sess.status == "active" {
			if total > sess.filesDiscovered {
				sess.filesDiscovered = total
			}
			return
		}
	}
}

// ErrNoSession signals that a request references an unknown run_id.
var ErrNoSession = errors.New("indexer: no active session for run_id")

// ErrProjectMismatch signals that the run_id belongs to a different project.
var ErrProjectMismatch = errors.New("indexer: run_id does not match project")

// ErrSessionConflict signals that /index/begin was called for a project that
// already has an active session. HTTP handlers should map this to 409 Conflict.
var ErrSessionConflict = errors.New("indexer: session already active for project")

// ConsumeGoneReason returns why a now-absent session for projectPath
// disappeared ("user-cancel" | "idle-timeout") and removes the tombstone.
// Returns "" when there is no record — which the caller should treat as an
// involuntary loss (process crash / never existed), i.e. NOT a deliberate
// force-stop. The in-process repo indexer uses this to decide whether an
// ErrNoSession mid-run is a clean cancellation (swallow) or an abort to
// surface so the queue retries and the resume path picks up where it stopped.
func (s *Service) ConsumeGoneReason(projectPath string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.gone[projectPath]
	if !ok {
		return ""
	}
	delete(s.gone, projectPath)
	return e.reason
}

// pruneGoneLocked drops tombstones older than sessionTTL. Caller holds s.mu.
func (s *Service) pruneGoneLocked() {
	if len(s.gone) == 0 {
		return
	}
	cutoff := time.Now().Add(-sessionTTL)
	for k, e := range s.gone {
		if e.at.Before(cutoff) {
			delete(s.gone, k)
		}
	}
}

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

// ttlCleanup reaps the session only after it has been IDLE for sessionTTL —
// i.e. no ProcessFiles batch bumped lastActivity within that window. This
// makes the timeout an inactivity guard against abandoned sessions, NOT a cap
// on total indexing time: an actively-progressing run (which bumps
// lastActivity every file) is never reaped, however long it takes. Exits
// early on Shutdown(), and once the session is gone or no longer active.
func (s *Service) ttlCleanup(runID string) {
	ticker := time.NewTicker(sessionTTL / 4)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		sess, ok := s.sessions[runID]
		if !ok {
			s.mu.Unlock()
			return // finished or cancelled — nothing to reap
		}
		if sess.status != "active" {
			s.mu.Unlock()
			return // delayedCleanup owns completed sessions
		}
		if time.Since(sess.lastActivity) > sessionTTL {
			s.logger.Warn("indexer: session idle-timed-out",
				"run_id", runID, "project", sess.projectPath,
				"idle_seconds", time.Since(sess.lastActivity).Seconds())
			delete(s.sessions, runID)
			s.gone[sess.projectPath] = goneEntry{reason: "idle-timeout", at: time.Now()}
			s.pruneGoneLocked()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
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

// deleteChunksFTSByFile is the standalone-db wrapper used by the
// FinishIndexing deletedPaths loop, which operates outside the per-file
// tx. Internally it opens a short tx so chunks_fts and chunks_meta
// stay consistent if one of the two DELETEs fails.
func deleteChunksFTSByFile(ctx context.Context, db *sql.DB, projectPath, filePath string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := chunksfts.DeleteByFileTx(ctx, tx, projectPath, filePath); err != nil {
		return err
	}
	return tx.Commit()
}
