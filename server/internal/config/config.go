// Package config loads runtime configuration from CIX_* environment variables.
// Variable names and semantics mirror api/app/config.py so the Go server can run
// alongside the Python server on the same host (differentiated by CIX_PORT).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings. Port defaults to 21847 — the same
// value the Docker images bake into ENV CIX_PORT and the same the
// docker-compose templates map on the host side. The earlier 8001
// default was a Python-FastAPI parallel-rollout carry-over; the Python
// backend was archived 2026-04 and the parity is no longer meaningful.
type Config struct {
	APIKey string
	// AuthDisabled, when true, makes the server skip the API-key check on
	// every endpoint. Off by default — must be turned on EXPLICITLY via
	// CIX_AUTH_DISABLED=true (and also requires CIX_API_KEY to be empty).
	// Replaces the older "empty API key = no auth" implicit bypass; the new
	// behaviour fails loud when CIX_API_KEY is missing without the flag.
	AuthDisabled            bool
	Port                    int
	EmbeddingModel          string
	ChromaPersistDir        string
	SQLitePath              string
	MaxFileSize             int
	ExcludedDirs            []string
	MaxEmbeddingConcurrency int
	EmbeddingQueueTimeout   int
	MaxChunkTokens          int
	// IndexEmbedBatchChunks packs chunks from consecutive files into one
	// embed call (cross-file batching) during repo indexing, cutting
	// round-trips on repos full of small files. 0 → one embed call per file.
	// Dashboard-overridable via runtimecfg. Env: CIX_INDEX_EMBED_BATCH_CHUNKS.
	IndexEmbedBatchChunks int

	// Phase 3 — llama-server sidecar configuration.
	GGUFPath          string // CIX_GGUF_PATH; absolute path. Empty = auto-resolve via cache / dev-fallback / HF download.
	GGUFCacheDir      string // CIX_GGUF_CACHE_DIR; where HF downloads land.
	LlamaBinDir       string // CIX_LLAMA_BIN_DIR; where llama-server + dylibs live. Default: <exe_dir>/llama.
	LlamaSocketPath   string // CIX_LLAMA_SOCKET; unix socket path. Default: <TMPDIR>/cix-llama-<pid>.sock.
	LlamaTransport    string // CIX_LLAMA_TRANSPORT; "unix" or "tcp".
	LlamaCtxSize      int    // CIX_LLAMA_CTX; defaults to MaxChunkTokens + 128 when unset.
	LlamaNGpuLayers   int    // CIX_N_GPU_LAYERS; -1 on darwin (Metal all layers), 0 elsewhere.
	LlamaNThreads     int    // CIX_LLAMA_THREADS; CPU thread count for llama-server (--threads). 0 = auto.
	LlamaBatchSize    int    // CIX_LLAMA_BATCH; llama-server logical batch size (-b). 0 = match LlamaCtxSize.
	LlamaStartupSec   int    // CIX_LLAMA_STARTUP_TIMEOUT; readiness probe ceiling in seconds.
	EmbeddingsEnabled bool   // CIX_EMBEDDINGS_ENABLED; test hook to bypass sidecar entirely.

	// BootstrapGGUFPath, when set, points at a .gguf file outside the cix
	// cache that should be imported on first boot — atomically copied into
	// `<GGUFCacheDir>/<safe_repo>/<basename>` so subsequent restarts use
	// the cache without re-downloading. Idempotent: if the target file
	// already exists, the import is skipped. Source: CIX_BOOTSTRAP_GGUF_PATH.
	//
	// Typical use: a Docker bind-mounting an existing HF cache file as
	// read-only (`/bootstrap/model.gguf:ro`) so the named cix-models volume
	// gets seeded once, then the bind can be removed.
	BootstrapGGUFPath string

	// EmbedIncludePath toggles a path+language+symbol preamble in front of
	// each chunk before sending it to the embedder. Improves retrieval for
	// queries whose terms appear in file paths (e.g. "server search handler"),
	// at the cost of requiring a full reindex when toggled. Source:
	// CIX_EMBED_INCLUDE_PATH (default true).
	EmbedIncludePath bool

	// Languages narrows the chunker's active language set. Empty / unset
	// activates all baked-in defaults (see chunker.defaultRegistry). Values
	// not present in the registry are warned-and-ignored at startup.
	// Source: CIX_LANGUAGES (comma-separated, case-insensitive).
	Languages []string

	// Dashboard auth bootstrap. When the users table is empty AND both of
	// these are set, main.go creates the first admin from these values.
	// The user is flagged must_change_password=1 so the env-supplied
	// password is never the long-term credential. Both ignored if the
	// users table already has rows. Sources: CIX_BOOTSTRAP_ADMIN_EMAIL +
	// CIX_BOOTSTRAP_ADMIN_PASSWORD.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	// Version-check feature — periodic GitHub poll that surfaces a
	// "newer release available" banner in the dashboard. Off entirely when
	// CIX_VERSION_CHECK_ENABLED=false (no outbound HTTP at all). Repo is
	// configurable so forks can point at their own GitHub project; the tag
	// filter is hardcoded to `server/v` since this is the server binary.
	// Sources: CIX_VERSION_CHECK_ENABLED (default true),
	// CIX_VERSION_CHECK_INTERVAL (default 6h, Go duration string),
	// CIX_VERSION_CHECK_REPO (default "dvcdsys/code-index").
	VersionCheckEnabled  bool
	VersionCheckInterval time.Duration
	VersionCheckRepo     string

	// SecretKey / SecretKeyFile control encryption-at-rest for the
	// github_tokens table (PATs used by clone_repo to authenticate
	// against private GitHub repos). Resolution order:
	//   1. CIX_SECRET_KEY (env var, hex or base64 32-byte value)
	//   2. CIX_SECRET_KEYFILE (env var, path to a 0600-perm key file)
	//   3. <DataDir>/.secret_key (auto-generated on first run; only used
	//      when workspaces are enabled and the operator hasn't explicitly
	//      pointed at a key)
	// PR1 keeps both fields here for documentation — the actual resolution
	// happens in internal/secrets.Open() which reads the env vars directly.
	SecretKey     string
	SecretKeyFile string

	// SecretsDataDir is the directory used as the auto-generated keyfile
	// destination when neither CIX_SECRET_KEY nor CIX_SECRET_KEYFILE is
	// set. Defaults to the SQLite parent directory so the generated key
	// lives alongside the encrypted data — losing one almost certainly
	// means losing both.
	SecretsDataDir string

	// WorkspacesDataDir is the base directory the worker pool clones GitHub
	// repositories under (each clone lives at
	// <WorkspacesDataDir>/repos/<path_hash>/). Defaults to
	// <SQLite parent>/repos. Source: CIX_REPOS_DIR (preferred), or the
	// legacy CIX_WORKSPACES_DATA_DIR alias. Point this at a dedicated volume
	// when cloned repos are large.
	WorkspacesDataDir string

	// WorkerConcurrency controls how many jobs goroutines drain the
	// queue at once. Clone+index is mostly IO; 2 is a sensible default
	// for a self-hosted single-node deployment. Source:
	// CIX_WORKER_CONCURRENCY (default 2).
	WorkerConcurrency int

	// PublicBaseURL is the externally-reachable URL of this server
	// (e.g. "https://cix.example.com"). Used to construct webhook
	// delivery URLs surfaced to operators on POST /workspaces/{id}/repos.
	// Optional — when empty, handlers return path-only URLs and trust
	// the operator to prepend their tunnel/proxy origin. Source:
	// CIX_PUBLIC_URL.
	PublicBaseURL string

	// Managed Tunnels — DEPLOYMENT INFRA ONLY. The feature itself has no
	// enable flag: whether a tunnel runs and how it's configured (mode,
	// hostname, token) lives in the DB (tunnel_config) and is managed
	// entirely from the dashboard. These env vars only describe where the
	// cloudflared binary lives and its tuning knobs, which are deployment
	// concerns the operator can't set from the UI.
	CloudflareBinPath     string // CIX_TUNNEL_CLOUDFLARE_BIN_PATH; default "cloudflared" (PATH) — images set /cloudflared
	CloudflareMetricsAddr string // CIX_TUNNEL_CLOUDFLARE_METRICS_ADDR; default 127.0.0.1:21848
	CloudflareStartupSec  int    // CIX_TUNNEL_CLOUDFLARE_STARTUP_TIMEOUT; readiness ceiling, default 30
	NgrokBinPath          string // CIX_TUNNEL_NGROK_BIN_PATH; default "ngrok" (PATH)
	NgrokStartupSec       int    // CIX_TUNNEL_NGROK_STARTUP_TIMEOUT; readiness ceiling, default 30

	// TunnelBinManaged enables installing/updating the provider binaries
	// from the dashboard (the server downloads them into TunnelBinDir). The
	// Docker images set this true — the bundled binaries can't be updated in
	// place (read-only image layer), so a writable managed dir on the data
	// volume lets operators update without rebuilding. Off by default for
	// local runs, where binaries come from the system (brew/apt/etc).
	// Source: CIX_TUNNEL_BIN_MANAGED.
	TunnelBinManaged bool
	// TunnelBinDir is the writable directory managed binaries are downloaded
	// into. Default: <SQLite dir>/tunnel-bin. Source: CIX_TUNNEL_BIN_DIR.
	TunnelBinDir string

	// DefaultPollInterval is the cadence used for polling-enabled repos
	// that don't set a per-repo interval. Cadence is measured from the end
	// of the last index run. Source: CIX_DEFAULT_POLL_INTERVAL (default 5m).
	DefaultPollInterval time.Duration

	// MinPollInterval is the floor applied to every effective poll interval
	// (per-repo or default), so an operator can't accidentally hammer
	// GitHub. Source: CIX_MIN_POLL_INTERVAL (default 60s).
	MinPollInterval time.Duration

	// PollSchedulerTick is how often the shared poll scheduler scans for
	// due repos. Source: CIX_POLL_SCHEDULER_TICK (default 30s).
	PollSchedulerTick time.Duration
}

// ModelSafeName returns the embedding model name normalised for use inside
// filesystem paths. Originally mirrored Settings.model_safe_name in the
// archived Python backend; now used ONLY to reconstruct the legacy
// per-model SQLite filename during the one-time DB adoption migration
// (internal/storage.AdoptLegacyModelDB). It no longer drives any live
// storage path — the system DB is model-independent and the vector store
// is namespaced by provider.StorageSlug(Provider.ID()).
//
// LEGACY-MIGRATION (remove next release): this and LegacyDynamicSQLitePath
// exist solely for the one-time storage-unification adoption. Once every
// deployment has booted on the unified layout, delete both along with
// storage.AdoptLegacyModelDB and its call in cmd/cix-server/main.go.
func (c *Config) ModelSafeName() string {
	s := strings.ReplaceAll(c.EmbeddingModel, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return strings.ToLower(s)
}

// LegacyDynamicSQLitePath reconstructs the OLD per-model SQLite filename
// (<base>_<ModelSafeName>.db) that pre-unification builds wrote to. It is
// used solely by the boot-time DB adoption migration to locate the file
// to adopt as the new model-independent system DB; no live code path
// should depend on it.
func (c *Config) LegacyDynamicSQLitePath() string {
	ext := filepath.Ext(c.SQLitePath)
	base := strings.TrimSuffix(c.SQLitePath, ext)
	return fmt.Sprintf("%s_%s%s", base, c.ModelSafeName(), ext)
}

// ChromaDirFor returns the on-disk vector-store directory for an embedding
// identity expressed as nested path components (see
// provider.Provider.StorageComponents): {kind, model-slug[, variant]}.
// ChromaPersistDir is the container; each component is its own directory
// level, so the provider kind can never collide with a model name that
// normalises to a kind-looking slug. Empty components → the bare
// container (callers guard against that).
func (c *Config) ChromaDirFor(components []string) string {
	return filepath.Join(append([]string{c.ChromaPersistDir}, components...)...)
}

// Load reads CIX_* environment variables and returns a populated Config.
// Returns an error if a numeric variable is present but unparseable.
//
// Defaults for SQLitePath / ChromaPersistDir resolve to ~/.cix/data/... so a
// fresh `make run` works without any env-file editing. Containers (CUDA + CPU
// Dockerfiles, portainer-stack.yml) override these explicitly with /data/...
// where /data is the persistent volume — no behaviour change in production.
func Load() (*Config, error) {
	c := &Config{
		APIKey:           getenv("CIX_API_KEY", ""),
		EmbeddingModel:   getenv("CIX_EMBEDDING_MODEL", "awhiteside/CodeRankEmbed-Q8_0-GGUF"),
		ChromaPersistDir: getenv("CIX_CHROMA_PERSIST_DIR", defaultChromaPersistDir()),
		SQLitePath:       getenv("CIX_SQLITE_PATH", defaultSQLitePath()),
	}

	authOff, err := getenvBool("CIX_AUTH_DISABLED", false)
	if err != nil {
		return nil, err
	}
	c.AuthDisabled = authOff

	port, err := getenvInt("CIX_PORT", 21847)
	if err != nil {
		return nil, err
	}
	c.Port = port

	maxFileSize, err := getenvInt("CIX_MAX_FILE_SIZE", 524288)
	if err != nil {
		return nil, err
	}
	c.MaxFileSize = maxFileSize

	maxConc, err := getenvInt("CIX_MAX_EMBEDDING_CONCURRENCY", 5)
	if err != nil {
		return nil, err
	}
	c.MaxEmbeddingConcurrency = maxConc

	queueTO, err := getenvInt("CIX_EMBEDDING_QUEUE_TIMEOUT", 300)
	if err != nil {
		return nil, err
	}
	c.EmbeddingQueueTimeout = queueTO

	idxBatch, err := getenvInt("CIX_INDEX_EMBED_BATCH_CHUNKS", 0)
	if err != nil {
		return nil, err
	}
	c.IndexEmbedBatchChunks = idxBatch

	maxChunk, err := getenvInt("CIX_MAX_CHUNK_TOKENS", 1500)
	if err != nil {
		return nil, err
	}
	c.MaxChunkTokens = maxChunk

	excluded := getenv("CIX_EXCLUDED_DIRS", "node_modules,.git,.venv,__pycache__,dist,build,.next,.cache,.DS_Store")
	for _, d := range strings.Split(excluded, ",") {
		if s := strings.TrimSpace(d); s != "" {
			c.ExcludedDirs = append(c.ExcludedDirs, s)
		}
	}

	// --- Phase 3 fields ---

	c.GGUFPath = getenv("CIX_GGUF_PATH", "")
	c.GGUFCacheDir = getenv("CIX_GGUF_CACHE_DIR", defaultGGUFCacheDir())
	c.LlamaBinDir = getenv("CIX_LLAMA_BIN_DIR", defaultLlamaBinDir())
	c.LlamaSocketPath = getenv("CIX_LLAMA_SOCKET", defaultLlamaSocketPath())
	c.LlamaTransport = strings.ToLower(getenv("CIX_LLAMA_TRANSPORT", "unix"))

	// Default to the model's full context window (2048 for CodeRankEmbed-Q8_0).
	// Using maxChunk+128 was too tight — code chunks can tokenize to more tokens
	// than their byte count suggests (code-optimized tokenizers are denser).
	llamaCtx, err := getenvInt("CIX_LLAMA_CTX", 2048)
	if err != nil {
		return nil, err
	}
	c.LlamaCtxSize = llamaCtx

	defaultGpu := 0
	if runtime.GOOS == "darwin" {
		defaultGpu = -1
	}
	gpuLayers, err := getenvInt("CIX_N_GPU_LAYERS", defaultGpu)
	if err != nil {
		return nil, err
	}
	c.LlamaNGpuLayers = gpuLayers

	// CIX_LLAMA_THREADS: when 0 (default), llama-server picks the count via
	// std::thread::hardware_concurrency. The dashboard runtime config can
	// override at runtime; an explicit env value still acts as the bootstrap
	// default the runtime layer falls back to.
	threads, err := getenvInt("CIX_LLAMA_THREADS", 0)
	if err != nil {
		return nil, err
	}
	c.LlamaNThreads = threads

	// CIX_LLAMA_BATCH: when 0 (default), the supervisor uses LlamaCtxSize so
	// a single chunk fits in one batch (matches the prior --ubatch-size=ctx
	// behaviour). Lower values trade throughput for memory.
	batch, err := getenvInt("CIX_LLAMA_BATCH", 0)
	if err != nil {
		return nil, err
	}
	c.LlamaBatchSize = batch

	startup, err := getenvInt("CIX_LLAMA_STARTUP_TIMEOUT", 60)
	if err != nil {
		return nil, err
	}
	c.LlamaStartupSec = startup

	enabled, err := getenvBool("CIX_EMBEDDINGS_ENABLED", true)
	if err != nil {
		return nil, err
	}
	c.EmbeddingsEnabled = enabled

	c.BootstrapGGUFPath = getenv("CIX_BOOTSTRAP_GGUF_PATH", "")

	includePath, err := getenvBool("CIX_EMBED_INCLUDE_PATH", true)
	if err != nil {
		return nil, err
	}
	c.EmbedIncludePath = includePath

	if langs := getenv("CIX_LANGUAGES", ""); langs != "" {
		for _, l := range strings.Split(langs, ",") {
			if s := strings.TrimSpace(l); s != "" {
				c.Languages = append(c.Languages, s)
			}
		}
	}

	c.BootstrapAdminEmail = getenv("CIX_BOOTSTRAP_ADMIN_EMAIL", "")
	c.BootstrapAdminPassword = getenv("CIX_BOOTSTRAP_ADMIN_PASSWORD", "")

	vcEnabled, err := getenvBool("CIX_VERSION_CHECK_ENABLED", true)
	if err != nil {
		return nil, err
	}
	c.VersionCheckEnabled = vcEnabled

	vcInterval, err := getenvDuration("CIX_VERSION_CHECK_INTERVAL", 6*time.Hour)
	if err != nil {
		return nil, err
	}
	c.VersionCheckInterval = vcInterval

	c.VersionCheckRepo = getenv("CIX_VERSION_CHECK_REPO", "dvcdsys/code-index")

	c.SecretKey = getenv("CIX_SECRET_KEY", "")
	c.SecretKeyFile = getenv("CIX_SECRET_KEYFILE", "")
	c.SecretsDataDir = getenv("CIX_SECRETS_DATA_DIR", filepath.Dir(c.SQLitePath))
	// CIX_REPOS_DIR is the clearly-named source; CIX_WORKSPACES_DATA_DIR is the
	// legacy alias kept for backward compatibility (this used to be a
	// workspace-only concept). First non-empty wins, else the default.
	c.WorkspacesDataDir = firstEnv(
		[]string{"CIX_REPOS_DIR", "CIX_WORKSPACES_DATA_DIR"},
		filepath.Join(filepath.Dir(c.SQLitePath), "repos"),
	)

	workerConc, err := getenvInt("CIX_WORKER_CONCURRENCY", 2)
	if err != nil {
		return nil, err
	}
	c.WorkerConcurrency = workerConc

	c.PublicBaseURL = strings.TrimSpace(getenv("CIX_PUBLIC_URL", ""))

	c.CloudflareBinPath = strings.TrimSpace(getenv("CIX_TUNNEL_CLOUDFLARE_BIN_PATH", "cloudflared"))
	c.CloudflareMetricsAddr = strings.TrimSpace(getenv("CIX_TUNNEL_CLOUDFLARE_METRICS_ADDR", "127.0.0.1:21848"))
	cfStartup, err := getenvInt("CIX_TUNNEL_CLOUDFLARE_STARTUP_TIMEOUT", 30)
	if err != nil {
		return nil, err
	}
	c.CloudflareStartupSec = cfStartup
	c.NgrokBinPath = strings.TrimSpace(getenv("CIX_TUNNEL_NGROK_BIN_PATH", "ngrok"))
	ngrokStartup, err := getenvInt("CIX_TUNNEL_NGROK_STARTUP_TIMEOUT", 30)
	if err != nil {
		return nil, err
	}
	c.NgrokStartupSec = ngrokStartup

	binManaged, err := getenvBool("CIX_TUNNEL_BIN_MANAGED", false)
	if err != nil {
		return nil, err
	}
	c.TunnelBinManaged = binManaged
	c.TunnelBinDir = strings.TrimSpace(getenv("CIX_TUNNEL_BIN_DIR", filepath.Join(filepath.Dir(c.SQLitePath), "tunnel-bin")))

	defaultPoll, err := getenvDuration("CIX_DEFAULT_POLL_INTERVAL", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	c.DefaultPollInterval = defaultPoll

	minPoll, err := getenvDuration("CIX_MIN_POLL_INTERVAL", 60*time.Second)
	if err != nil {
		return nil, err
	}
	c.MinPollInterval = minPoll

	pollTick, err := getenvDuration("CIX_POLL_SCHEDULER_TICK", 30*time.Second)
	if err != nil {
		return nil, err
	}
	c.PollSchedulerTick = pollTick

	return c, nil
}

// Validate sanity-checks the Phase 3 fields. It must be called after Load
// (main.go invokes it before constructing the embeddings service). Returns
// an error only for values that cannot be made safe with a default.
//
// PR-E removed the implicit `bench/results/reference_gguf_path.txt` dev
// fallback that used to silently stamp `cfg.GGUFPath` here. The dashboard's
// runtime-config UI now requires the operator to pick exactly one of:
// (a) HF repo ID → cix downloads to its own cache, or (b) absolute path →
// used as-is. There is no longer a "magic file the user didn't paste"
// resolution branch — it confused the UI ("No cached models" while the
// sidecar happily ran an out-of-cache GGUF) and made provenance opaque.
// Parity-gate developers must set CIX_GGUF_PATH explicitly (the test-gate
// Makefile target now reads bench/results/reference_gguf_path.txt and
// passes it through as an env var instead of relying on this code path).
func (c *Config) Validate() error {
	// NOTE: the old "CIX_API_KEY required unless CIX_AUTH_DISABLED" check is
	// gone. Auth gating moved into the bootstrap path in main.go: the server
	// now refuses to start when the users table is empty AND no
	// CIX_BOOTSTRAP_ADMIN_{EMAIL,PASSWORD} were supplied. CIX_API_KEY is now
	// optional (it's imported as a legacy api_keys row at first boot if set).
	// CIX_AUTH_DISABLED still works and bypasses auth wholesale.
	if c.LlamaTransport != "unix" && c.LlamaTransport != "tcp" {
		return fmt.Errorf("CIX_LLAMA_TRANSPORT=%q, must be 'unix' or 'tcp'", c.LlamaTransport)
	}
	if c.LlamaCtxSize <= 0 {
		return fmt.Errorf("CIX_LLAMA_CTX=%d, must be positive", c.LlamaCtxSize)
	}
	if c.LlamaStartupSec <= 0 {
		return fmt.Errorf("CIX_LLAMA_STARTUP_TIMEOUT=%d, must be positive", c.LlamaStartupSec)
	}
	return nil
}

// defaultDataDir returns the platform-specific root for runtime data
// (SQLite + chromem-go). Used to build defaults for CIX_SQLITE_PATH and
// CIX_CHROMA_PERSIST_DIR when neither env var is set.
//
// Order of resolution:
//  1. $CIX_DATA_DIR if set — explicit override
//  2. ~/.cix/data when $HOME is resolvable — typical dev case
//  3. /tmp/cix-data when $HOME is missing — fallback for headless / CI
//
// Container deployments (Dockerfile, Dockerfile.cuda, portainer-stack.yml)
// set CIX_SQLITE_PATH and CIX_CHROMA_PERSIST_DIR explicitly to /data/... so
// this function is never reached in production.
func defaultDataDir() string {
	if v := os.Getenv("CIX_DATA_DIR"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cix", "data")
	}
	return filepath.Join(os.TempDir(), "cix-data")
}

// defaultSQLitePath resolves the local SQLite database path under the
// platform data dir. This is the literal, model-independent system DB
// path the server opens (no model suffix is appended any more).
func defaultSQLitePath() string {
	return filepath.Join(defaultDataDir(), "sqlite", "projects.db")
}

// defaultChromaPersistDir resolves the local chromem-go persist directory.
func defaultChromaPersistDir() string {
	return filepath.Join(defaultDataDir(), "chroma")
}

// defaultGGUFCacheDir chooses a platform-appropriate location for downloaded
// GGUF files. We prefer XDG_CACHE_HOME when set (matches Linux conventions),
// then fall back to ~/Library/Caches on darwin and ~/.cache elsewhere.
func defaultGGUFCacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "cix", "models")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "cix-models")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "cix", "models")
	}
	return filepath.Join(home, ".cache", "cix", "models")
}

// defaultLlamaBinDir points at the `llama/` sibling directory next to the
// cix-server executable. This is the bundle layout produced by `make bundle`.
//
// n4 — the earlier comment claimed we fall back to "./llama" on symlink
// resolution failure; actually we fall back to `<exe_dir>/llama` in that case
// too (the pre-symlink exe path still has a valid Dir). The only truly
// relative "llama" fallback is when os.Executable() itself fails (extremely
// rare, usually during `go run`).
func defaultLlamaBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "llama"
	}
	// Resolve symlinks so nested invocations (e.g. installers putting
	// cix-server into /usr/local/bin pointing at /opt/cix/bin) still find
	// the bundled llama/ directory next to the real binary.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "llama")
}

// defaultLlamaSocketPath picks a short, unique socket path in TMPDIR.
// macOS limits sun_path to 104 bytes — including NUL — so we keep the path
// short. PID-based naming avoids collisions across concurrent test runs.
func defaultLlamaSocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("cix-llama-%d.sock", os.Getpid()))
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// firstEnv returns the value of the first set (present, possibly empty) env var
// in keys, or def when none are set. Used for vars with a preferred name plus a
// legacy alias.
func firstEnv(keys []string, def string) string {
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
	}
	return def
}

func getenvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("env %s: %w", key, err)
	}
	return n, nil
}

func getenvBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("env %s: %w", key, err)
	}
	return b, nil
}

func getenvDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("env %s: %w", key, err)
	}
	return d, nil
}
