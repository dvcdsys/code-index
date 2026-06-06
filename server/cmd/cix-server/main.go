// cix-server is the Go replacement for the Python api/ FastAPI service.
// Phase 1: config + SQLite init + chi router with /health and /api/v1/status.
// Embeddings, indexer, projects, search — Phase 2+.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/chunker"
	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
	"github.com/dvcdsys/code-index/server/internal/embeddingscfg"
	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/httpapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/pollscheduler"
	"github.com/dvcdsys/code-index/server/internal/repojobs"
	"github.com/dvcdsys/code-index/server/internal/runtimecfg"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/storage"
	"github.com/dvcdsys/code-index/server/internal/tunnelcfg"
	"github.com/dvcdsys/code-index/server/internal/tunnels"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
	"github.com/dvcdsys/code-index/server/internal/versioncheck"
	"github.com/dvcdsys/code-index/server/internal/workspaceprojects"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

func runHealthcheck() {
	port := os.Getenv("CIX_PORT")
	if port == "" {
		port = "21847"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://localhost:" + port + "/health")
	if err != nil {
		os.Exit(1)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		os.Exit(1)
	}
	os.Exit(0)
}

func main() {
	printVersion := flag.Bool("v", false, "print version and exit")
	doHealthcheck := flag.Bool("healthcheck", false, "run health probe and exit")
	flag.Parse()
	if *printVersion {
		fmt.Printf("cix-server %s (%s, api %s)\n", version, backend, apiVersion)
		return
	}
	if *doHealthcheck {
		runHealthcheck()
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cix-server:", err)
		os.Exit(1)
	}
}

// parseLogLevel maps CIX_LOG_LEVEL (debug|info|warn|error, case-insensitive)
// to a slog level. Unset or unrecognised values fall back to info.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(os.Getenv("CIX_LOG_LEVEL"))}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// CIX_AUTH_DISABLED=true skips ALL auth — log loudly so the warning
	// shows up in container logs / Portainer. Bootstrap (further below)
	// also enforces "no users + no bootstrap env = fatal", so this branch
	// is the only path to a legitimately auth-free deployment.
	if cfg.AuthDisabled {
		logger.Warn("auth disabled (CIX_AUTH_DISABLED=true) — every endpoint is reachable without authentication")
	}

	chunker.Configure(cfg.Languages)
	logger.Info("chunker languages configured", "active", chunker.SupportedLanguages())

	// The system DB is model-INDEPENDENT (one permanent file at
	// cfg.SQLitePath holding accounts + catalog + parsed code). Older
	// builds suffixed the model name onto the path; adopt any such legacy
	// per-model file as the canonical system DB (idempotent, one-time).
	// LEGACY-MIGRATION (remove next release): drop this adoption call once
	// all deployments have booted on the unified layout.
	if err := storage.AdoptLegacyModelDB(cfg.SQLitePath, cfg.LegacyDynamicSQLitePath(), logger); err != nil {
		return fmt.Errorf("adopt legacy system db: %w", err)
	}
	dbPath := cfg.SQLitePath
	logger.Info("opening database", "path", dbPath)
	database, err := db.OpenWith(db.OpenOptions{
		Path:    dbPath,
		DataDir: cfg.WorkspacesDataDir,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("db close", "err", err)
		}
	}()

	// Webhook clean-break notice (Fix #4a): the migration that split
	// workspace_repos into git_repos + workspace_projects also changed
	// the webhook URL format. Surface a one-line WARN with row counts
	// so the operator knows to re-register in GitHub on upgrade.
	auditWebhookCleanBreak(context.Background(), database, logger)

	// PR-E — overlay dashboard-saved runtime overrides onto the env-loaded
	// config before any code path reads its fields. The DB row may not
	// exist yet (fresh install); resolution falls through to env / recommended
	// in that case so behaviour matches pre-PR-E exactly.
	rcfg := runtimecfg.New(database, cfg)
	snap, err := rcfg.Get(context.Background())
	if err != nil {
		return fmt.Errorf("load runtime_settings: %w", err)
	}
	snap.ApplyTo(cfg)
	logger.Info("runtime config resolved",
		"embedding_model", cfg.EmbeddingModel,
		"llama_ctx", cfg.LlamaCtxSize,
		"n_gpu_layers", cfg.LlamaNGpuLayers,
		"n_threads", cfg.LlamaNThreads,
		"max_concurrency", cfg.MaxEmbeddingConcurrency,
		"batch", cfg.LlamaBatchSize,
		"sources", snap.Source,
	)
	// The system DB is model-independent (opened above at cfg.SQLitePath).
	// Only the chroma vector store is namespaced per embedding identity —
	// it is opened below once the active provider is known, using
	// provider.StorageSlug(Provider.ID()).

	// Embeddings service. When disabled we still build the value so router
	// wiring stays consistent — Service methods return ErrDisabled in that case.
	// Startup is bounded by a context derived from LlamaStartupSec plus a grace
	// window for the HF download path on cold cache.
	startupCtx, startupCancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.LlamaStartupSec)*time.Second+30*time.Second)

	// Bootstrap pluggable-provider selection. If the runtime_settings row
	// has no embedding_provider yet (fresh install or pre-migration DB),
	// seed it with the env-derived ollama config and use that for boot.
	// Otherwise the persisted blob is authoritative — env vars (other
	// than API-key envs that providers read live) are ignored.
	embedCfgStore := embeddingscfg.New(database)
	persistedProv, hasProv, err := embedCfgStore.Get(context.Background())
	if err != nil {
		startupCancel()
		return fmt.Errorf("load embedding provider config: %w", err)
	}
	if !hasProv && cfg.EmbeddingsEnabled {
		seed, serr := embeddings.BuildOllamaConfigFromEnv(cfg)
		if serr != nil {
			startupCancel()
			return fmt.Errorf("seed embedding provider config: %w", serr)
		}
		if serr := embedCfgStore.Save(context.Background(),
			embeddingscfg.Snapshot{Kind: "ollama", Config: seed}, ""); serr != nil {
			logger.Warn("could not seed embedding provider config (continuing on env)", "err", serr)
		}
		persistedProv = embeddingscfg.Snapshot{Kind: "ollama", Config: seed}
		hasProv = true
	}

	var embedSvc *embeddings.Service
	if !cfg.EmbeddingsEnabled || !hasProv {
		// Legacy / disabled path — fall back to env-only ollama wiring.
		embedSvc, err = embeddings.New(startupCtx, cfg, logger)
	} else {
		prov, perr := provider.Build(startupCtx, persistedProv.Kind, persistedProv.Config, embeddings.EnvSecrets(), logger)
		if perr != nil {
			// The persisted provider config is malformed (bad JSON /
			// unknown kind). Don't brick the whole server over it: fall
			// back to env-only ollama so the dashboard stays reachable to
			// fix the config or re-switch the provider.
			logger.Error("could not build persisted embedding provider; falling back to env ollama (fix or re-switch via dashboard)",
				"kind", persistedProv.Kind, "err", perr)
			embedSvc, err = embeddings.New(startupCtx, cfg, logger)
		} else {
			if perr := prov.Start(startupCtx); perr != nil {
				// A remote provider's boot connect-test (or the ollama
				// sidecar spawn) failed — a transient upstream blip, a
				// revoked key, or a missing API-key env after a redeploy.
				// Attach the provider anyway instead of failing the whole
				// process: the HTTP server (dashboard, auth, every project
				// API) must stay up so an operator can recover, and remote
				// providers self-heal once the upstream/key is back. Embeds
				// return a clear error until then and Status() reports it.
				logger.Error("persisted embedding provider failed to start; continuing in degraded state — embeddings unavailable until it recovers or is switched",
					"kind", persistedProv.Kind, "err", perr)
			}
			embedSvc = embeddings.NewWithProvider(cfg, prov, logger)
		}
	}
	startupCancel()
	if err != nil {
		return fmt.Errorf("embeddings: %w", err)
	}
	// Shared shutdown context — see M7 below. We build it lazily (in the
	// signal handler) so startup doesn't carry a dangling deadline.
	var shutdownCtx context.Context
	defer func() {
		// Fallback for the path where shutdownCtx was never assigned (e.g.
		// startup-error branch): bound embeddings stop independently.
		ctx := shutdownCtx
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
		}
		if err := embedSvc.Stop(ctx); err != nil {
			logger.Error("embeddings stop", "err", err)
		}
	}()

	// Relocate a legacy Python ChromaDB store occupying the container path
	// itself, so chromaBase is free to become the nested container below.
	if backed, bErr := vectorstore.DetectLegacyAndBackup(cfg.ChromaPersistDir); bErr != nil {
		return fmt.Errorf("back up legacy python chroma store: %w", bErr)
	} else if backed {
		logger.Warn("legacy python chroma layout detected at container path — backed up; re-run cix init to reindex")
	}

	// Migrate pre-unification FLAT ollama dirs (<base>_<model>) into the
	// unified NESTED layout (<base>/ollama/<model-slug>/) so existing
	// vectors are reused without a reindex. Unambiguous: every flat sibling
	// is a legacy ollama dir (the provider kind is now its own path level,
	// not guessed from a name prefix). Idempotent.
	// LEGACY-MIGRATION (remove next release): drop once every deployment has
	// booted on the nested layout.
	if err := storage.MigrateFlatChromaToNested(cfg.ChromaPersistDir, logger); err != nil {
		// Fail closed. A half-completed move would leave the server opening
		// a fresh empty namespace while existing vectors sit under the
		// un-migrated legacy dir — search would silently return nothing on
		// a "healthy" server. Surface it so the operator fixes the cause
		// (e.g. dir perms under prod uid 1001) rather than losing the index.
		return fmt.Errorf("migrate legacy chroma dirs: %w", err)
	}

	// The vector store is namespaced by the ACTIVE provider's identity path
	// components (<kind>/<model>[/<variant>]) so vectors of different
	// dimensions never share a collection.
	components := embedSvc.StoragePath()
	if len(components) == 0 {
		// Embeddings disabled / provider not built: deterministic ollama-
		// shaped fallback so toggling embeddings on/off doesn't move dirs.
		components = []string{provider.KindOllama, provider.StorageSlug(cfg.EmbeddingModel)}
	}
	chromaDir := cfg.ChromaDirFor(components)

	vs, err := vectorstore.Open(chromaDir)
	if err != nil {
		return fmt.Errorf("open vectorstore: %w", err)
	}
	// Wrap in a swappable Holder shared by indexer / repojobs / httpapi so
	// a runtime provider switch can reopen the store under a new namespace.
	vsHolder := vectorstore.NewHolder(vs)
	// Wire the live-reopen path used by SwitchProvider.
	embedSvc.AttachVectorStore(
		vsHolder,
		cfg.ChromaDirFor,
		vectorstore.Open,
		func() error { return storage.MigrateFlatChromaToNested(cfg.ChromaPersistDir, logger) },
	)

	idx := indexer.New(database, vsHolder, embedSvc, logger)
	idx.SetEmbedIncludePath(cfg.EmbedIncludePath)
	// Record the active embedding model on every indexed project so the
	// dashboard can highlight stale vectors when the runtime provider /
	// model changes. Wire it as a live lookup so a runtime provider
	// switch (PUT /admin/embedding-providers/active) is reflected in the
	// next FinishIndexing without a process restart — the indexer reads
	// embedSvc.EmbeddingModel() at write time and that returns the active
	// Provider.ID() ("ollama:<model>" / "voyage:..."), matching what the
	// drift-detector and dashboard compare against.
	idx.SetEmbeddingModelLookup(embedSvc.EmbeddingModel)
	// Parallel + cross-file-batched indexing. Concurrency reuses the
	// embedding-queue cap (MaxEmbeddingConcurrency); the cross-file batch
	// size is its own runtime knob. Bound as a live lookup so a dashboard
	// runtime-config change takes effect on the next batch without a restart.
	idx.SetEmbedTuningLookup(func() (int, int) {
		snap, err := rcfg.Get(context.Background())
		if err != nil {
			return cfg.MaxEmbeddingConcurrency, cfg.IndexEmbedBatchChunks
		}
		return snap.MaxEmbeddingConcurrency, snap.IndexEmbedBatchChunks
	})
	if cfg.EmbedIncludePath {
		logger.Info("embedding format: path-aware preamble enabled (CIX_EMBED_INCLUDE_PATH=true) — full reindex required if upgrading")
	}
	// Stop housekeeping goroutines during shutdown so sessionTTL timers do not
	// leak for up to 1h past shutdown. m8 fix.
	defer idx.Shutdown()

	// Dashboard auth services. Built once and shared with the router.
	usrSvc := users.New(database)
	sessSvc := sessions.New(database)
	akSvc := apikeys.New(database)
	grpSvc := groups.New(database)

	if !cfg.AuthDisabled {
		if err := bootstrapAuth(context.Background(), cfg, logger, usrSvc, akSvc); err != nil {
			return fmt.Errorf("bootstrap auth: %w", err)
		}
	}

	// GitHub-repo + workspaces subsystem wiring. Both surfaces ship in
	// every release now — there's no opt-in flag. Boot fails hard if
	// the secrets layer can't initialise, because github_tokens rows
	// are unreadable without an encryption key and we'd rather refuse
	// to start than silently disable that path.
	secSvc, err := secrets.Open(secrets.OpenOptions{
		DataDir:       cfg.SecretsDataDir,
		Logger:        logger,
		AllowGenerate: true,
	})
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	ghSvc := githubtokens.New(database, secSvc)

	// Sanity gate: if encrypted rows exist but the resolved key cannot
	// decrypt them, refuse to start so the operator doesn't accidentally
	// nuke the data. Probing one row is enough — Decrypt fails uniformly
	// on a wrong key.
	n, err := ghSvc.CountWithEncryption(context.Background())
	if err != nil {
		return fmt.Errorf("secrets sanity: %w", err)
	}
	if n > 0 {
		toks, _ := ghSvc.List(context.Background())
		if len(toks) > 0 {
			if _, err := ghSvc.Reveal(context.Background(), toks[0].ID); err != nil {
				return fmt.Errorf("encryption key does not match existing github_tokens — refusing to start (recover the prior CIX_SECRET_KEY or wipe github_tokens manually): %w", err)
			}
		}
	}
	if secSvc.Autogenerated() {
		logger.Warn("a fresh encryption keyfile was generated this boot — back it up before redeploying",
			"source", secSvc.Source())
	} else {
		logger.Info("encryption key loaded", "source", secSvc.Source())
	}
	wsSvc := workspaces.New(database)
	grSvc := gitrepos.New(database)
	wpSvc := workspaceprojects.New(database)

	// Persistent job queue + worker pool. Worker concurrency comes
	// from CIX_WORKER_CONCURRENCY (default 2). Handlers are registered
	// before Start so racing inserts get picked up immediately.
	jobsSvc := jobs.New(database, jobs.Options{
		Concurrency: cfg.WorkerConcurrency,
		Logger:      logger,
	})
	repojobs.Register(repojobs.Deps{
		DB:                         database,
		Jobs:                       jobsSvc,
		GitRepos:                   grSvc,
		GithubTokens:               ghSvc,
		Indexer:                    idx,
		VectorStore:                vsHolder,
		DataDir:                    cfg.WorkspacesDataDir,
		Logger:                     logger,
		DefaultPollIntervalSeconds: int(cfg.DefaultPollInterval.Seconds()),
		MinPollIntervalSeconds:     int(cfg.MinPollInterval.Seconds()),
	})

	jobsCtx, jobsCancel := context.WithCancel(context.Background())
	jobsSvc.Start(jobsCtx)
	// Honest-state recovery: an external project a prior process left
	// mid-pipeline (e.g. OOM-killed) whose job was abandoned + exhausted its
	// retries is otherwise stuck showing 'indexing' with nothing driving it.
	// Flip those to 'error' so the dashboard is honest and the operator can
	// Sync (resume from file_hashes via reconcile). Runs after Start, which
	// recovered orphaned 'running' jobs synchronously.
	repojobs.ReconcileStuckProjects(context.Background(), database, logger)
	defer func() {
		// Cancel in-flight handlers (a long index run) FIRST so they abort
		// promptly. Otherwise Stop blocks for its full budget while indexing
		// keeps running, and the later database.Close() interrupts the
		// worker's queries ("interrupted (9)" flood). The aborted index
		// resumes via reconcile on next start.
		jobsCancel()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := jobsSvc.Stop(stopCtx); err != nil {
			logger.Warn("jobs: stop", "err", err)
		}
	}()
	logger.Info("jobs worker pool started",
		"concurrency", cfg.WorkerConcurrency,
		"data_dir", cfg.WorkspacesDataDir)

	// Background version-check poller. The 60s initial delay keeps GitHub
	// off the boot path; the goroutine exits cleanly when bgCtx is canceled
	// in the shutdown branch below.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	vcSvc := versioncheck.New(versioncheck.Config{
		Enabled:        cfg.VersionCheckEnabled,
		Interval:       cfg.VersionCheckInterval,
		InitialDelay:   60 * time.Second,
		Repo:           cfg.VersionCheckRepo,
		CurrentVersion: version,
	}, logger)
	go vcSvc.Run(bgCtx)

	// Managed tunnel + webhook reconciler. The feature has NO env enable
	// flag — its config (enable/mode/hostname/token) lives in tunnel_config
	// and is managed entirely from the dashboard. The reconciler is always
	// wired (it re-registers webhook_mode=auto repos against whatever public
	// base is current). The tunnel is a feature, not a core dependency — a
	// start failure is logged, not fatal.
	tunnelCfgSvc := tunnelcfg.New(database, secSvc)
	webhookReconciler := tunnels.NewReconciler(grSvc, ghSvc, githubapi.New(), logger)
	tunnelMgr := tunnels.NewManager(tunnels.ManagerConfig{
		LocalPort: cfg.Port,
		Cloudflare: tunnels.CloudflareInfra{
			BinPath:     cfg.CloudflareBinPath,
			MetricsAddr: cfg.CloudflareMetricsAddr,
			StartupSec:  cfg.CloudflareStartupSec,
		},
		Ngrok: tunnels.NgrokInfra{
			BinPath:    cfg.NgrokBinPath,
			StartupSec: cfg.NgrokStartupSec,
		},
		BinManaged: cfg.TunnelBinManaged,
		BinDir:     cfg.TunnelBinDir,
	}, logger)
	// Re-register auto webhooks whenever the public URL changes (quick
	// tunnels mint a fresh URL on every restart). Reconcile is idempotent —
	// a PATCH to the same URL is harmless — so we also run it once on boot.
	tunnelMgr.OnURLChange(func(u string) {
		if _, rerr := webhookReconciler.Reconcile(bgCtx, u); rerr != nil {
			logger.Warn("webhook reconcile on URL change failed", "err", rerr)
		}
	})
	defer func() {
		ctx := shutdownCtx
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
		}
		if err := tunnelMgr.Stop(ctx); err != nil {
			logger.Warn("tunnel stop", "err", err)
		}
	}()
	// Apply the persisted tunnel config on boot. Non-fatal: a failure (bad
	// config, missing cloudflared binary) is surfaced via the dashboard.
	if resolved, rerr := tunnelCfgSvc.Resolve(context.Background()); rerr != nil {
		logger.Error("could not load tunnel config", "err", rerr)
	} else if resolved.Enabled {
		applyCtx, applyCancel := context.WithTimeout(context.Background(),
			time.Duration(cfg.CloudflareStartupSec)*time.Second+10*time.Second)
		if aerr := tunnelMgr.Apply(applyCtx, tunnels.Settings{
			Enabled:  resolved.Enabled,
			Provider: resolved.Provider,
			Mode:     resolved.Mode,
			Hostname: resolved.Hostname,
			Token:    resolved.Token,
		}); aerr != nil {
			logger.Error("managed tunnel failed to start (continuing)", "err", aerr)
		}
		applyCancel()
		go func() {
			if _, rrerr := webhookReconciler.Reconcile(bgCtx, tunnelMgr.URL()); rrerr != nil {
				logger.Warn("initial webhook reconcile failed", "err", rrerr)
			}
		}()
	}

	// Shared poll scheduler: one watcher driving git polling for every
	// polling-enabled repo, feeding the same bounded jobs queue. Exits
	// cleanly when bgCtx is cancelled in the shutdown branch.
	pollSvc := pollscheduler.New(
		grSvc, jobsSvc,
		cfg.PollSchedulerTick,
		int(cfg.DefaultPollInterval.Seconds()),
		int(cfg.MinPollInterval.Seconds()),
		logger,
	)
	go pollSvc.Run(bgCtx)

	handler := httpapi.NewRouter(httpapi.Deps{
		DB:                database,
		ServerVersion:     version,
		APIVersion:        apiVersion,
		Backend:           backend,
		EmbeddingModel:    cfg.EmbeddingModel,
		Logger:            logger,
		AuthDisabled:      cfg.AuthDisabled,
		Users:             usrSvc,
		Groups:            grpSvc,
		Sessions:          sessSvc,
		APIKeys:           akSvc,
		EmbeddingSvc:      embedSvc,
		VectorStore:       vsHolder,
		Indexer:           idx,
		RuntimeCfg:        rcfg,
		EmbeddingsCfg:     embedCfgStore,
		VersionCheck:      vcSvc,
		Workspaces:        wsSvc,
		GithubTokens:      ghSvc,
		GitRepos:          grSvc,
		WorkspaceProjects: wpSvc,
		Jobs:              jobsSvc,
		PublicBaseURL:     cfg.PublicBaseURL,
		Tunnel:            tunnelMgr,
		WebhookReconciler: webhookReconciler,
		TunnelConfig:      tunnelCfgSvc,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"addr", srv.Addr,
			"version", version,
			"embedding_model", cfg.EmbeddingModel,
		)
		// Public surface — emit as one structured line so JSON-aware log
		// shippers (Loki, GCP Cloud Logging) keep them grouped, plus a
		// plaintext banner to stderr for humans tailing the terminal.
		// `localhost` is good enough here: the server binds 0.0.0.0 by
		// default, but the operator is almost always reading this on the
		// same host they're about to click on.
		base := fmt.Sprintf("http://localhost:%d", cfg.Port)
		logger.Info("server ready",
			"dashboard", base+"/dashboard",
			"api_docs", base+"/docs",
			"openapi_spec", base+"/openapi.json",
			"health", base+"/health",
		)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  cix-server is ready 🟢")
		fmt.Fprintln(os.Stderr, "    Dashboard:    "+base+"/dashboard")
		fmt.Fprintln(os.Stderr, "    API docs:     "+base+"/docs")
		fmt.Fprintln(os.Stderr, "    OpenAPI spec: "+base+"/openapi.json")
		fmt.Fprintln(os.Stderr, "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Wait for SIGTERM/SIGINT or a server startup error.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("server: %w", err)
		}
		return nil
	}

	// M7 — single shared shutdown budget for HTTP drain + embeddings supervisor.
	// Previously each subsystem had its own 10s context, producing up to 20s
	// of total grace — which blows past Docker's default SIGKILL deadline.
	var cancel context.CancelFunc
	shutdownCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}
