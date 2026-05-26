package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider/ollama"


	// Blank imports trigger each provider package's init() which
	// registers a Factory in the registry. Service builds the active
	// provider purely by kind string — these imports are the wiring.
	_ "github.com/dvcdsys/code-index/server/internal/embeddings/provider/openai"
	_ "github.com/dvcdsys/code-index/server/internal/embeddings/provider/voyage"
)

// Service is the public embeddings API used by handlers and the indexer.
// It composes:
//   - the active embedding provider (ollama sidecar / OpenAI / Voyage / …)
//   - a concurrency queue for backpressure
//
// Concurrency. Embed* methods are safe under concurrent callers — they
// each acquire a slot from the queue and release it on return. Provider
// swaps (SwitchProvider) drain the queue first to avoid stranding
// in-flight requests on a torn-down child process.
//
// A Service with disabled == true is a legal no-op used in tests; every
// method returns ErrDisabled. main.go constructs it that way when
// cfg.EmbeddingsEnabled is false.
type Service struct {
	cfg    *config.Config
	logger *slog.Logger

	queue    *Queue
	disabled bool

	// mu guards current — swaps happen behind it (under BlockNew/Resume
	// at the queue layer, but mu makes the swap itself atomic).
	mu      sync.RWMutex
	current provider.Provider
}

// New constructs a Service from the env-derived config. The legacy
// entry point: builds an ollama provider with the env-supplied
// defaults and blocks until Start succeeds. main.go uses NewWithBoot
// to layer the DB-persisted provider selection on top of this.
//
// ctx governs startup only; Stop has its own context.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.EmbeddingsEnabled {
		logger.Info("embeddings service disabled (CIX_EMBEDDINGS_ENABLED=false)")
		return &Service{cfg: cfg, logger: logger, disabled: true}, nil
	}

	prov, err := buildOllamaFromConfig(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("build ollama provider: %w", err)
	}
	if err := prov.Start(ctx); err != nil {
		return nil, err
	}

	return &Service{
		cfg:     cfg,
		logger:  logger,
		queue:   NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second),
		current: prov,
	}, nil
}

// NewWithProvider constructs a Service around an already-built
// Provider. Used by main.go's boot path: it reads the persisted
// provider snapshot, calls provider.Build, then hands the result to
// this constructor. The Provider must already be Start()-ed.
func NewWithProvider(cfg *config.Config, prov provider.Provider, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.EmbeddingsEnabled {
		return &Service{cfg: cfg, logger: logger, disabled: true}
	}
	return &Service{
		cfg:     cfg,
		logger:  logger,
		queue:   NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second),
		current: prov,
	}
}

// BuildOllamaConfigFromEnv produces the ollama provider config blob
// derived from env (used by main.go to seed the persisted row on
// first boot and by tests that want a "live env-default" snapshot).
func BuildOllamaConfigFromEnv(cfg *config.Config) ([]byte, error) {
	c := ollama.Config{
		Model:         cfg.EmbeddingModel,
		GGUFPath:      cfg.GGUFPath,
		CacheDir:      cfg.GGUFCacheDir,
		BootstrapPath: cfg.BootstrapGGUFPath,
		BinDir:        cfg.LlamaBinDir,
		SocketPath:    cfg.LlamaSocketPath,
		Transport:     cfg.LlamaTransport,
		CtxSize:       cfg.LlamaCtxSize,
		NGpuLayers:    cfg.LlamaNGpuLayers,
		NThreads:      cfg.LlamaNThreads,
		BatchSize:     cfg.LlamaBatchSize,
		StartupSec:    cfg.LlamaStartupSec,
	}
	return json.Marshal(c)
}

// EnvSecrets returns the production SecretLookup: os.LookupEnv. main.go
// and the admin handlers pass it to provider.Build / Service.SwitchProvider.
func EnvSecrets() provider.SecretLookup { return envSecrets }

// SwitchProvider replaces the active provider. Steps:
//  1. Build the new provider from kind + cfg.
//  2. Start it (validates config / connectivity).
//  3. Drain the queue (block new acquires, wait up to 30s).
//  4. Swap current to new under the mutex.
//  5. Stop the old provider on a separate goroutine so a slow SIGTERM
//     does not hold the admin request.
//
// If step 2 fails, the old provider stays active and the error is
// returned to the caller. If step 3 times out we proceed anyway,
// favouring availability — in-flight calls finish on the old
// provider and the new takes over for everything subsequent.
func (s *Service) SwitchProvider(ctx context.Context, kind string, cfgBytes []byte) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}

	newProv, err := provider.Build(ctx, kind, cfgBytes, envSecrets, s.logger)
	if err != nil {
		return fmt.Errorf("build %s provider: %w", kind, err)
	}
	if err := newProv.Start(ctx); err != nil {
		return fmt.Errorf("start %s provider: %w", kind, err)
	}

	s.queue.BlockNew()
	drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
	if derr := s.queue.WaitDrain(drainCtx); derr != nil {
		s.logger.Warn("embeddings: drain timed out during switch; proceeding anyway",
			"in_flight", s.queue.InFlight(), "err", derr,
		)
	}
	drainCancel()
	s.queue.Resume()

	s.mu.Lock()
	old := s.current
	s.current = newProv
	s.mu.Unlock()

	if old != nil {
		go func(p provider.Provider) {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := p.Stop(stopCtx); err != nil {
				s.logger.Warn("embeddings: old provider Stop returned error",
					"kind", p.Kind(), "err", err)
			}
		}(old)
	}
	s.logger.Info("embeddings: switched provider", "kind", kind, "id", newProv.ID())
	return nil
}

// buildOllamaFromConfig assembles an ollama.Provider out of the env-
// derived *config.Config. Bridges the legacy bootstrap path until the
// provider config persists into runtime_settings (Phase 6).
func buildOllamaFromConfig(cfg *config.Config, logger *slog.Logger) (provider.Provider, error) {
	c := ollama.Config{
		Model:         cfg.EmbeddingModel,
		GGUFPath:      cfg.GGUFPath,
		CacheDir:      cfg.GGUFCacheDir,
		BootstrapPath: cfg.BootstrapGGUFPath,
		BinDir:        cfg.LlamaBinDir,
		SocketPath:    cfg.LlamaSocketPath,
		Transport:     cfg.LlamaTransport,
		CtxSize:       cfg.LlamaCtxSize,
		NGpuLayers:    cfg.LlamaNGpuLayers,
		NThreads:      cfg.LlamaNThreads,
		BatchSize:     cfg.LlamaBatchSize,
		StartupSec:    cfg.LlamaStartupSec,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama config: %w", err)
	}
	return provider.Build(context.Background(), provider.KindOllama, b, envSecrets, logger)
}

// envSecrets resolves env-var names via os.LookupEnv. Production
// SecretLookup; tests pass their own to avoid touching the process
// environment.
func envSecrets(name string) (string, bool) {
	return os.LookupEnv(name)
}

// Config returns the *config.Config the service was constructed with.
func (s *Service) Config() *config.Config {
	if s == nil {
		return nil
	}
	return s.cfg
}

// CacheDirFromService returns the GGUF cache directory the dashboard's
// /admin/models handler should walk. Returns "" when the
// EmbeddingsQuerier isn't a *Service whose active provider is ollama
// (e.g. test fakes, openai/voyage active).
func CacheDirFromService(q any) string {
	s, ok := q.(*Service)
	if !ok || s == nil {
		return ""
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return ""
	}
	ol, ok := cur.(*ollama.Provider)
	if !ok {
		return ""
	}
	return ol.CacheDir()
}

// Stop tears the current provider down within ctx. Safe on a disabled
// or never-started Service.
func (s *Service) Stop(ctx context.Context) error {
	if s == nil || s.disabled {
		return nil
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return nil
	}
	return cur.Stop(ctx)
}

// Status returns a snapshot for the dashboard. State="disabled" when
// embeddings were turned off at boot.
func (s *Service) Status() provider.Status {
	if s == nil || s.disabled {
		return provider.Status{State: provider.StateDisabled}
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return provider.Status{State: provider.StateFailed, LastError: "provider not initialised"}
	}
	st := cur.Status()
	if s.queue != nil {
		st.InFlight = s.queue.InFlight()
	}
	return st
}

// CurrentKind reports the kind of the active provider, or "" when
// disabled / not yet built. Used by /status and admin endpoints.
func (s *Service) CurrentKind() string {
	if s == nil || s.disabled {
		return ""
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return ""
	}
	return cur.Kind()
}

// EmbeddingModel returns the active provider's fingerprint ID(). Used
// by repojobs to detect drift against projects.indexed_with_model.
func (s *Service) EmbeddingModel() string {
	if s == nil || s.disabled {
		return ""
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return ""
	}
	return cur.ID()
}

// Restart preserves the legacy admin /sidecar/restart contract: drain
// the queue, swap in a freshly-built provider with the supplied cfg,
// Start it. Currently only supports the ollama provider; openai/voyage
// callers use SwitchProvider directly.
func (s *Service) Restart(ctx context.Context, cfg *config.Config) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}

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

	if cfg.MaxEmbeddingConcurrency != cap(s.queue.slots) {
		s.queue = NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second)
	}

	newProv, err := buildOllamaFromConfig(cfg, s.logger)
	if err != nil {
		return fmt.Errorf("rebuild ollama provider: %w", err)
	}

	// Stop the old, start the new. On Start failure leave current==nil
	// so subsequent calls fail fast with ErrSupervisor — the operator
	// then re-Restart with corrected config.
	s.mu.Lock()
	old := s.current
	s.current = nil
	s.mu.Unlock()
	if old != nil {
		stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
		_ = old.Stop(stopCtx)
		stopCancel()
	}
	if err := newProv.Start(ctx); err != nil {
		s.logger.Error("embeddings: restart Start failed; provider remains down", "err", err)
		return err
	}
	s.mu.Lock()
	s.current = newProv
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// Ready reports whether the embeddings pipeline can serve a request.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return ErrSupervisor
	}
	err := cur.Ready(ctx)
	if errors.Is(err, provider.ErrUnrecoverable) {
		return ErrSupervisor
	}
	if errors.Is(err, provider.ErrNotReady) {
		return ErrNotReady
	}
	return err
}

// EmbedQuery delegates to the active provider after acquiring a queue
// slot. The provider applies its own query-side transform (ollama
// prefix, voyage input_type=query, openai pass-through).
func (s *Service) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if s == nil || s.disabled {
		return nil, ErrDisabled
	}
	cur, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer s.queue.Release(slotStart)
	return cur.EmbedQuery(ctx, query)
}

// EmbedTexts embeds passages unchanged.
func (s *Service) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if s == nil || s.disabled {
		return nil, ErrDisabled
	}
	if len(texts) == 0 {
		return nil, nil
	}
	cur, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer s.queue.Release(slotStart)
	return cur.EmbedDocuments(ctx, texts)
}

// TokenizeAndEmbed runs the token-aware embedding pipeline. For
// providers that don't support native tokenization
// (SupportsTokenize() == false) this is identical to EmbedTexts —
// callers must chunk inputs themselves before reaching here.
func (s *Service) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if s == nil || s.disabled {
		return nil, ErrDisabled
	}
	if len(texts) == 0 {
		return nil, nil
	}
	cur, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer s.queue.Release(slotStart)
	if cur.SupportsTokenize() {
		return cur.TokenizeAndEmbed(ctx, texts)
	}
	return cur.EmbedDocuments(ctx, texts)
}

// acquireProvider acquires a queue slot and returns the active
// provider snapshot. Caller is responsible for queue.Release once the
// call returns (deferred at call site so the slot is released even on
// provider error).
func (s *Service) acquireProvider(ctx context.Context) (provider.Provider, error) {
	if err := s.queue.Acquire(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		// We hold the slot but have nothing to call — release it before
		// returning the error so subsequent callers aren't starved.
		s.queue.Release(time.Now())
		return nil, ErrSupervisor
	}
	return cur, nil
}
