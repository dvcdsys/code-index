// Package ollama implements provider.Provider on top of an in-process
// llama-server child. It owns the supervisor (fork+exec, crash-restart
// budget, readiness probe), the unix/TCP llamaClient, the asymmetric
// retrieval prefix, the GGUF resolver, and the token-aware multi-pass
// embedding pipeline.
//
// The Service layer (embeddings.Service) brackets all calls with a
// Queue for backpressure; this package does not impose its own
// concurrency limit.
package ollama

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// Config is the typed shape of the ollama provider's persisted config
// (the JSON-blob in runtime_settings.embedding_provider_config when
// embedding_provider="ollama"). The Factory's Build method
// unmarshals JSON into this struct and validates.
type Config struct {
	// Model is the HF repo id ("owner/repo") or an absolute path to
	// a .gguf file. Resolved via ResolveGGUFPath at Start.
	Model string `json:"model"`

	// GGUFPath is an optional absolute-path override (CIX_GGUF_PATH
	// equivalent). When set it takes precedence over Model and the
	// cache lookup.
	GGUFPath string `json:"gguf_path,omitempty"`

	// CacheDir is where downloaded GGUFs live and where the
	// bootstrap-import drops files (CIX_GGUF_CACHE_DIR).
	CacheDir string `json:"cache_dir,omitempty"`

	// BootstrapPath is the one-shot import source
	// (CIX_BOOTSTRAP_GGUF_PATH). Idempotent across boots.
	BootstrapPath string `json:"bootstrap_path,omitempty"`

	// BinDir is the directory containing llama-server + its
	// dylibs (CIX_LLAMA_BIN_DIR).
	BinDir string `json:"bin_dir,omitempty"`

	// SocketPath is the unix socket for IPC with llama-server.
	// Auto-falls-back to TCP when this exceeds the platform limit
	// (104 bytes on darwin).
	SocketPath string `json:"socket_path,omitempty"`

	// Transport is "unix" or "tcp".
	Transport string `json:"transport,omitempty"`

	// CtxSize is the context window in tokens.
	CtxSize int `json:"ctx_size,omitempty"`

	// NGpuLayers: -1 = all on Metal, 0 = CPU only.
	NGpuLayers int `json:"n_gpu_layers"`

	// NThreads: 0 = let llama-server pick.
	NThreads int `json:"n_threads,omitempty"`

	// BatchSize: 0 = match CtxSize.
	BatchSize int `json:"batch_size,omitempty"`

	// StartupSec bounds the readiness probe.
	StartupSec int `json:"startup_sec,omitempty"`
}

// Provider is the ollama-backed provider.Provider implementation.
// One per active config; rebuilt (Stop+new+Start) when the admin
// changes any config field.
type Provider struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	sup      *supervisor
	started  atomic.Bool
	ggufPath string // resolved at Start; empty before
}

// New constructs an ollama Provider. Does not start it — call Start
// to spawn llama-server. Provider methods that need the running
// child return provider.ErrNotReady until Start succeeds.
func New(cfg Config, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{cfg: cfg, logger: logger}
}

// Kind reports the registered factory kind for this provider.
func (p *Provider) Kind() string { return provider.KindOllama }

// ID is the fingerprint stored in projects.indexed_with_model. Tied
// to the model name only — different GGUF tunings (ctx, threads,
// batch) do not change embedding output, so they must NOT change
// the ID (would force unnecessary reindexes).
func (p *Provider) ID() string {
	return "ollama:" + p.cfg.Model
}

// Dimension returns 0 — the vector store infers dimension from the
// first upsert (chromem-go behaviour) and CodeRankEmbed-Q8 reports
// it on first call.
func (p *Provider) Dimension() int { return 0 }

// SupportsTokenize is true: llama-server exposes /tokenize.
func (p *Provider) SupportsTokenize() bool { return true }

// StorageComponents namespaces the vector store as ollama/<model-slug>.
// Dimension is not part of the path: it is unknown at config time
// (Dimension() == 0) and the model name already pins the GGUF/quant.
func (p *Provider) StorageComponents() []string {
	return []string{provider.KindOllama, provider.StorageSlug(p.cfg.Model)}
}

// Start resolves the GGUF path then spawns the supervisor. Blocks
// until the readiness probe succeeds or ctx expires.
func (p *Provider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sup != nil {
		// Idempotent — re-Start on an already-running provider is a
		// no-op (Service uses Stop+new+Start for restarts, never
		// Start twice on the same instance, but be defensive).
		return nil
	}

	gguf, err := ResolveGGUFPath(ctx, GGUFInputs{
		GGUFPath:      p.cfg.GGUFPath,
		Model:         p.cfg.Model,
		CacheDir:      p.cfg.CacheDir,
		BootstrapPath: p.cfg.BootstrapPath,
	}, p.logger)
	if err != nil {
		return fmt.Errorf("resolve gguf: %w", err)
	}
	p.ggufPath = gguf

	supCfg := supervisorConfig{
		BinDir:     p.cfg.BinDir,
		GGUFPath:   gguf,
		SocketPath: p.cfg.SocketPath,
		Transport:  p.cfg.Transport,
		CtxSize:    p.cfg.CtxSize,
		NGpuLayers: p.cfg.NGpuLayers,
		NThreads:   p.cfg.NThreads,
		BatchSize:  p.cfg.BatchSize,
		StartupSec: p.cfg.StartupSec,
		Model:      p.cfg.Model,
	}
	sup, err := newSupervisor(ctx, supCfg, p.logger)
	if err != nil {
		return err
	}
	p.sup = sup
	p.started.Store(true)
	return nil
}

// Stop tears the supervisor down within ctx. Idempotent.
func (p *Provider) Stop(ctx context.Context) error {
	p.mu.Lock()
	sup := p.sup
	p.sup = nil
	p.started.Store(false)
	p.mu.Unlock()
	if sup == nil {
		return nil
	}
	return sup.Stop(ctx)
}

// Ready reports nil when llama-server is alive and responding to
// /health, provider.ErrUnrecoverable when the restart budget is
// exhausted, or provider.ErrNotReady while warming up.
func (p *Provider) Ready(ctx context.Context) error {
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		return provider.ErrNotReady
	}
	if sup.dead.Load() {
		return provider.ErrUnrecoverable
	}
	return sup.Ready(ctx)
}

// Status returns the dashboard snapshot.
func (p *Provider) Status() provider.Status {
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	st := provider.Status{
		ManagesProcess: true,
		Model:          p.cfg.Model,
	}
	if sup == nil {
		st.State = provider.StateDisabled
		return st
	}
	src := sup.Status()
	st.PID = src.PID
	st.UptimeSeconds = int64(src.Uptime.Seconds())
	st.LastError = src.LastError
	switch src.State {
	case "running":
		st.State = provider.StateRunning
	case "failed":
		st.State = provider.StateFailed
	case "starting":
		st.State = provider.StateStarting
	default:
		st.State = src.State
	}
	return st
}

// EmbedQuery prepends the asymmetric-retrieval prefix and embeds a
// single query.
func (p *Provider) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		return nil, provider.ErrNotReady
	}
	if sup.dead.Load() {
		return nil, provider.ErrUnrecoverable
	}
	if err := p.waitReady(ctx, sup); err != nil {
		return nil, err
	}
	text := ResolveQueryPrefix(p.cfg.Model) + query
	vecs, err := sup.client.Embeddings(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// EmbedDocuments embeds passages unchanged.
func (p *Provider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		return nil, provider.ErrNotReady
	}
	if sup.dead.Load() {
		return nil, provider.ErrUnrecoverable
	}
	if err := p.waitReady(ctx, sup); err != nil {
		return nil, err
	}
	return sup.client.Embeddings(ctx, texts)
}

// TokenizeAndEmbed is the token-aware embedding pipeline. For each
// text it:
//  1. Calls /tokenize to get token IDs (CLS + content + SEP).
//  2. Splits sequences longer than CtxSize at token boundaries.
//  3. Embeds all sequences in one /v1/embeddings call.
//  4. Averages sub-window vectors back to one vector per text.
func (p *Provider) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	sup := p.sup
	maxTokens := p.cfg.CtxSize
	p.mu.Unlock()
	if sup == nil {
		return nil, provider.ErrNotReady
	}
	if sup.dead.Load() {
		return nil, provider.ErrUnrecoverable
	}
	if err := p.waitReady(ctx, sup); err != nil {
		return nil, err
	}

	type span struct{ start, length int }
	spans := make([]span, len(texts))
	var sequences [][]int

	for i, text := range texts {
		ids, err := sup.client.Tokenize(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("tokenize text[%d]: %w", i, err)
		}

		if len(ids) == 0 {
			spans[i] = span{start: len(sequences), length: 1}
			sequences = append(sequences, []int{})
			continue
		}

		if len(ids) <= maxTokens {
			spans[i] = span{start: len(sequences), length: 1}
			sequences = append(sequences, ids)
			continue
		}

		cls := ids[0]
		sep := ids[len(ids)-1]
		content := ids[1 : len(ids)-1]
		// Reserve two slots for the CLS/SEP tokens. Guard against a
		// pathologically small CtxSize (<= 2): windowSize must be >= 1
		// or the split loop below never advances `start` and spins
		// forever while holding a queue slot.
		windowSize := max(maxTokens-2, 1)

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

	allVecs, err := sup.client.EmbedBatchTokenIDs(ctx, sequences)
	if err != nil {
		return nil, err
	}

	result := make([][]float32, len(texts))
	for i, sp := range spans {
		if sp.length == 1 {
			result[i] = allVecs[sp.start]
			continue
		}
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

// EmbedRaw is a parity-test helper: skip prefix, embed texts verbatim.
// Lower-case in production paths to discourage misuse; the parity
// test file in the embeddings package needs cross-package access so
// it lives upper-cased here.
func (p *Provider) EmbedRaw(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		return nil, provider.ErrNotReady
	}
	return sup.client.Embeddings(ctx, texts)
}

// CacheDir returns the configured GGUF cache directory. Used by the
// admin /models endpoint to enumerate cached files.
func (p *Provider) CacheDir() string { return p.cfg.CacheDir }

// waitReady waits up to 5 seconds for the supervisor's child to be
// ready. The 5s window is short — a healthy steady-state Service is
// always ready in <1ms; during restarts the queue has already been
// drained / blocked, so callers waiting here are by design rare.
func (p *Provider) waitReady(ctx context.Context, sup *supervisor) error {
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return sup.Ready(readyCtx)
}
