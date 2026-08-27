package searchstats

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Kinds of search, recorded alongside every counter.
//
// They are separated rather than summed because they cost wildly different
// things and answer different questions: a semantic query embeds text and
// scans a vector collection, while a definition lookup is one indexed SELECT.
// A single "searches" number would average those together and mean nothing.
const (
	KindSemantic    = "semantic"
	KindSymbols     = "symbols"
	KindDefinitions = "definitions"
	KindReferences  = "references"
	KindFiles       = "files"
	// KindWorkspace is a per-project slice of a workspace fan-out: one
	// workspace query records one of these against EVERY project it actually
	// searched. That is what makes "how much load does this project carry"
	// true — a project pulled into a busy workspace is being searched
	// whether or not anybody names it directly.
	KindWorkspace = "workspace"
)

// KnownKinds is the set the API will accept as a filter, in display order.
var KnownKinds = []string{
	KindSemantic, KindWorkspace, KindSymbols,
	KindDefinitions, KindReferences, KindFiles,
}

// defaultFlushInterval is how long counters sit in memory before being
// written. It trades freshness of the dashboard, and how much is lost if the
// process dies, against how often the writer wakes up.
//
// Ten seconds because the numbers it feeds are cumulative counters read by a
// human looking at a table — nobody is watching for a single query to appear —
// and because it collapses a burst of searches into one transaction. A busy
// minute writes six times, not six hundred.
const defaultFlushInterval = 10 * time.Second

// maxPendingFiles caps how many distinct (bucket, project, kind, file) keys
// may be held between flushes.
//
// This is a memory bound, not a correctness one. The natural size of the map
// is one entry per file returned per search — tens per query — so the cap is
// unreachable in ordinary use. It exists for the pathological case: a script
// hammering search with limit=200 across many projects could otherwise grow
// the map without limit between two flushes, and analytics is never allowed
// to be the reason the server runs out of memory. Past the cap, FILE detail
// is dropped and counted; query counts keep accruing, because those are
// bounded by the number of distinct projects and are the more important half.
const maxPendingFiles = 100_000

// projectKey identifies one counter row. The bucket is part of the key so a
// flush that straddles a bucket boundary attributes each half correctly
// instead of dumping everything into whichever bucket the flush ran in.
//
// The same delta is applied to BOTH tiers: the bucket row for this bucket, and
// the cumulative total, which ignores the bucket entirely.
type projectKey struct {
	bucket int64
	path   string
	kind   string
}

type fileKey struct {
	bucket int64
	path   string
	kind   string
	file   string
}

type projectDelta struct {
	queries int64
	results int64
}

// Recorder accumulates counters in memory and flushes them in batches.
//
// Nothing here is on the search request's critical path beyond a mutex and a
// map write. That is the whole point: the handlers that call Record are the
// ones the server promises to keep serving while the main database is frozen
// for compaction, so recording must not be able to block on any database at
// all — not even this one.
//
// Every method is safe on a nil *Recorder, so call sites do not have to
// nil-check a feature that can be switched off.
type Recorder struct {
	store  *Store
	logger *slog.Logger
	now    func() time.Time

	interval time.Duration

	mu           sync.Mutex
	projects     map[projectKey]projectDelta
	files        map[fileKey]int64
	droppedFiles int64

	// started is the guard that makes Stop safe on a recorder whose flush loop
	// was never launched. main.go registers the deferred Stop the moment the
	// recorder is built, but Start happens hundreds of lines later, after
	// several checks that can abort the boot — the encryption-key mismatch in
	// particular exists to fail LOUDLY, and a Stop that blocked here would turn
	// that message into a silent hang.
	started   atomic.Bool
	startOnce sync.Once
	stop      chan struct{}
	stopped   chan struct{}
	stopOnce  sync.Once
}

// NewRecorder builds a recorder over the given store. It does not start the
// flush loop — call Start.
func NewRecorder(store *Store, logger *slog.Logger) *Recorder {
	if store == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{
		store:    store,
		logger:   logger,
		now:      time.Now,
		interval: defaultFlushInterval,
		projects: make(map[projectKey]projectDelta),
		files:    make(map[fileKey]int64),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Record notes one search: one query against projectPath, whose results named
// the given files.
//
// files is expected to be already DEDUPLICATED by the caller — one entry per
// file that appeared in the result, however many chunks of it matched. That
// convention is what keeps a file's hit count comparable to the project's
// query count: hits can never exceed queries, so a row reads as "this file came
// back in 42 of the project's 128 searches". Repeats are not rejected, they
// simply count twice and cost the reader that guarantee.
//
// Returns immediately. Never touches a database, never returns an error:
// a counter that could fail is a counter that would need error handling on the
// search path, and there is nothing a search handler could usefully do about
// it anyway.
func (r *Recorder) Record(projectPath, kind string, files []string) {
	if r == nil || projectPath == "" || kind == "" {
		return
	}
	bucket := BucketOf(r.now())

	r.mu.Lock()
	defer r.mu.Unlock()

	pk := projectKey{bucket: bucket, path: projectPath, kind: kind}
	d := r.projects[pk]
	d.queries++
	d.results += int64(len(files))
	r.projects[pk] = d

	for _, f := range files {
		if f == "" {
			continue
		}
		fk := fileKey{bucket: bucket, path: projectPath, kind: kind, file: f}
		if _, seen := r.files[fk]; !seen && len(r.files) >= maxPendingFiles {
			r.droppedFiles++
			continue
		}
		r.files[fk]++
	}
}

// Start runs the flush loop until ctx is cancelled or Stop is called.
//
// Idempotent. A second loop would close r.stopped a second time and panic, so
// the guard is not merely tidiness.
func (r *Recorder) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.started.Store(true)
		go r.loop(ctx)
	})
}

func (r *Recorder) loop(ctx context.Context) {
	defer close(r.stopped)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.finalFlush()
			return
		case <-r.stop:
			r.finalFlush()
			return
		case <-ticker.C:
			if err := r.Flush(ctx); err != nil {
				r.logger.Warn("search stats flush failed", "err", err)
			}
		}
	}
}

// finalFlush drains whatever is pending during shutdown.
//
// It takes a FRESH context with its own budget rather than the cancelled one
// that got us here: the counters are already in memory, the write is a single
// small transaction, and dropping them because the context that triggered the
// shutdown is done would lose data for no reason. The budget is short because
// shutdown has one of its own.
func (r *Recorder) finalFlush() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Flush(ctx); err != nil {
		r.logger.Warn("search stats final flush failed", "err", err)
	}
}

// Stop ends the flush loop and waits for the final flush. Idempotent, and safe
// on a recorder that was never started.
//
// The never-started case is not hypothetical: main.go registers this as a defer
// as soon as the store opens, and every boot failure between there and Start
// unwinds through it. Waiting on r.stopped — which only loop() ever closes —
// would hang the process instead of surfacing the error that caused the abort.
// So that case drains inline and returns, which also means counters recorded
// before an aborted boot are not silently dropped.
func (r *Recorder) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	if !r.started.Load() {
		r.finalFlush()
		return
	}
	<-r.stopped
}

// Flush writes everything pending in one transaction.
//
// The pending maps are swapped out under the lock and the write happens
// outside it, so a slow disk delays the next flush rather than every search
// running concurrently with it.
//
// On failure the batch is DROPPED, not retried. Retrying would mean holding a
// failed batch in memory while new counters accumulate behind it, which turns
// a transient disk error into unbounded growth; these are approximate usage
// numbers, and the honest response to "the disk is full" is to lose an
// interval of them and say so in the log.
func (r *Recorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	projectsPending, filesPending := r.projects, r.files
	dropped := r.droppedFiles
	r.projects = make(map[projectKey]projectDelta)
	r.files = make(map[fileKey]int64)
	r.droppedFiles = 0
	r.mu.Unlock()

	if dropped > 0 {
		r.logger.Warn("search stats dropped per-file detail — pending buffer full",
			"dropped", dropped, "cap", maxPendingFiles)
	}
	if len(projectsPending) == 0 && len(filesPending) == 0 {
		return nil
	}

	if err := r.write(ctx, projectsPending, filesPending); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) write(ctx context.Context,
	projectsPending map[projectKey]projectDelta, filesPending map[fileKey]int64) error {

	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("searchstats: begin flush: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Project ids are resolved inside the transaction and cached only for its
	// duration. A cache that outlived the transaction would have to be
	// invalidated by Forget and by Reset — the id-reuse hazard the schema
	// comment describes — and a flush resolves at most a handful of distinct
	// projects, so there is nothing to gain by keeping it.
	ids := make(map[string]int64, len(projectsPending))
	resolve := func(path string) (int64, error) {
		if id, ok := ids[path]; ok {
			return id, nil
		}
		id, err := internProject(ctx, tx, path)
		if err != nil {
			return 0, err
		}
		ids[path] = id
		return id, nil
	}

	now := r.now().Unix()

	upsertTotal, err := tx.PrepareContext(ctx, `
		INSERT INTO search_totals (project_id, kind, queries, results, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, kind) DO UPDATE SET
			queries   = queries + excluded.queries,
			results   = results + excluded.results,
			last_seen = MAX(last_seen, excluded.last_seen)`)
	if err != nil {
		return fmt.Errorf("searchstats: prepare totals upsert: %w", err)
	}
	defer upsertTotal.Close()

	upsertBucket, err := tx.PrepareContext(ctx, `
		INSERT INTO search_buckets (bucket, project_id, kind, queries, results)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(bucket, project_id, kind) DO UPDATE SET
			queries = queries + excluded.queries,
			results = results + excluded.results`)
	if err != nil {
		return fmt.Errorf("searchstats: prepare bucket upsert: %w", err)
	}
	defer upsertBucket.Close()

	for k, d := range projectsPending {
		id, err := resolve(k.path)
		if err != nil {
			return err
		}
		if _, err := upsertTotal.ExecContext(ctx, id, k.kind, d.queries, d.results, now); err != nil {
			return fmt.Errorf("searchstats: upsert totals: %w", err)
		}
		if _, err := upsertBucket.ExecContext(ctx, k.bucket, id, k.kind, d.queries, d.results); err != nil {
			return fmt.Errorf("searchstats: upsert bucket: %w", err)
		}
	}

	upsertFileTotal, err := tx.PrepareContext(ctx, `
		INSERT INTO search_file_totals (project_id, kind, file_path, hits, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, kind, file_path) DO UPDATE SET
			hits      = hits + excluded.hits,
			last_seen = MAX(last_seen, excluded.last_seen)`)
	if err != nil {
		return fmt.Errorf("searchstats: prepare file totals upsert: %w", err)
	}
	defer upsertFileTotal.Close()

	upsertFileBucket, err := tx.PrepareContext(ctx, `
		INSERT INTO search_file_buckets (bucket, project_id, kind, file_path, hits)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(bucket, project_id, kind, file_path) DO UPDATE SET
			hits = hits + excluded.hits`)
	if err != nil {
		return fmt.Errorf("searchstats: prepare file bucket upsert: %w", err)
	}
	defer upsertFileBucket.Close()

	for k, hits := range filesPending {
		id, err := resolve(k.path)
		if err != nil {
			return err
		}
		if _, err := upsertFileTotal.ExecContext(ctx, id, k.kind, k.file, hits, now); err != nil {
			return fmt.Errorf("searchstats: upsert file totals: %w", err)
		}
		if _, err := upsertFileBucket.ExecContext(ctx, k.bucket, id, k.kind, k.file, hits); err != nil {
			return fmt.Errorf("searchstats: upsert file bucket: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("searchstats: commit flush: %w", err)
	}
	return nil
}

// internProject maps a project path to its integer id, inserting it on first
// sight.
//
// The INSERT comes first and the SELECT is the fallback, rather than the other
// way round: after the very first flush for a project the row exists, so
// ordering it this way costs one no-op INSERT per project per flush instead of
// a SELECT plus, on the first flush, an INSERT. DO NOTHING makes the repeat
// case free of any error handling.
func internProject(ctx context.Context, tx *sql.Tx, path string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO projects_seen (project_path) VALUES (?) ON CONFLICT(project_path) DO NOTHING`,
		path); err != nil {
		return 0, fmt.Errorf("searchstats: intern project: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM projects_seen WHERE project_path = ?`, path).Scan(&id); err != nil {
		return 0, fmt.Errorf("searchstats: resolve project id: %w", err)
	}
	return id, nil
}
