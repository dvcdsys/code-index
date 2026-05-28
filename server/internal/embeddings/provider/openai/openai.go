// Package openai implements provider.Provider against any
// OpenAI-compatible /v1/embeddings endpoint (OpenAI proper, vLLM,
// TEI, LocalAI, Ollama's own openai-compat endpoint, …). All
// providers share the same request/response shape; the differences
// are only the base URL and which API key env var to read.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// Config is the persisted shape of the openai provider's config blob.
type Config struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// maxBatchSize caps how many inputs we send in a single
// /v1/embeddings POST. OpenAI proper accepts up to 2048 inputs
// per request for text-embedding-3-*; self-hosted clones (vLLM,
// TEI, LocalAI) may be tighter but rarely lower than that. The
// split is transparent to callers — same queue slot, sequential
// sub-batches.
const maxBatchSize = 2048

// Provider is the openai-compatible HTTP client wrapped behind the
// provider.Provider interface.
type Provider struct {
	cfg     Config
	logger  *slog.Logger
	secrets provider.SecretLookup
	http    *http.Client
}

// New constructs the Provider. Does not contact the endpoint — call
// Start to perform a one-shot connect test.
func New(cfg Config, secrets provider.SecretLookup, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	// Normalise away a trailing slash so url building (BaseURL +
	// "/v1/embeddings") never produces a double slash, which stricter
	// OpenAI-compatible servers (vLLM/TEI behind a proxy) can 404 on.
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Provider{
		cfg:     cfg,
		logger:  logger,
		secrets: secrets,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *Provider) Kind() string { return provider.KindOpenAI }

// ID is "openai:{model}[:{dim}]". The dimension is part of the ID only
// when explicitly configured (Matryoshka shrink via the `dimensions`
// param) — otherwise different model versions are distinguished by
// the model name alone.
func (p *Provider) ID() string {
	if p.cfg.Dimensions > 0 {
		return "openai:" + p.cfg.Model + ":" + strconv.Itoa(p.cfg.Dimensions)
	}
	return "openai:" + p.cfg.Model
}

func (p *Provider) Dimension() int         { return p.cfg.Dimensions }
func (p *Provider) SupportsTokenize() bool { return false }

// Start runs a one-shot connect test: embed a single short string.
// Surfaces auth / network errors before the provider is wired into
// the request path.
func (p *Provider) Start(ctx context.Context) error {
	if p.cfg.BaseURL == "" {
		return errors.New("openai: base_url is required")
	}
	if p.cfg.Model == "" {
		return errors.New("openai: model is required")
	}
	if _, ok := p.apiKey(); !ok {
		return fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, p.cfg.APIKeyEnv)
	}
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := p.embed(testCtx, []string{"ping"})
	if err != nil {
		return fmt.Errorf("openai: connect test failed: %w", err)
	}
	return nil
}

// Stop is a no-op — the provider holds no managed process or
// long-lived connection.
func (p *Provider) Stop(_ context.Context) error { return nil }

// Ready returns nil if the API key is set. We do NOT ping the remote
// on every Ready call (which the /status footer polls every 30s) —
// remote outages surface as real embed failures with diagnostics; an
// always-green footer dot for HTTP-only providers matches the
// dashboard's documented behaviour.
func (p *Provider) Ready(_ context.Context) error {
	if _, ok := p.apiKey(); !ok {
		return fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, p.cfg.APIKeyEnv)
	}
	return nil
}

func (p *Provider) Status() provider.Status {
	st := provider.Status{
		State:          provider.StateRemote,
		ManagesProcess: false,
		Model:          p.cfg.Model,
	}
	if _, ok := p.apiKey(); !ok {
		st.State = provider.StateFailed
		st.LastError = "API key env var " + p.cfg.APIKeyEnv + " is not set"
	}
	return st
}

// EmbedQuery is a pass-through to EmbedDocuments — generic
// OpenAI-compatible servers have no query/document differentiation.
func (p *Provider) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := p.embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (p *Provider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) <= maxBatchSize {
		return p.embed(ctx, texts)
	}
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		part, err := p.embed(ctx, texts[i:end])
		if err != nil {
			return nil, fmt.Errorf("openai: sub-batch [%d:%d]: %w", i, end, err)
		}
		out = append(out, part...)
	}
	return out, nil
}

// TokenizeAndEmbed falls back to EmbedDocuments — generic openai-style
// servers expose neither /tokenize nor reliable input token counts.
// The indexer's chunking step pre-truncates inputs to a conservative
// limit when SupportsTokenize() is false.
func (p *Provider) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	return p.EmbedDocuments(ctx, texts)
}

type embedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponseItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embedResponse struct {
	Data []embedResponseItem `json:"data"`
}

// embed POSTs /v1/embeddings and returns vectors in input order.
func (p *Provider) embed(ctx context.Context, texts []string) ([][]float32, error) {
	key, ok := p.apiKey()
	if !ok {
		return nil, fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, p.cfg.APIKeyEnv)
	}
	body, err := json.Marshal(embedRequest{
		Input:      texts,
		Model:      p.cfg.Model,
		Dimensions: p.cfg.Dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}
	url := p.cfg.BaseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(snippet))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("openai: got %d vectors for %d inputs", len(er.Data), len(texts))
	}
	out := make([][]float32, len(er.Data))
	for _, item := range er.Data {
		if item.Index < 0 || item.Index >= len(out) {
			return nil, fmt.Errorf("openai: out-of-range index %d", item.Index)
		}
		out[item.Index] = item.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("openai: missing vector at index %d", i)
		}
	}
	return out, nil
}

func (p *Provider) apiKey() (string, bool) {
	if p.secrets == nil {
		return "", false
	}
	if p.cfg.APIKeyEnv == "" {
		return "", false
	}
	v, ok := p.secrets(p.cfg.APIKeyEnv)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
