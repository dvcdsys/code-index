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
	"github.com/dvcdsys/code-index/server/internal/githubapi"
	"github.com/dvcdsys/code-index/server/internal/githubtokens"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/gitrepos"
	"github.com/dvcdsys/code-index/server/internal/httpapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/pollscheduler"
	"github.com/dvcdsys/code-index/server/internal/repojobs"
	"github.com/dvcdsys/code-index/server/internal/runtimecfg"
	"github.com/dvcdsys/code-index/server/internal/secrets"
	"github.com/dvcdsys/code-index/server/internal/sessions"
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

	dbPath := cfg.DynamicSQLitePath()
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
	// DynamicSQLitePath embeds ModelSafeName(); if the dashboard switched the
	// model, the storage path resolved a moment ago is for the OLD model. The
	// already-opened DB is still correct (it's the OLD model's state) but the
	// chroma vectorstore opened below needs to honour the NEW model. Recompute
	// dbPath only matters if we want to re-open under the new model — for PR-E
	// we deliberately keep the old DB so historical projects keep their
	// indexed_with_model and the dashboard can show the drift. Sidecar +
	// vectorstore use the new model.

	// Embeddings service. When disabled we still build the value so router
	// wiring stays consistent — Service methods return ErrDisabled in that case.
	// Startup is bounded by a context derived from LlamaStartupSec plus a grace
	// window for the HF download path on cold cache.
	startupCtx, startupCancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.LlamaStartupSec)*time.Second+30*time.Second)
	embedSvc, err := embeddings.New(startupCtx, cfg, logger)
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

	// Detect and back up a legacy ChromaDB layout left by the Python server.
	if backed, bErr := vectorstore.DetectLegacyAndBackup(cfg.DynamicChromaPersistDir()); bErr != nil {
		logger.Warn("could not back up legacy chroma dir", "err", bErr)
	} else if backed {
		logger.Warn("legacy chroma layout detected — backed up; re-run cix init to reindex")
	}

	// Vector store (chromem-go). Lives under the dynamic chroma persist dir so
	// the path includes the model-safe name, matching Python parity.
	vs, err := vectorstore.Open(cfg.DynamicChromaPersistDir())
	if err != nil {
		return fmt.Errorf("open vectorstore: %w", err)
	}

	idx := indexer.New(database, vs, embedSvc, logger)
	idx.SetEmbedIncludePath(cfg.EmbedIncludePath)
	// PR-E — record the active embedding model on every indexed project so the
	// dashboard can highlight stale vectors when the runtime model changes.
	idx.SetEmbeddingModel(cfg.EmbeddingModel)
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
		VectorStore:                vs,
		DataDir:                    cfg.WorkspacesDataDir,
		Logger:                     logger,
		DefaultPollIntervalSeconds: int(cfg.DefaultPollInterval.Seconds()),
		MinPollIntervalSeconds:     int(cfg.MinPollInterval.Seconds()),
	})
	jobsSvc.Start(context.Background())
	defer func() {
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
		VectorStore:       vs,
		Indexer:           idx,
		RuntimeCfg:        rcfg,
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
