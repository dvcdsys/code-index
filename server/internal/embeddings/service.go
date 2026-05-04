package embeddings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
)

// Service is the public embeddings API used by handlers. It composes the
// llama-server supervisor, the unix-socket client, the concurrency queue, and
// the per-model query-prefix policy. Handlers should call EmbedQuery for
// search inputs (applies prefix for asymmetric retrieval) and EmbedTexts for
// passages/chunks.
//
// A Service with Disabled == true is a legal no-op used in tests; every
// method returns ErrDisabled. main.go constructs it via New when
// cfg.EmbeddingsEnabled is false.
type Service struct {
	cfg    *config.Config
	logger *slog.Logger

	sup      *supervisor
	queue    *Queue
	prefix   string
	disabled bool
}

// New constructs a Service. If cfg.EmbeddingsEnabled is false it returns a
// disabled Service that reports ErrDisabled on every Embed* call but can
// still be Stop()-ed cleanly. Otherwise it resolves the GGUF path (env →
// cache → HF download), then starts the llama-server supervisor and blocks
// until the readiness probe succeeds.
//
// ctx governs startup only. It is NOT stored on the Service — Stop has its
// own context so shutdown can be bounded independently of startup.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.EmbeddingsEnabled {
		logger.Info("embeddings service disabled (CIX_EMBEDDINGS_ENABLED=false)")
		return &Service{cfg: cfg, logger: logger, disabled: true}, nil
	}

	ggufPath, err := resolveGGUFPath(ctx, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("resolve gguf: %w", err)
	}

	supCfg := supervisorConfig{
		BinDir:     cfg.LlamaBinDir,
		GGUFPath:   ggufPath,
		SocketPath: cfg.LlamaSocketPath,
		Transport:  cfg.LlamaTransport,
		CtxSize:    cfg.LlamaCtxSize,
		NGpuLayers: cfg.LlamaNGpuLayers,
		NThreads:   cfg.LlamaNThreads,
		BatchSize:  cfg.LlamaBatchSize,
		StartupSec: cfg.LlamaStartupSec,
		Model:      cfg.EmbeddingModel,
	}

	sup, err := newSupervisor(ctx, supCfg, logger)
	if err != nil {
		return nil, err
	}

	return &Service{
		cfg:    cfg,
		logger: logger,
		sup:    sup,
		queue:  NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second),
		prefix: ResolveQueryPrefix(cfg.EmbeddingModel),
	}, nil
}

// Config returns the *config.Config the service was constructed with. The
// pointer is shared; callers that mutate it in place must understand they
// are racing the supervisor — only the dashboard restart path is supposed
// to do this, and it does so behind queue.BlockNew + sup.Restart.
func (s *Service) Config() *config.Config {
	if s == nil {
		return nil
	}
	return s.cfg
}

// CacheDirFromService returns the GGUF cache directory the dashboard's
// /admin/models handler should walk. Returns "" when the EmbeddingsQuerier
// isn't a *Service (test fakes) or when the service is disabled.
func CacheDirFromService(q any) string {
	s, ok := q.(*Service)
	if !ok || s == nil || s.cfg == nil {
		return ""
	}
	return s.cfg.GGUFCacheDir
}

// Stop tears the supervisor down within the ctx deadline. Safe to call on a
// disabled or partially-initialised Service.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil || s.disabled || s.sup == nil {
		return nil
	}
	return s.sup.Stop(ctx)
}

// Status returns a snapshot of the sidecar process state for the dashboard.
// Returns SupervisorStatus{State: "disabled"} when the service was started
// with embeddings turned off — the dashboard renders a banner in that case
// and disables the runtime-config save buttons.
func (s *Service) Status() SupervisorStatus {
	if s == nil || s.disabled {
		return SupervisorStatus{State: "disabled"}
	}
	if s.sup == nil {
		return SupervisorStatus{State: "failed", LastError: "supervisor not initialised"}
	}
	st := s.sup.Status()
	if s.queue != nil {
		// Annotate with in-flight count so the UI can show "draining (N)"
		// during a restart cycle.
		st.InFlight = s.queue.InFlight()
	}
	return st
}

// Restart drains the embedding queue, stops the current sidecar child, and
// spawns a new one with the new config. cfg is the freshly-resolved
// runtimecfg-on-top-of-env Config snapshot — Restart does not consult any
// stored boot config.
//
// On success, the new sidecar is ready to serve embeddings before this
// returns. On failure, the supervisor enters the "failed" state and the
// queue is reopened (so callers get the existing ErrSupervisor / ErrBusy
// rather than a permanent block).
func (s *Service) Restart(ctx context.Context, cfg *config.Config) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}
	if s.sup == nil {
		return ErrSupervisor
	}

	// Drain: refuse new acquires, then wait for in-flight to settle. 30s
	// matches the documented restart UX in the dashboard plan; longer values
	// would let a stuck embedding call block the operator's intentional
	// restart indefinitely.
	s.queue.BlockNew()
	defer s.queue.Resume()
	drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := s.queue.WaitDrain(drainCtx); err != nil {
		drainCancel()
		s.logger.Warn("embeddings: drain timed out, proceeding with restart anyway",
			"in_flight", s.queue.InFlight(), "err", err,
		)
	} else {
		drainCancel()
	}

	// Resolve the (possibly new) GGUF path before tearing down the current
	// child — if resolution fails, we stay on the running sidecar instead of
	// crashing it for a config we can't honour.
	ggufPath, err := resolveGGUFPath(ctx, cfg, s.logger)
	if err != nil {
		return fmt.Errorf("resolve gguf for restart: %w", err)
	}

	// Update queue concurrency / prefix to match the new model. The buffered
	// slot channel can't be resized in place; we swap the queue, but only
	// AFTER drain so no caller is mid-Acquire/Release on the old channel.
	if cfg.MaxEmbeddingConcurrency != cap(s.queue.slots) {
		s.queue = NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second)
		// New queue starts unblocked; that's fine because we hold the
		// *previous* queue's blocked state via deferred Resume. The previous
		// queue is now garbage and won't see any callers.
	}
	s.prefix = ResolveQueryPrefix(cfg.EmbeddingModel)

	supCfg := supervisorConfig{
		BinDir:     cfg.LlamaBinDir,
		GGUFPath:   ggufPath,
		SocketPath: cfg.LlamaSocketPath,
		Transport:  cfg.LlamaTransport,
		CtxSize:    cfg.LlamaCtxSize,
		NGpuLayers: cfg.LlamaNGpuLayers,
		NThreads:   cfg.LlamaNThreads,
		BatchSize:  cfg.LlamaBatchSize,
		StartupSec: cfg.LlamaStartupSec,
		Model:      cfg.EmbeddingModel,
	}
	return s.sup.Restart(ctx, supCfg)
}

// Ready reports whether the embeddings pipeline is currently able to serve a
// request. Returns nil when the model is loaded and the supervisor is healthy,
// ErrDisabled when embeddings are turned off, or ErrSupervisor/ErrNotReady
// when the sidecar has died or is still warming up. m5 — /api/v1/status uses
// this to populate model_loaded rather than hard-coding `true`.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}
	if s.sup == nil {
		return ErrSupervisor
	}
	if s.sup.dead.Load() {
		return ErrSupervisor
	}
	return s.sup.Ready(ctx)
}

// EmbedQuery prepends the model's asymmetric-retrieval prefix and returns a
// single vector. Mirrors Python `embed_query`.
func (s *Service) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if s.disabled {
		return nil, ErrDisabled
	}
	text := s.prefix + query
	vecs, err := s.embedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// EmbedTexts embeds passages unchanged (no prefix). Mirrors Python
// `embed_texts`. Returned vectors follow input order.
func (s *Service) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if s.disabled {
		return nil, ErrDisabled
	}
	return s.embedBatch(ctx, texts)
}

// embedBatch is the shared path used by both EmbedQuery and EmbedTexts. It
// acquires a queue slot, waits for the supervisor to be ready, and issues the
// HTTP call. Prefix logic stays in the callers so the queue accounting is
// identical regardless of whether the caller was a query or a passage batch.
func (s *Service) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if s.sup.dead.Load() {
		return nil, ErrSupervisor
	}
	if len(texts) == 0 {
		return nil, nil
	}

	// Block on queue slot first — this is the backpressure surface that maps
	// to HTTP 503 + Retry-After.
	slotStart := time.Now()
	if err := s.queue.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.queue.Release(slotStart)

	// Make sure the child process finished its (re)start before issuing the
	// call. For a healthy steady-state Service this is a no-op.
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.sup.Ready(readyCtx)
	cancel()
	if err != nil {
		if errors.Is(err, ErrSupervisor) {
			return nil, ErrSupervisor
		}
		return nil, fmt.Errorf("wait ready: %w", err)
	}

	return s.sup.client.Embeddings(ctx, texts)
}

// TokenizeAndEmbed is the token-aware embedding pipeline. For each text it:
//  1. Calls /tokenize to get token IDs (CLS + content + SEP).
//  2. Splits sequences longer than cfg.LlamaCtxSize at token boundaries,
//     preserving CLS/SEP on each window.
//  3. Embeds all sequences in a single /v1/embeddings call using pre-tokenized
//     IDs — no re-tokenization happens inside the model server.
//  4. Averages sub-window vectors back to one vector per original text.
//
// The entire operation holds one queue slot so back-pressure accounting matches
// EmbedTexts. Returns ErrDisabled / ErrSupervisor / ErrBusy on the same
// conditions as EmbedTexts.
func (s *Service) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if s.disabled {
		return nil, ErrDisabled
	}
	if s.sup.dead.Load() {
		return nil, ErrSupervisor
	}
	if len(texts) == 0 {
		return nil, nil
	}

	slotStart := time.Now()
	if err := s.queue.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.queue.Release(slotStart)

	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.sup.Ready(readyCtx)
	cancel()
	if err != nil {
		if errors.Is(err, ErrSupervisor) {
			return nil, ErrSupervisor
		}
		return nil, fmt.Errorf("wait ready: %w", err)
	}

	maxTokens := s.cfg.LlamaCtxSize

	// Phase 1: tokenize each text. Accumulate flat sequences slice and a
	// span table that records which flat sequences belong to each text.
	type span struct{ start, length int }
	spans := make([]span, len(texts))
	var sequences [][]int

	for i, text := range texts {
		ids, err := s.sup.client.Tokenize(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("tokenize text[%d]: %w", i, err)
		}

		if len(ids) == 0 {
			// Empty result: placeholder — embed will return a zero vector.
			spans[i] = span{start: len(sequences), length: 1}
			sequences = append(sequences, []int{})
			continue
		}

		if len(ids) <= maxTokens {
			spans[i] = span{start: len(sequences), length: 1}
			sequences = append(sequences, ids)
			continue
		}

		// Sequence exceeds context window — split at token boundaries.
		// ids[0] is CLS, ids[len-1] is SEP (add_special=true).
		cls := ids[0]
		sep := ids[len(ids)-1]
		content := ids[1 : len(ids)-1]
		windowSize := maxTokens - 2 // reserve 2 slots for CLS + SEP

		spanStart := len(sequences)
		for start := 0; start < len(content); start += windowSize {
			end := start + windowSize
			if end > len(content) {
				end = len(content)
			}
			window := make([]int, 0, end-start+2)
			window = append(window, cls)
			window = append(window, content[start:end]...)
			window = append(window, sep)
			sequences = append(sequences, window)
		}
		spans[i] = span{start: spanStart, length: len(sequences) - spanStart}
	}

	// Phase 2: single batch embed call with all pre-tokenized sequences.
	allVecs, err := s.sup.client.EmbedBatchTokenIDs(ctx, sequences)
	if err != nil {
		return nil, err
	}

	// Phase 3: re-assemble — average sub-window vectors for split texts.
	result := make([][]float32, len(texts))
	for i, sp := range spans {
		if sp.length == 1 {
			result[i] = allVecs[sp.start]
			continue
		}
		// Average sp.length vectors element-wise.
		dim := len(allVecs[sp.start])
		avg := make([]float32, dim)
		for k := 0; k < sp.length; k++ {
			v := allVecs[sp.start+k]
			for d := range avg {
				avg[d] += v[d]
			}
		}
		n := float32(sp.length)
		for d := range avg {
			avg[d] /= n
		}
		result[i] = avg
	}
	return result, nil
}

// embedRaw skips the queue *and* the prefix logic. It exists as a test helper
// for the parity gate: the reference file stores the exact text that was fed
// to the model, so the gate must not re-apply the prefix. This method is
// deliberately lowercase (package-private) — production handlers must go
// through EmbedQuery / EmbedTexts.
func (s *Service) embedRaw(ctx context.Context, texts []string) ([][]float32, error) {
	if s.disabled {
		return nil, ErrDisabled
	}
	if s.sup.dead.Load() {
		return nil, ErrSupervisor
	}
	if len(texts) == 0 {
		return nil, nil
	}
	return s.sup.client.Embeddings(ctx, texts)
}

// resolveGGUFPath walks the precedence chain:
//  1. CIX_GGUF_PATH (absolute path env override, validated by Stat).
//  2. cfg.EmbeddingModel as absolute path — when the dashboard's "Local
//     path" mode wrote it through to the runtime_settings row.
//  3. Cached file under cfg.GGUFCacheDir/<safe-repo>/*.gguf when
//     cfg.EmbeddingModel is an HF repo ID.
//  4. CIX_BOOTSTRAP_GGUF_PATH one-shot import — copies the file into
//     the cache layout, then behaves like step 3 forever after.
//  5. HuggingFace download into the same cix cache (this is the path
//     that actually writes to disk).
//
// PR-E removed the implicit `bench/results/reference_gguf_path.txt` dev
// fallback that used to short-circuit step 2 — operators must now make
// the choice explicitly via env or the dashboard. Only step 5 is
// expensive; all others are stat-only or one-time copies.
func resolveGGUFPath(ctx context.Context, cfg *config.Config, logger *slog.Logger) (string, error) {
	if cfg.GGUFPath != "" {
		if _, err := os.Stat(cfg.GGUFPath); err != nil {
			return "", fmt.Errorf("CIX_GGUF_PATH=%s: %w", cfg.GGUFPath, err)
		}
		return cfg.GGUFPath, nil
	}
	// PR-E — the dashboard's "Local path" mode writes an absolute path into
	// embedding_model. Treat it as such instead of trying to interpret it
	// as an HF repo id (which would fail the slash check or, worse, send
	// the path to api.huggingface.co).
	if filepath.IsAbs(cfg.EmbeddingModel) {
		if _, err := os.Stat(cfg.EmbeddingModel); err != nil {
			return "", fmt.Errorf("embedding model path %s: %w", cfg.EmbeddingModel, err)
		}
		return cfg.EmbeddingModel, nil
	}
	// HF repo ids look like "<owner>/<repo>" — exactly one slash, no leading "/".
	if !strings.Contains(cfg.EmbeddingModel, "/") {
		return "", fmt.Errorf("embedding model %q is neither an absolute path nor an HF repo id (owner/repo)", cfg.EmbeddingModel)
	}

	// Cache-hit short-circuit: if we already downloaded a .gguf from this repo
	// into the cache, use it — HF downloader would do the same stat first,
	// but doing it here keeps the service silent in the happy path.
	if cached := findCachedGGUF(cfg.GGUFCacheDir, cfg.EmbeddingModel); cached != "" {
		logger.Info("using cached gguf", "path", cached)
		return cached, nil
	}

	// CIX_BOOTSTRAP_GGUF_PATH — one-time import path. Used so a fresh
	// container with a freshly-mounted cache volume doesn't have to
	// re-download a 280 MB GGUF the operator already has on disk. Once
	// the file lands in the cache layout, the next boot satisfies the
	// findCachedGGUF branch above and the bootstrap path is never read
	// again (idempotent — repeated boots with the same env are no-ops).
	if cfg.BootstrapGGUFPath != "" {
		imported, err := importBootstrapGGUF(cfg.GGUFCacheDir, cfg.EmbeddingModel, cfg.BootstrapGGUFPath, logger)
		if err != nil {
			logger.Warn("bootstrap gguf import failed; falling through to HF download",
				"src", cfg.BootstrapGGUFPath, "err", err)
		} else if imported != "" {
			return imported, nil
		}
	}

	return DownloadGGUF(ctx, cfg.EmbeddingModel, cfg.GGUFCacheDir, logger)
}

// importBootstrapGGUF copies srcPath into <cacheDir>/<safe_repo>/<basename>
// atomically (write to .partial, fsync, rename). Returns the final path
// on success, "" if the source is missing (caller falls through to HF
// download), or an error for IO problems we should surface to the operator.
//
// safe_repo derived from the HF repo id (`owner/repo` → `owner__repo`)
// to match DownloadGGUF's layout exactly — so subsequent boots' cache
// scan finds the imported file under the same name HF would have used.
func importBootstrapGGUF(cacheDir, repo, srcPath string, logger *slog.Logger) (string, error) {
	if cacheDir == "" || repo == "" {
		return "", nil
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		// Missing file is not a hard error — the operator may have set
		// the env optimistically with a path that lives on a host they
		// haven't mounted yet. Let the caller fall through to download.
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat bootstrap gguf %s: %w", srcPath, err)
	}
	if srcInfo.IsDir() {
		return "", fmt.Errorf("bootstrap gguf %s is a directory, expected file", srcPath)
	}

	safeRepo := strings.ReplaceAll(repo, "/", "__")
	targetDir := filepath.Join(cacheDir, safeRepo)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache dir: %w", err)
	}
	finalPath := filepath.Join(targetDir, filepath.Base(srcPath))

	// Idempotency: if a previous boot already imported the same file,
	// trust it — re-importing would be wasted IO and could race with a
	// concurrent boot of a sibling container against a shared volume.
	if _, err := os.Stat(finalPath); err == nil {
		return finalPath, nil
	}

	logger.Info("importing bootstrap gguf into cache",
		"src", srcPath, "dst", finalPath, "size", srcInfo.Size())

	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open bootstrap gguf: %w", err)
	}
	defer src.Close()

	partial := finalPath + ".partial"
	dst, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create cache target: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(partial)
		return "", fmt.Errorf("copy bootstrap gguf: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		_ = os.Remove(partial)
		return "", fmt.Errorf("fsync bootstrap gguf: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("close bootstrap gguf: %w", err)
	}
	if err := os.Rename(partial, finalPath); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("atomic rename bootstrap gguf: %w", err)
	}
	logger.Info("bootstrap gguf imported", "path", finalPath)
	return finalPath, nil
}

// findCachedGGUF looks for a previously-downloaded .gguf under the standard
// cache layout produced by DownloadGGUF. Returns "" on any miss (including
// IO errors) so the caller proceeds to the download path.
func findCachedGGUF(cacheDir, repo string) string {
	safeRepo := strings.ReplaceAll(repo, "/", "__")
	dir := cacheDir + string(os.PathSeparator) + safeRepo
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 5 && strings.EqualFold(name[len(name)-5:], ".gguf") {
			return dir + string(os.PathSeparator) + name
		}
	}
	return ""
}
