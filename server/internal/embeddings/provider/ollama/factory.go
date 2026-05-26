package ollama

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// factory is the provider.Factory implementation for ollama. The
// init() block below registers it in the global registry so the
// Service can build an ollama provider purely by kind string.
type factory struct{}

func (factory) Kind() string { return provider.KindOllama }

// SchemaJSON describes the editable ollama fields for the dashboard.
// Sidecar-internal knobs (BinDir, SocketPath, Transport, CacheDir,
// BootstrapPath) are populated from env at bootstrap and are not
// surfaced for runtime edit — they are deployment-level concerns.
func (factory) SchemaJSON() []byte {
	schema := provider.ConfigSchema{
		Fields: []provider.ConfigField{
			{
				Name:        "model",
				Label:       "Embedding model",
				Kind:        "string",
				Required:    true,
				Description: "HuggingFace repo id (owner/repo) or an absolute path to a .gguf file.",
			},
			{
				Name:        "gguf_path",
				Label:       "GGUF path override",
				Kind:        "string",
				Description: "Optional absolute path that takes precedence over Model (CIX_GGUF_PATH).",
			},
			{
				Name:    "ctx_size",
				Label:   "Context size",
				Kind:    "int",
				Default: 2048,
			},
			{
				Name:        "n_gpu_layers",
				Label:       "GPU layers",
				Kind:        "int",
				Description: "-1 = all on Metal, 0 = CPU only.",
			},
			{
				Name:        "n_threads",
				Label:       "Threads",
				Kind:        "int",
				Description: "0 = let llama-server auto-detect.",
			},
			{
				Name:        "batch_size",
				Label:       "Batch size",
				Kind:        "int",
				Description: "0 = match context size.",
			},
		},
	}
	// Schema is small and stable; build once per call so the registry
	// doesn't have to hold long-lived buffers.
	b, _ := json.Marshal(schema)
	return b
}

// SecretEnvVars: ollama has no remote API key.
func (factory) SecretEnvVars() []string { return nil }

// Build unmarshals cfg into Config and constructs a Provider. Does
// not call Start; the caller (Service.SwitchProvider) sequences the
// start so it can roll back to the previous provider on failure.
func (factory) Build(cfg []byte, _ provider.SecretLookup, logger *slog.Logger) (provider.Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg) == 0 {
		return nil, fmt.Errorf("ollama: empty config")
	}
	var c Config
	if err := json.Unmarshal(cfg, &c); err != nil {
		return nil, fmt.Errorf("ollama: unmarshal config: %w", err)
	}
	if c.Model == "" {
		return nil, fmt.Errorf("ollama: model is required")
	}
	if c.Transport == "" {
		c.Transport = "unix"
	}
	if c.CtxSize == 0 {
		c.CtxSize = 2048
	}
	if c.StartupSec == 0 {
		c.StartupSec = 60
	}
	return New(c, logger), nil
}

func init() {
	provider.Register(factory{})
}
