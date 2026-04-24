package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear any CIX_* that may leak in from the shell. We register t.Setenv
	// first for each key so the test-scoped cleanup restores pre-test values,
	// then force-Unsetenv so Load() sees no var and picks its default.
	unsetAll(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 8001 {
		t.Errorf("Port default = %d, want 8001", c.Port)
	}
	if c.EmbeddingModel != "awhiteside/CodeRankEmbed-Q8_0-GGUF" {
		t.Errorf("EmbeddingModel default = %q", c.EmbeddingModel)
	}
	if c.MaxChunkTokens != 1500 {
		t.Errorf("MaxChunkTokens default = %d", c.MaxChunkTokens)
	}
	if c.MaxFileSize != 524288 {
		t.Errorf("MaxFileSize default = %d", c.MaxFileSize)
	}
	if len(c.ExcludedDirs) == 0 || c.ExcludedDirs[0] != "node_modules" {
		t.Errorf("ExcludedDirs default unexpected: %v", c.ExcludedDirs)
	}
}

func TestLoadOverrides(t *testing.T) {
	unsetAll(t)
	// The unsetAll above wipes env before Setenv registers restore callbacks.
	// Subsequent t.Setenv calls both set the value for this test and register
	// proper cleanups.
	t.Setenv("CIX_PORT", "9002")
	t.Setenv("CIX_API_KEY", "secret")
	t.Setenv("CIX_EMBEDDING_MODEL", "test/Model-Name")
	t.Setenv("CIX_SQLITE_PATH", "/tmp/test.db")
	t.Setenv("CIX_EXCLUDED_DIRS", "a, b ,c")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 9002 {
		t.Errorf("Port = %d, want 9002", c.Port)
	}
	if c.APIKey != "secret" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
	if got, want := len(c.ExcludedDirs), 3; got != want {
		t.Fatalf("ExcludedDirs len = %d, want %d (%v)", got, want, c.ExcludedDirs)
	}
	if c.ExcludedDirs[1] != "b" {
		t.Errorf("ExcludedDirs[1] = %q, want 'b'", c.ExcludedDirs[1])
	}

	if got := c.ModelSafeName(); got != "test_model_name" {
		t.Errorf("ModelSafeName = %q", got)
	}
	if got := c.DynamicSQLitePath(); got != "/tmp/test_test_model_name.db" {
		t.Errorf("DynamicSQLitePath = %q", got)
	}
}

func TestLoadPhase3Defaults(t *testing.T) {
	unsetAll(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LlamaTransport != "unix" {
		t.Errorf("LlamaTransport default = %q, want unix", c.LlamaTransport)
	}
	if c.LlamaCtxSize != 2048 {
		t.Errorf("LlamaCtxSize default = %d, want 2048", c.LlamaCtxSize)
	}
	if c.LlamaStartupSec != 60 {
		t.Errorf("LlamaStartupSec default = %d, want 60", c.LlamaStartupSec)
	}
	if !c.EmbeddingsEnabled {
		t.Errorf("EmbeddingsEnabled default = false, want true")
	}
	// GPU layers default depends on GOOS. On darwin we expect -1 (Metal all);
	// on any other platform 0. Either way the value must be set explicitly.
	if c.LlamaNGpuLayers != -1 && c.LlamaNGpuLayers != 0 {
		t.Errorf("LlamaNGpuLayers default = %d, expected -1 or 0", c.LlamaNGpuLayers)
	}
	if c.GGUFCacheDir == "" {
		t.Error("GGUFCacheDir default is empty")
	}
}

func TestValidateBadTransport(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_LLAMA_TRANSPORT", "udp")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate: expected error for bogus transport")
	}
}

func TestValidateBadCtx(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_LLAMA_CTX", "0")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate: expected error for non-positive ctx")
	}
}

func TestLoadEmbeddingsEnabledToggle(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_EMBEDDINGS_ENABLED", "false")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.EmbeddingsEnabled {
		t.Error("EmbeddingsEnabled should be false when env set to false")
	}
}

func TestLoadBadInt(t *testing.T) {
	unsetAll(t)
	t.Setenv("CIX_PORT", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad CIX_PORT")
	}
}

// unsetAll wipes every CIX_* env var so Load() exercises its defaults.
// We first call t.Setenv to register a per-test restore hook, then
// os.Unsetenv so LookupEnv returns ok=false inside the test body.
func unsetAll(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CIX_API_KEY", "CIX_PORT", "CIX_EMBEDDING_MODEL",
		"CIX_CHROMA_PERSIST_DIR", "CIX_SQLITE_PATH", "CIX_MAX_FILE_SIZE",
		"CIX_EXCLUDED_DIRS", "CIX_MAX_EMBEDDING_CONCURRENCY",
		"CIX_EMBEDDING_QUEUE_TIMEOUT", "CIX_MAX_CHUNK_TOKENS",
		// Phase 3 additions — kept in the same helper so new tests cannot
		// accidentally inherit values from a developer shell.
		"CIX_GGUF_PATH", "CIX_GGUF_CACHE_DIR", "CIX_LLAMA_BIN_DIR",
		"CIX_LLAMA_SOCKET", "CIX_LLAMA_TRANSPORT", "CIX_LLAMA_CTX",
		"CIX_N_GPU_LAYERS", "CIX_LLAMA_STARTUP_TIMEOUT", "CIX_EMBEDDINGS_ENABLED",
	} {
		t.Setenv(k, "sentinel")
		osUnsetenv(k)
	}
}
