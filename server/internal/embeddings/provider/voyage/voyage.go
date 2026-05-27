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

	"golang.org/x/time/rate"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// DefaultBaseURL is the public Voyage AI embeddings endpoint origin.
const DefaultBaseURL = "https://api.voyageai.com"

// Supported dtypes. binary/ubinary intentionally absent for v1.
const (
	DtypeFloat = "float"
	DtypeInt8  = "int8"
)

// defaultMaxBatchSize is the static safe default for inputs per POST
// when the operator has not configured an explicit MaxInputsPerRequest
// in the provider config. Voyage's voyage-code-* models cap at 128;
// voyage-3* accept up to 1000. We pick the lower bound so a single
// default works across all models without 422s.
const defaultMaxBatchSize = 128

// defaultMaxTokensPerBatch is the static safe default for total
// estimated tokens per POST when the operator has not configured an
// explicit MaxTokensPerRequest. Voyage's hard limit (observed in 400
// responses) is 120K; we target 100K to leave 17% headroom for the
// byte→token estimation error.
const defaultMaxTokensPerBatch = 100_000

// bytesPerToken is a conservative chars-per-token heuristic used to
// estimate the request's token cost without a real tokenizer. Voyage
// does not publish their tokenizer for client-side use; empirically
// code averages ~3–4 chars/token and English prose ~4. We use 3 to
// over-count the cost (safe upper bound — we'll split sooner than the
// upstream limit, never later).
//
// len() in Go returns BYTE length, not rune count, so multi-byte
// UTF-8 input (Cyrillic comments, CJK) gets further over-counted —
// also safe.
const bytesPerToken = 3

// Config is the persisted shape of the voyage provider's config blob.
type Config struct {
	BaseURL         string `json:"base_url,omitempty"`
	APIKeyEnv       string `json:"api_key_env"`
	Model           string `json:"model"`
	OutputDimension int    `json:"output_dimension,omitempty"`
	OutputDtype     string `json:"output_dtype,omitempty"`
	Truncation      bool   `json:"truncation,omitempty"`

	// RateLimitRPM caps requests-per-minute the provider will emit.
	// 0 = no client-side throttling (rely on Voyage to 429 us). When
	// >0, a token-bucket waits before each POST so we don't exceed
	// the configured rate. The operator sets this from the Voyage
	// dashboard's "Rate Limits" page to match their account tier.
	RateLimitRPM int `json:"rate_limit_rpm,omitempty"`

	// RateLimitTPM caps tokens-per-minute (estimated, summed across
	// all in-flight + recent requests). 0 = no throttling.
	RateLimitTPM int `json:"rate_limit_tpm,omitempty"`

	// MaxInputsPerRequest overrides defaultMaxBatchSize. 0 = use
	// the default (128, safe for voyage-code-*). Operators running
	// only voyage-3* may bump this to 1000 for fewer round-trips.
	MaxInputsPerRequest int `json:"max_inputs_per_request,omitempty"`

	// MaxTokensPerRequest overrides defaultMaxTokensPerBatch. 0 =
	// use the default (100K with 20K headroom from Voyage's 120K
	// hard cap).
	MaxTokensPerRequest int `json:"max_tokens_per_request,omitempty"`
}

// maxBatchSize returns the effective per-POST input cap: explicit
// config override, falling back to the static default.
func (c *Config) maxBatchSize() int {
	if c.MaxInputsPerRequest > 0 {
		return c.MaxInputsPerRequest
	}
	return defaultMaxBatchSize
}

// maxTokensPerBatch returns the effective per-POST token cap.
func (c *Config) maxTokensPerBatch() int {
	if c.MaxTokensPerRequest > 0 {
		return c.MaxTokensPerRequest
	}
	return defaultMaxTokensPerBatch
}

// Provider is the Voyage HTTP client.
type Provider struct {
	cfg     Config
	logger  *slog.Logger
	secrets provider.SecretLookup
	http    *http.Client

	// reqLimiter caps requests-per-minute when cfg.RateLimitRPM > 0.
	// nil when no throttling is configured. Token-bucket with burst
	// = 1 — we don't allow client-side bursts, since the upstream
	// budget is a sliding minute and bursting saves nothing.
	reqLimiter *rate.Limiter

	// tokenLimiter caps tokens-per-minute when cfg.RateLimitTPM > 0.
	// Burst is set to maxTokensPerBatch so a single full-budget POST
	// can pass even when the bucket is otherwise empty (we'd just
	// wait longer afterward). nil when no throttling.
	tokenLimiter *rate.Limiter
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
	p := &Provider{
		cfg:     cfg,
		logger:  logger,
		secrets: secrets,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	// Convert RPM/TPM to per-second token-bucket rates. burst on the
	// request bucket is 1 (one request worth of "credit"); burst on
	// the token bucket equals one full POST so we don't deadlock a
	// legitimate big batch.
	if cfg.RateLimitRPM > 0 {
		p.reqLimiter = rate.NewLimiter(rate.Limit(float64(cfg.RateLimitRPM)/60.0), 1)
	}
	if cfg.RateLimitTPM > 0 {
		p.tokenLimiter = rate.NewLimiter(rate.Limit(float64(cfg.RateLimitTPM)/60.0), cfg.maxTokensPerBatch())
	}
	return p
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
	batches := planBatches(texts, p.cfg.maxBatchSize(), p.cfg.maxTokensPerBatch())
	if len(batches) == 1 {
		return p.embed(ctx, batches[0], "document")
	}
	// Oversize input — split into sequential sub-batches. The Service
	// queue holds a single slot for the whole call, so concurrency
	// semantics are preserved (no extra slots consumed).
	p.logger.Info("voyage: splitting batch",
		"model", p.cfg.Model,
		"total_inputs", len(texts),
		"sub_batches", len(batches),
		"limit_inputs", p.cfg.maxBatchSize(),
		"limit_tokens", p.cfg.maxTokensPerBatch(),
	)
	out := make([][]float32, 0, len(texts))
	offset := 0
	for i, batch := range batches {
		p.logger.Debug("voyage: sub-batch POST",
			"index", i+1,
			"of", len(batches),
			"inputs", len(batch),
			"est_tokens", sumEstimateTokens(batch),
		)
		part, err := p.embed(ctx, batch, "document")
		if err != nil {
			return nil, fmt.Errorf("voyage: sub-batch %d/%d (offset=%d, inputs=%d, ~%d tokens): %w",
				i+1, len(batches), offset, len(batch), sumEstimateTokens(batch), err)
		}
		out = append(out, part...)
		offset += len(batch)
	}
	return out, nil
}

// planBatches groups texts into sub-batches that each respect BOTH
// the input-count cap and the token-budget cap. A single text that
// on its own exceeds the token budget is placed in its own batch —
// Voyage will then 400 with a clear "tokens after truncation"
// message and the caller surfaces that to the operator (indicates
// the chunker upstream let through an over-long chunk).
//
// maxInputs and maxTokens come from the live Provider.cfg so the
// operator can override them via the admin form when their tier or
// chosen model allows a higher cap (e.g. voyage-3-large at 1000
// inputs/POST instead of 128).
func planBatches(texts []string, maxInputs, maxTokens int) [][]string {
	if len(texts) == 0 {
		return nil
	}
	var batches [][]string
	var current []string
	currentTokens := 0
	for _, t := range texts {
		est := estimateTokens(t)
		// Close the current batch when adding this text would exceed
		// either limit (and the batch already has something to send).
		if len(current) > 0 && (len(current) >= maxInputs || currentTokens+est > maxTokens) {
			batches = append(batches, current)
			current = nil
			currentTokens = 0
		}
		current = append(current, t)
		currentTokens += est
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// estimateTokens returns a conservative upper bound on the token cost
// of one text, in Voyage's tokenizer. Uses byte-length divided by a
// chars-per-token heuristic; see bytesPerToken doc for rationale.
func estimateTokens(s string) int {
	return len(s) / bytesPerToken
}

// sumEstimateTokens sums estimateTokens over a slice. Cheap; used in
// log lines so an operator can see the per-batch cost.
func sumEstimateTokens(texts []string) int {
	n := 0
	for _, t := range texts {
		n += estimateTokens(t)
	}
	return n
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

	// Wait on the operator-configured rate-limit token-buckets before
	// hitting the wire. Both reservations honour ctx cancellation so
	// a server shutdown / drain doesn't strand callers in Wait().
	if p.reqLimiter != nil {
		if err := p.reqLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("voyage: request-rate wait: %w", err)
		}
	}
	if p.tokenLimiter != nil {
		est := sumEstimateTokens(texts)
		if est > p.tokenLimiter.Burst() {
			est = p.tokenLimiter.Burst()
		}
		if est > 0 {
			if err := p.tokenLimiter.WaitN(ctx, est); err != nil {
				return nil, fmt.Errorf("voyage: token-rate wait (~%d tokens): %w", est, err)
			}
		}
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
