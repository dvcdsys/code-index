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
	"syscall"
	"time"

	"github.com/dvcdsys/code-index/server/internal/chunker"
	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/httpapi"
	"github.com/dvcdsys/code-index/server/internal/indexer"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
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

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// Validate already refused an empty API key without CIX_AUTH_DISABLED=true,
	// so reaching this point means either auth is properly configured or the
	// operator explicitly opted out. Log loudly when we are about to serve
	// without auth so it shows up in container logs / Portainer.
	if cfg.AuthDisabled {
		logger.Warn("auth disabled (CIX_AUTH_DISABLED=true) — every endpoint is reachable without an API key")
	}

	chunker.Configure(cfg.Languages)
	logger.Info("chunker languages configured", "active", chunker.SupportedLanguages())

	dbPath := cfg.DynamicSQLitePath()
	logger.Info("opening database", "path", dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("db close", "err", err)
		}
	}()

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
	if cfg.EmbedIncludePath {
		logger.Info("embedding format: path-aware preamble enabled (CIX_EMBED_INCLUDE_PATH=true) — full reindex required if upgrading")
	}
	// Stop housekeeping goroutines during shutdown so sessionTTL timers do not
	// leak for up to 1h past shutdown. m8 fix.
	defer idx.Shutdown()

	handler := httpapi.NewRouter(httpapi.Deps{
		DB:             database,
		ServerVersion:  version,
		APIVersion:     apiVersion,
		Backend:        backend,
		EmbeddingModel: cfg.EmbeddingModel,
		Logger:         logger,
		APIKey:         cfg.APIKey,
		AuthDisabled:   cfg.AuthDisabled,
		EmbeddingSvc:   embedSvc,
		VectorStore:    vs,
		Indexer:        idx,
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
