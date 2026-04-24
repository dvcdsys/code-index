package embeddings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
		StartupSec: cfg.LlamaStartupSec,
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

// Stop tears the supervisor down within the ctx deadline. Safe to call on a
// disabled or partially-initialised Service.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil || s.disabled || s.sup == nil {
		return nil
	}
	return s.sup.Stop(ctx)
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
//  1. CIX_GGUF_PATH (already applied to cfg.GGUFPath before Validate).
//  2. bench/results/reference_gguf_path.txt dev fallback (Validate handles it).
//  3. Cached file under cfg.GGUFCacheDir/<safe-repo>/*.gguf.
//  4. HuggingFace download (this is the path that actually writes to disk).
//
// Only step 4 can be expensive; all others are stat-only.
func resolveGGUFPath(ctx context.Context, cfg *config.Config, logger *slog.Logger) (string, error) {
	if cfg.GGUFPath != "" {
		if _, err := os.Stat(cfg.GGUFPath); err != nil {
			return "", fmt.Errorf("CIX_GGUF_PATH=%s: %w", cfg.GGUFPath, err)
		}
		return cfg.GGUFPath, nil
	}
	// The embedding model is an HF repo id like "awhiteside/CodeRankEmbed-Q8_0-GGUF".
	// Only repo ids contain a slash; a raw filesystem path would have been
	// captured by the CIX_GGUF_PATH branch above.
	if !strings.Contains(cfg.EmbeddingModel, "/") {
		return "", fmt.Errorf("embedding model %q is neither a path nor an HF repo id", cfg.EmbeddingModel)
	}

	// Cache-hit short-circuit: if we already downloaded a .gguf from this repo
	// into the cache, use it — HF downloader would do the same stat first,
	// but doing it here keeps the service silent in the happy path.
	if cached := findCachedGGUF(cfg.GGUFCacheDir, cfg.EmbeddingModel); cached != "" {
		logger.Info("using cached gguf", "path", cached)
		return cached, nil
	}

	return DownloadGGUF(ctx, cfg.EmbeddingModel, cfg.GGUFCacheDir, logger)
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
