package voyage

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

const defaultAPIKeyEnv = "CIX_VOYAGE_API_KEY"

type factory struct{}

func (factory) Kind() string { return provider.KindVoyage }

func (factory) SchemaJSON() []byte {
	s := provider.ConfigSchema{
		Fields: []provider.ConfigField{
			{
				Name: "model", Label: "Model", Kind: "enum", Required: true,
				Enum:    []string{"voyage-code-3", "voyage-3-large", "voyage-3", "voyage-3-lite", "voyage-code-2"},
				Default: "voyage-code-3",
			},
			{
				Name: "output_dimension", Label: "Output dimension", Kind: "enum",
				Enum: []string{"256", "512", "1024", "2048"}, Default: "1024",
				Description: "Matryoshka shrink. Changing this triggers a full reindex.",
			},
			{
				Name: "output_dtype", Label: "Output dtype", Kind: "enum",
				Enum: []string{DtypeFloat, DtypeInt8}, Default: DtypeFloat,
				Description: "int8 is dequantized to float32 on the server side.",
			},
			{Name: "truncation", Label: "Truncate over-length input", Kind: "bool", Default: true},
			{Name: "api_key_env", Label: "API key env var", Kind: "secret-env", Required: true, Default: defaultAPIKeyEnv},
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
		return nil, fmt.Errorf("voyage: empty config")
	}
	var c Config
	if err := json.Unmarshal(cfg, &c); err != nil {
		return nil, fmt.Errorf("voyage: unmarshal config: %w", err)
	}
	if c.Model == "" {
		return nil, fmt.Errorf("voyage: model is required")
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = defaultAPIKeyEnv
	}
	if c.OutputDtype == "" {
		c.OutputDtype = DtypeFloat
	}
	return New(c, secrets, logger), nil
}

func init() {
	provider.Register(factory{})
}
