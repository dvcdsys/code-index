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
	"github.com/dvcdsys/code-index/server/internal/vectorstore"

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

	// lifecycleMu serializes the two provider-lifecycle operations
	// (SwitchProvider and Restart) against each other so they never
	// interleave their s.current / s.queue mutations or both tear down a
	// provider. Embed* methods do NOT take it — they run concurrently via
	// the queue + an s.mu snapshot of current/queue.
	lifecycleMu sync.Mutex

	// mu guards current AND the queue pointer — both are swapped at
	// runtime (current by SwitchProvider/Restart, queue by Restart when
	// the concurrency cap changes), so every read must snapshot them
	// under the read lock rather than touching the fields directly.
	mu      sync.RWMutex
	current provider.Provider

	// Vector-store reopen hooks, wired by main.go via AttachVectorStore.
	// When set, SwitchProvider reopens the vector store under the new
	// provider's identity slug and atomically swaps it into vsHolder, so
	// a runtime provider switch moves to a dimension-isolated namespace
	// without a process restart. All four are nil in tests that don't
	// exercise the reopen path (SwitchProvider then only swaps the
	// provider, matching the pre-unification behaviour).
	vsHolder  *vectorstore.Holder
	vsDirFor  func(slug string) string                     // cfg.ChromaDirForSlug
	vsOpener  func(dir string) (*vectorstore.Store, error) // vectorstore.Open
	vsMigrate func() error                                 // legacy chroma-dir prefix migration (idempotent)
}

// AttachVectorStore wires the live vector-store reopen path used by
// SwitchProvider. main.go calls it once after constructing the Service
// and the shared Holder:
//
//	dirFor  — cfg.ChromaDirForSlug (maps a StorageSlug to an on-disk dir)
//	opener  — vectorstore.Open
//	migrate — optional idempotent legacy-dir migration run before each
//	          reopen (lets a switch back to ollama on a pre-unification
//	          box adopt its renamed dir without a restart); may be nil
//
// Passing the formula (dirFor) and opener as funcs keeps embeddings free
// of a hard dependency on config path layout and avoids an
// embeddings→storage import for the migration hook.
func (s *Service) AttachVectorStore(
	holder *vectorstore.Holder,
	dirFor func(slug string) string,
	opener func(dir string) (*vectorstore.Store, error),
	migrate func() error,
) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.vsHolder = holder
	s.vsDirFor = dirFor
	s.vsOpener = opener
	s.vsMigrate = migrate
	s.mu.Unlock()
}

// StorageSlug returns the filesystem slug of the ACTIVE provider's
// identity (provider.StorageSlug(current.ID())), or "" when disabled /
// not yet built. The dashboard's project-detail handler uses it to show
// the live chroma directory.
func (s *Service) StorageSlug() string {
	if s == nil || s.disabled {
		return ""
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		return ""
	}
	return provider.StorageSlug(cur.ID())
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
	// Serialize against Restart so the two lifecycle ops never interleave
	// their s.current / s.queue mutations.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	newProv, err := provider.Build(ctx, kind, cfgBytes, envSecrets, s.logger)
	if err != nil {
		return fmt.Errorf("build %s provider: %w", kind, err)
	}
	if err := newProv.Start(ctx); err != nil {
		return fmt.Errorf("start %s provider: %w", kind, err)
	}

	q := s.currentQueue()
	q.BlockNew()
	// Keep the queue blocked across BOTH the provider swap and the
	// vector-store reopen below; resume only on the way out (all return
	// paths). A blocked queue fails Acquire fast with a 503, so no embed
	// runs in the window where s.current is the NEW provider while the
	// Holder still points at the OLD store — that window is exactly what
	// would write new-dimension vectors into the previous provider's
	// collection.
	defer q.Resume()
	drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
	if derr := q.WaitDrain(drainCtx); derr != nil {
		s.logger.Warn("embeddings: drain timed out during switch; proceeding anyway",
			"in_flight", q.InFlight(), "err", derr,
		)
	}
	drainCancel()

	s.mu.Lock()
	old := s.current
	s.current = newProv
	s.mu.Unlock()

	// Reopen the vector store under the new provider's identity slug so
	// its (possibly different-dimension) vectors land in their own
	// namespace instead of colliding with the previous provider's
	// collection. If the reopen fails we roll the provider swap BACK to
	// the old provider (whose store the Holder still points at) and stop
	// the new provider we started: fail closed, never leave a
	// new-provider / old-store pairing that corrupts the old collection.
	if err := s.reopenVectorStore(newProv); err != nil {
		s.mu.Lock()
		s.current = old
		s.mu.Unlock()
		go stopProviderAsync(s.logger, newProv)
		s.logger.Error("embeddings: provider switch rolled back — vector store reopen failed",
			"kind", kind, "err", err)
		return err
	}

	if old != nil {
		go stopProviderAsync(s.logger, old)
	}
	s.logger.Info("embeddings: switched provider", "kind", kind, "id", newProv.ID())
	return nil
}

// stopProviderAsync stops a provider in the background with a bounded
// timeout, logging (not failing) on error. Used to release the provider
// displaced by a switch — or the half-started new provider on a switch
// rollback — without blocking the caller.
func stopProviderAsync(logger *slog.Logger, p provider.Provider) {
	if p == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		logger.Warn("embeddings: provider Stop returned error", "kind", p.Kind(), "err", err)
	}
}

// reopenVectorStore opens a fresh *vectorstore.Store under the directory
// derived from prov's identity slug and atomically swaps it into the
// shared Holder. No-op when AttachVectorStore was never called (tests).
func (s *Service) reopenVectorStore(prov provider.Provider) error {
	s.mu.RLock()
	holder, dirFor, opener, migrate := s.vsHolder, s.vsDirFor, s.vsOpener, s.vsMigrate
	s.mu.RUnlock()
	if holder == nil || dirFor == nil || opener == nil {
		return nil // reopen path not wired (e.g. unit tests)
	}
	if migrate != nil {
		// Idempotent legacy-dir prefixing — lets a switch back to ollama
		// on a pre-unification box adopt its renamed dir without restart.
		if err := migrate(); err != nil {
			s.logger.Warn("embeddings: chroma legacy-dir migration failed during switch (continuing)", "err", err)
		}
	}
	dir := dirFor(provider.StorageSlug(prov.ID()))
	newStore, err := opener(dir)
	if err != nil {
		s.logger.Error("embeddings: provider switched but vector store reopen failed; keeping previous store until restart",
			"dir", dir, "err", err)
		return fmt.Errorf("reopen vector store at %s: %w", dir, err)
	}
	holder.Swap(newStore)
	s.logger.Info("embeddings: vector store reopened under new provider namespace", "dir", dir)
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
	if q := s.currentQueue(); q != nil {
		st.InFlight = q.InFlight()
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

// Restart applies runtime-config changes to the live Service.
//
// Provider-aware:
//   - When the active provider is ollama, this is the legacy
//     "respawn the sidecar with new flags" path: drain queue, build
//     a new ollama provider with cfg, stop the old child, start the
//     new one.
//   - When the active provider is HTTP-only (openai / voyage), there
//     is no sidecar to respawn. The only runtime-config field that
//     still applies is max_embedding_concurrency (the Service-level
//     queue depth). We rebuild the queue if it changed and leave the
//     provider untouched.
//
// SwitchProvider is the right call for swapping the active provider
// itself; Restart only touches knobs that the runtime_config row
// owns.
func (s *Service) Restart(ctx context.Context, cfg *config.Config) error {
	if s == nil || s.disabled {
		return ErrDisabled
	}
	// Serialize against SwitchProvider so the two lifecycle ops never
	// interleave their s.current / s.queue mutations.
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	// Snapshot the live queue, block + drain it. The queue stays blocked
	// through the respawn below and is resumed via the deferred
	// activeQ.Resume() once s.current is valid again (activeQ is oldQ, or
	// the new queue when the concurrency cap changed).
	oldQ := s.currentQueue()
	oldQ.BlockNew()
	drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := oldQ.WaitDrain(drainCtx); err != nil {
		drainCancel()
		s.logger.Warn("embeddings: drain timed out, proceeding with restart anyway",
			"in_flight", oldQ.InFlight(), "err", err,
		)
	} else {
		drainCancel()
	}

	// activeQ is the queue that stays live through the respawn below and
	// must be resumed on the way out. When the concurrency cap changes we
	// install a NEW queue — it must also start blocked, otherwise embed
	// callers acquire a slot on it during the sidecar respawn (when
	// s.current is briefly nil) and get ErrSupervisor instead of a clean
	// drain-style 503. Resuming oldQ would be a no-op (it's discarded).
	activeQ := oldQ
	if cfg.MaxEmbeddingConcurrency != cap(oldQ.slots) {
		newQ := NewQueue(cfg.MaxEmbeddingConcurrency, time.Duration(cfg.EmbeddingQueueTimeout)*time.Second)
		newQ.BlockNew()
		s.mu.Lock()
		s.queue = newQ
		s.mu.Unlock()
		activeQ = newQ
	}
	defer activeQ.Resume()

	// Snapshot the live provider's kind under the read lock — we don't
	// want to swap an ollama for a voyage just because the runtime-
	// config form was submitted.
	s.mu.RLock()
	curKind := ""
	if s.current != nil {
		curKind = s.current.Kind()
	}
	s.mu.RUnlock()

	if curKind != provider.KindOllama {
		// HTTP-only provider: queue (re)built above is all there is to
		// do. The cfg blob is persisted by the caller; we just stash
		// the new *config.Config snapshot so subsequent /status / cfg
		// reads return the live values.
		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		s.logger.Info("embeddings: restart applied to remote provider (queue only)",
			"kind", curKind, "concurrency", cfg.MaxEmbeddingConcurrency,
		)
		return nil
	}

	// Ollama path: rebuild + respawn the sidecar with the supplied
	// llama tuning fields.
	newProv, err := buildOllamaFromConfig(cfg, s.logger)
	if err != nil {
		return fmt.Errorf("rebuild ollama provider: %w", err)
	}
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
	cur, q, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer q.Release(slotStart)
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
	cur, q, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer q.Release(slotStart)
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
	cur, q, err := s.acquireProvider(ctx)
	if err != nil {
		return nil, err
	}
	slotStart := time.Now()
	defer q.Release(slotStart)
	if cur.SupportsTokenize() {
		return cur.TokenizeAndEmbed(ctx, texts)
	}
	return cur.EmbedDocuments(ctx, texts)
}

// currentQueue returns the active queue under the read lock. Restart
// swaps the queue pointer when the concurrency cap changes, so callers
// must snapshot it rather than reading s.queue directly (otherwise the
// read races the swap).
func (s *Service) currentQueue() *Queue {
	s.mu.RLock()
	q := s.queue
	s.mu.RUnlock()
	return q
}

// acquireProvider acquires a queue slot and returns the active provider
// snapshot AND the queue the slot was taken from. The caller must
// Release on the RETURNED queue (not s.queue, which Restart may have
// swapped meanwhile) — deferred at the call site so the slot is released
// even on provider error.
func (s *Service) acquireProvider(ctx context.Context) (provider.Provider, *Queue, error) {
	q := s.currentQueue()
	if err := q.Acquire(ctx); err != nil {
		return nil, nil, err
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur == nil {
		// We hold the slot but have nothing to call — release it before
		// returning the error so subsequent callers aren't starved.
		q.Release(time.Now())
		return nil, nil, ErrSupervisor
	}
	return cur, q, nil
}
