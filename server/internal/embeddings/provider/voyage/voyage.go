// Package voyage implements provider.Provider against the Voyage AI
// embeddings API (https://api.voyageai.com/v1/embeddings).
//
// Voyage diverges from the OpenAI shape in three ways we care about:
//   - input_type: "query" vs "document" — required for retrieval
//     quality. EmbedQuery sends "query"; EmbedDocuments sends
//     "document".
//   - output_dimension: Matryoshka shrink, configured by the admin
//     (256/512/1024/2048). Part of Provider.ID() because changing it
//     invalidates the existing index.
//   - output_dtype: float|int8 (binary/ubinary are out of scope —
//     chromem-go has no hamming search). For int8 the server returns
//     a list of integers per dimension; we dequantize to float32 in
//     this package before returning vectors to the vector store.
//
// usage. Voyage omits prompt_tokens from the usage object — only
// total_tokens is present. The response struct therefore has its own
// shape distinct from OpenAI's.
package voyage

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
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// DefaultBaseURL is the public Voyage AI embeddings endpoint origin.
const DefaultBaseURL = "https://api.voyageai.com"

// Supported dtypes. binary/ubinary intentionally absent for v1.
const (
	DtypeFloat = "float"
	DtypeInt8  = "int8"
)

// maxBatchSize caps how many inputs we send in a single
// /v1/embeddings POST. Voyage's per-request limits depend on the
// model — voyage-code-3 and voyage-code-2 cap at 128; voyage-3*
// models accept up to 1000. We pick the conservative floor so a
// single constant works across all supported models. EmbedDocuments
// transparently splits oversize inputs into sequential sub-batches
// under the same queue slot so the caller never sees 422 Request
// Too Large from a large file's chunks.
const maxBatchSize = 128

// Config is the persisted shape of the voyage provider's config blob.
type Config struct {
	BaseURL         string `json:"base_url,omitempty"`
	APIKeyEnv       string `json:"api_key_env"`
	Model           string `json:"model"`
	OutputDimension int    `json:"output_dimension,omitempty"`
	OutputDtype     string `json:"output_dtype,omitempty"`
	Truncation      bool   `json:"truncation,omitempty"`
}

// Provider is the Voyage HTTP client.
type Provider struct {
	cfg     Config
	logger  *slog.Logger
	secrets provider.SecretLookup
	http    *http.Client
}

// New constructs the Provider. Does not contact the endpoint.
func New(cfg Config, secrets provider.SecretLookup, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.OutputDtype == "" {
		cfg.OutputDtype = DtypeFloat
	}
	return &Provider{
		cfg:     cfg,
		logger:  logger,
		secrets: secrets,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *Provider) Kind() string { return provider.KindVoyage }

// ID is "voyage:{model}:{dim}:{dtype}". All three parts contribute to
// embedding identity — switching any of them invalidates the index.
func (p *Provider) ID() string {
	dim := p.cfg.OutputDimension
	dimStr := "auto"
	if dim > 0 {
		dimStr = strconv.Itoa(dim)
	}
	return "voyage:" + p.cfg.Model + ":" + dimStr + ":" + p.cfg.OutputDtype
}

func (p *Provider) Dimension() int       { return p.cfg.OutputDimension }
func (p *Provider) SupportsTokenize() bool { return false }

func (p *Provider) Start(ctx context.Context) error {
	if p.cfg.Model == "" {
		return errors.New("voyage: model is required")
	}
	switch p.cfg.OutputDtype {
	case DtypeFloat, DtypeInt8:
	default:
		return fmt.Errorf("voyage: unsupported output_dtype %q (use float or int8)", p.cfg.OutputDtype)
	}
	if _, ok := p.apiKey(); !ok {
		return fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, p.cfg.APIKeyEnv)
	}
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := p.embed(testCtx, []string{"ping"}, "document")
	if err != nil {
		return fmt.Errorf("voyage: connect test failed: %w", err)
	}
	return nil
}

func (p *Provider) Stop(_ context.Context) error { return nil }

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

func (p *Provider) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := p.embed(ctx, []string{query}, "query")
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
		return p.embed(ctx, texts, "document")
	}
	// Oversize input — split into sequential sub-batches. The Service
	// queue holds a single slot for the whole call, so concurrency
	// semantics are preserved (no extra slots consumed).
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		part, err := p.embed(ctx, texts[i:end], "document")
		if err != nil {
			return nil, fmt.Errorf("voyage: sub-batch [%d:%d]: %w", i, end, err)
		}
		out = append(out, part...)
	}
	return out, nil
}

func (p *Provider) TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	return p.EmbedDocuments(ctx, texts)
}

type embedRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type,omitempty"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	OutputDtype     string   `json:"output_dtype,omitempty"`
	Truncation      bool     `json:"truncation,omitempty"`
}

// embedResponseItem.Embedding is decoded as json.RawMessage because
// the shape depends on output_dtype: []float for float, []int for
// int8. dequantize() handles both branches.
type embedResponseItem struct {
	Embedding json.RawMessage `json:"embedding"`
	Index     int             `json:"index"`
}

type embedResponseUsage struct {
	TotalTokens int `json:"total_tokens"`
}

type embedResponse struct {
	Data  []embedResponseItem `json:"data"`
	Model string              `json:"model"`
	Usage embedResponseUsage  `json:"usage"`
}

func (p *Provider) embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	key, ok := p.apiKey()
	if !ok {
		return nil, fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, p.cfg.APIKeyEnv)
	}

	body, err := json.Marshal(embedRequest{
		Input:           texts,
		Model:           p.cfg.Model,
		InputType:       inputType,
		OutputDimension: p.cfg.OutputDimension,
		OutputDtype:     p.cfg.OutputDtype,
		Truncation:      p.cfg.Truncation,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal: %w", err)
	}
	url := p.cfg.BaseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("voyage: status %d: %s", resp.StatusCode, string(snippet))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("voyage: decode: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("voyage: got %d vectors for %d inputs", len(er.Data), len(texts))
	}
	out := make([][]float32, len(er.Data))
	for _, item := range er.Data {
		if item.Index < 0 || item.Index >= len(out) {
			return nil, fmt.Errorf("voyage: out-of-range index %d", item.Index)
		}
		vec, err := dequantize(item.Embedding, p.cfg.OutputDtype)
		if err != nil {
			return nil, fmt.Errorf("voyage: decode embedding[%d]: %w", item.Index, err)
		}
		out[item.Index] = vec
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("voyage: missing vector at index %d", i)
		}
	}
	return out, nil
}

// dequantize converts the raw JSON embedding to []float32 per dtype.
//
// For dtype=float: passthrough — Voyage returns IEEE 754 floats.
// For dtype=int8: each component is a signed 8-bit integer in
// [-128, 127]; Voyage's docs prescribe dividing by 127.0 to recover
// the approximate unit-norm float representation.
//
// This is the only place in the codebase that handles int8 quantized
// embeddings; chromem-go and the search path both work exclusively
// in float32.
func dequantize(raw json.RawMessage, dtype string) ([]float32, error) {
	switch dtype {
	case DtypeInt8:
		var ints []int8
		if err := json.Unmarshal(raw, &ints); err != nil {
			return nil, fmt.Errorf("int8 decode: %w", err)
		}
		out := make([]float32, len(ints))
		for i, v := range ints {
			out[i] = float32(v) / 127.0
		}
		return out, nil
	default:
		// "float" (and empty as a defensive default — Voyage's docs
		// say float is the implicit choice when output_dtype is
		// omitted from the request).
		var floats []float32
		if err := json.Unmarshal(raw, &floats); err != nil {
			return nil, fmt.Errorf("float decode: %w", err)
		}
		return floats, nil
	}
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
