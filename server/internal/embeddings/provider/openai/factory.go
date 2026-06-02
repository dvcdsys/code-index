package openai

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

const defaultAPIKeyEnv = "CIX_OPENAI_API_KEY"

type factory struct{}

func (factory) Kind() string { return provider.KindOpenAI }

func (factory) SchemaJSON() []byte {
	s := provider.ConfigSchema{
		Fields: []provider.ConfigField{
			{Name: "base_url", Label: "Base URL", Kind: "string", Required: true, Default: "https://api.openai.com", Description: "Server origin without /v1 suffix."},
			{Name: "model", Label: "Model", Kind: "string", Required: true, Default: "text-embedding-3-small"},
			{Name: "api_key_env", Label: "API key env var", Kind: "secret-env", Required: true, Default: defaultAPIKeyEnv, Description: "Server-side env var name that holds the API key."},
			{Name: "dimensions", Label: "Dimensions", Kind: "int", Description: "Optional Matryoshka shrink (text-embedding-3*)."},
		},
	}
	b, _ := json.Marshal(s)
	return b
}

func (factory) SecretEnvVars() []string { return []string{defaultAPIKeyEnv} }

func (factory) Build(cfg []byte, secrets provider.SecretLookup, logger *slog.Logger) (provider.Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg) == 0 {
		return nil, fmt.Errorf("openai: empty config")
	}
	var c Config
	if err := json.Unmarshal(cfg, &c); err != nil {
		return nil, fmt.Errorf("openai: unmarshal config: %w", err)
	}
	if c.Model == "" {
		return nil, fmt.Errorf("openai: model is required")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com"
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = defaultAPIKeyEnv
	}
	return New(c, secrets, logger), nil
}

func init() {
	provider.Register(factory{})
}
