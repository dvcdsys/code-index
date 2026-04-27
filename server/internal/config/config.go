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
)

// Config holds all runtime settings. Defaults match api/app/config.py except
// for Port, which is 8001 by default so the Go server does not collide with
// the Python server (21847) during parallel PoC rollout.
type Config struct {
	APIKey                  string
	Port                    int
	EmbeddingModel          string
	ChromaPersistDir        string
	SQLitePath              string
	MaxFileSize             int
	ExcludedDirs            []string
	MaxEmbeddingConcurrency int
	EmbeddingQueueTimeout   int
	MaxChunkTokens          int

	// Phase 3 — llama-server sidecar configuration.
	GGUFPath          string // CIX_GGUF_PATH; absolute path. Empty = auto-resolve via cache / dev-fallback / HF download.
	GGUFCacheDir      string // CIX_GGUF_CACHE_DIR; where HF downloads land.
	LlamaBinDir       string // CIX_LLAMA_BIN_DIR; where llama-server + dylibs live. Default: <exe_dir>/llama.
	LlamaSocketPath   string // CIX_LLAMA_SOCKET; unix socket path. Default: <TMPDIR>/cix-llama-<pid>.sock.
	LlamaTransport    string // CIX_LLAMA_TRANSPORT; "unix" or "tcp".
	LlamaCtxSize      int    // CIX_LLAMA_CTX; defaults to MaxChunkTokens + 128 when unset.
	LlamaNGpuLayers   int    // CIX_N_GPU_LAYERS; -1 on darwin (Metal all layers), 0 elsewhere.
	LlamaStartupSec   int    // CIX_LLAMA_STARTUP_TIMEOUT; readiness probe ceiling in seconds.
	EmbeddingsEnabled bool   // CIX_EMBEDDINGS_ENABLED; test hook to bypass sidecar entirely.

	// Languages narrows the chunker's active language set. Empty / unset
	// activates all baked-in defaults (see chunker.defaultRegistry). Values
	// not present in the registry are warned-and-ignored at startup.
	// Source: CIX_LANGUAGES (comma-separated, case-insensitive).
	Languages []string
}

// ModelSafeName returns the embedding model name normalised for use inside
// filesystem paths. Matches Settings.model_safe_name in api/app/config.py.
func (c *Config) ModelSafeName() string {
	s := strings.ReplaceAll(c.EmbeddingModel, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return strings.ToLower(s)
}

// DynamicSQLitePath returns the SQLite path with the model-safe name suffixed
// before the extension. Matches Settings.dynamic_sqlite_path in Python.
func (c *Config) DynamicSQLitePath() string {
	ext := filepath.Ext(c.SQLitePath)
	base := strings.TrimSuffix(c.SQLitePath, ext)
	return fmt.Sprintf("%s_%s%s", base, c.ModelSafeName(), ext)
}

// DynamicChromaPersistDir matches Settings.dynamic_chroma_persist_dir.
func (c *Config) DynamicChromaPersistDir() string {
	return fmt.Sprintf("%s_%s", c.ChromaPersistDir, c.ModelSafeName())
}

// Load reads CIX_* environment variables and returns a populated Config.
// Returns an error if a numeric variable is present but unparseable.
func Load() (*Config, error) {
	c := &Config{
		APIKey:           getenv("CIX_API_KEY", ""),
		EmbeddingModel:   getenv("CIX_EMBEDDING_MODEL", "awhiteside/CodeRankEmbed-Q8_0-GGUF"),
		ChromaPersistDir: getenv("CIX_CHROMA_PERSIST_DIR", "/data/chroma"),
		SQLitePath:       getenv("CIX_SQLITE_PATH", "/data/sqlite/projects.db"),
	}

	port, err := getenvInt("CIX_PORT", 8001)
	if err != nil {
		return nil, err
	}
	c.Port = port

	maxFileSize, err := getenvInt("CIX_MAX_FILE_SIZE", 524288)
	if err != nil {
		return nil, err
	}
	c.MaxFileSize = maxFileSize

	maxConc, err := getenvInt("CIX_MAX_EMBEDDING_CONCURRENCY", 1)
	if err != nil {
		return nil, err
	}
	c.MaxEmbeddingConcurrency = maxConc

	queueTO, err := getenvInt("CIX_EMBEDDING_QUEUE_TIMEOUT", 300)
	if err != nil {
		return nil, err
	}
	c.EmbeddingQueueTimeout = queueTO

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

	if langs := getenv("CIX_LANGUAGES", ""); langs != "" {
		for _, l := range strings.Split(langs, ",") {
			if s := strings.TrimSpace(l); s != "" {
				c.Languages = append(c.Languages, s)
			}
		}
	}

	return c, nil
}

// Validate sanity-checks the Phase 3 fields and applies the dev-fallback rule
// for CIX_GGUF_PATH. It must be called after Load (main.go invokes it before
// constructing the embeddings service). Returns an error only for values that
// cannot be made safe with a default.
//
// Dev fallback: when EmbeddingsEnabled is true and GGUFPath is empty, we look
// for `bench/results/reference_gguf_path.txt` relative to the CWD. If present,
// we use its contents as the GGUF path so the parity gate works without the
// developer having to set an env var. This is a deliberate PoC ergonomic —
// it is silent when the file is missing and the HF downloader picks up.
func (c *Config) Validate() error {
	if c.LlamaTransport != "unix" && c.LlamaTransport != "tcp" {
		return fmt.Errorf("CIX_LLAMA_TRANSPORT=%q, must be 'unix' or 'tcp'", c.LlamaTransport)
	}
	if c.LlamaCtxSize <= 0 {
		return fmt.Errorf("CIX_LLAMA_CTX=%d, must be positive", c.LlamaCtxSize)
	}
	if c.LlamaStartupSec <= 0 {
		return fmt.Errorf("CIX_LLAMA_STARTUP_TIMEOUT=%d, must be positive", c.LlamaStartupSec)
	}
	if c.EmbeddingsEnabled && c.GGUFPath == "" {
		if path := readDevFallbackGGUF(); path != "" {
			c.GGUFPath = path
		}
	}
	return nil
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

// readDevFallbackGGUF reads bench/results/reference_gguf_path.txt relative to
// the CWD if it exists. Empty return means "no fallback available"; callers
// then rely on HF download.
func readDevFallbackGGUF() string {
	const refFile = "bench/results/reference_gguf_path.txt"
	data, err := os.ReadFile(refFile)
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return ""
	}
	// Only use the fallback when the file actually exists on disk. Otherwise
	// we'd stamp a non-existent path and the supervisor would fail later with
	// a less friendly error.
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
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
