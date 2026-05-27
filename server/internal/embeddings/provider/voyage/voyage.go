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
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// voyageBatchTooLargeRegex matches Voyage's per-batch token-limit
// 400 response so the caller can react adaptively. Voyage's message
// is fairly stable:
//   "The max allowed tokens per submitted batch is 120000.
//    Your batch has 187609 tokens after truncation."
// We capture both numbers; the actual count drives how aggressively
// the caller bisects.
var voyageBatchTooLargeRegex = regexp.MustCompile(
	`max allowed tokens per submitted batch is (\d+).*Your batch has (\d+) tokens`,
)

// parseBatchTooLarge tries to extract (cap, actual) token counts
// from a Voyage 400 message. Returns (0, 0, false) when the message
// doesn't match — e.g. a different 400 like "model not found".
func parseBatchTooLarge(errMsg string) (cap, actual int, ok bool) {
	m := voyageBatchTooLargeRegex.FindStringSubmatch(errMsg)
	if len(m) < 3 {
		return 0, 0, false
	}
	c, err1 := strconv.Atoi(m[1])
	a, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return c, a, true
}

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

// defaultMaxInputBytes caps the byte-length of any SINGLE input
// (one chunk) before it goes to Voyage. When a chunk exceeds this
// the provider splits it into non-overlapping byte windows and
// averages the resulting per-window vectors — same pattern as the
// ollama provider's TokenizeAndEmbed, but byte-based here because
// Voyage doesn't expose a tokenize endpoint.
//
// Sized for voyage-code-3's 32K-token per-input context window
// with headroom: worst-case dense code can hit ~1 byte / token,
// so 30K bytes ≈ 30K tokens, leaving the model 2K tokens of
// margin. Prose typically has ~4 bytes / token, so a 30K-byte
// English passage is ~7.5K tokens — well under the cap.
const defaultMaxInputBytes = 30_000

// bytesPerToken is the chars-per-token heuristic used to estimate
// token cost without a real tokenizer. Voyage's own docs at
// https://docs.voyageai.com/docs/tokenization recommend "dividing
// character count by 5" for English prose, but cix is primarily a
// CODE-indexing workload and dense source code runs much hotter:
// production logs against voyage-code-3 show 1 token ≈ 1.4 bytes
// for tight Go/Rust files, so a /5 estimate under-counts by
// ~3.6×. That's exactly the kind of error that ships an
// estimated-51K-token batch into a real 187K-token POST and
// triggers Voyage's 120K hard-cap 400.
//
// 2 is the safer baseline: matches code reality within a small
// margin, and over-estimates prose by ~2.5× (fewer inputs packed
// per batch — more round-trips, never a 400). The
// embedWithAdaptiveSplit bisect remains as a residual safety net
// for outliers (Voyage publishes real HF tokenizers on
// huggingface.co/voyageai/voyage-* but pulling one in here would
// require a CGO Rust dep — deferred to a follow-up).
//
// len() in Go returns BYTE length, not rune count, so multi-byte
// UTF-8 input (Cyrillic comments, CJK) gets over-counted relative
// to Voyage's character-based heuristic — safe direction (more
// splits, never fewer).
const bytesPerToken = 2

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

	// MaxInputBytes caps the byte-length of any SINGLE input before
	// the provider splits it into byte-aligned windows + averages
	// the resulting vectors. 0 → use defaultMaxInputBytes (sized
	// for voyage-code-3's 32K-token per-input context window with
	// margin). The operator only needs to override when running a
	// model with a substantially larger context (e.g. future
	// voyage-* with 64K context) or a different bytes-per-token
	// regime (heavily non-ASCII content).
	MaxInputBytes int `json:"max_input_bytes,omitempty"`
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

// maxInputBytes returns the effective per-input byte cap (defines
// when splitOversizeInput kicks in).
func (c *Config) maxInputBytes() int {
	if c.MaxInputBytes > 0 {
		return c.MaxInputBytes
	}
	return defaultMaxInputBytes
}

// splitOversizeInput slices text into non-overlapping byte windows
// no larger than maxBytes each, aligned to UTF-8 rune boundaries so
// we never cut a multi-byte character mid-sequence. Returns the
// original text in a single-element slice when it's already small
// enough — common case is zero allocations beyond the slice header.
//
// Why byte-based rather than token-aligned: Voyage doesn't expose a
// /tokenize endpoint we can call client-side. The ollama provider
// gets to split at exact token boundaries (CLS + content_window +
// SEP) because llama-server tokenises for us. Voyage's real
// tokenizer is opaque, so we approximate with bytes. The adaptive
// bisect on 400 (see embedWithAdaptiveSplit) is the safety net
// when this approximation under-counts.
func splitOversizeInput(text string, maxBytes int) []string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return []string{text}
	}
	var windows []string
	start := 0
	for start < len(text) {
		end := start + maxBytes
		if end >= len(text) {
			windows = append(windows, text[start:])
			break
		}
		// Walk backward to the nearest rune-start byte so we never
		// split a multi-byte UTF-8 character in the middle.
		// utf8.RuneStart returns true for ASCII (single-byte) and
		// for the leading byte of a multi-byte sequence.
		for end > start && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == start {
			// Degenerate: maxBytes < the length of the next rune.
			// Cut at the original boundary to make progress; the
			// resulting partial codepoint is still bytes Voyage
			// can tokenise (just less meaningfully). In practice
			// maxBytes is in the tens of thousands so this branch
			// is unreachable on real input.
			end = start + maxBytes
		}
		windows = append(windows, text[start:end])
		start = end
	}
	return windows
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
	vecs, err := p.embedAndAverage(ctx, []string{query}, "query")
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (p *Provider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return p.embedAndAverage(ctx, texts, "document")
}

// embedAndAverage is the per-input sliding-window pipeline (mirrors
// ollama.Provider.TokenizeAndEmbed, but byte-based since Voyage has
// no tokenize endpoint we can hit):
//
//  1. Walk every input. If its byte-length exceeds maxInputBytes,
//     split at rune-aligned boundaries into N non-overlapping
//     windows. Remember which original each window belongs to via
//     a (start, length) span table.
//  2. Send the expanded slice through planBatches → POST chains
//     with the same adaptive-bisect-on-400 behaviour as before.
//  3. Reassemble: for inputs that produced multiple windows,
//     average the per-window vectors back to a single vector.
//     For inputs that fit unchanged, the vector passes through.
//
// The averaging step is what keeps tail content of an over-long
// chunk from being dropped (which is what truncation:true would do
// upstream). The trade-off is N times more POST/token cost per
// such chunk, but oversize chunks are rare on well-chunked
// indexes — the indexer should already be cutting at function /
// class boundaries.
func (p *Provider) embedAndAverage(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	maxIn := p.cfg.maxInputBytes()

	// Phase 1: expand oversize inputs into windows; track spans.
	type span struct{ start, length int }
	spans := make([]span, len(texts))
	var expanded []string
	totalSplits := 0
	for i, t := range texts {
		windows := splitOversizeInput(t, maxIn)
		spans[i] = span{start: len(expanded), length: len(windows)}
		expanded = append(expanded, windows...)
		if len(windows) > 1 {
			totalSplits += len(windows)
		}
	}
	if totalSplits > 0 {
		p.logger.Info("voyage: oversize inputs split into byte-windows",
			"original_inputs", len(texts),
			"total_windows", len(expanded),
			"split_windows", totalSplits,
			"max_input_bytes", maxIn,
		)
	}

	// Phase 2: batch + POST as before, on the expanded slice.
	batches := planBatches(expanded, p.cfg.maxBatchSize(), p.cfg.maxTokensPerBatch())
	if len(batches) > 1 {
		p.logger.Info("voyage: splitting batch",
			"model", p.cfg.Model,
			"total_inputs", len(expanded),
			"sub_batches", len(batches),
			"limit_inputs", p.cfg.maxBatchSize(),
			"limit_tokens", p.cfg.maxTokensPerBatch(),
		)
	}
	allVecs := make([][]float32, 0, len(expanded))
	offset := 0
	for i, batch := range batches {
		p.logger.Debug("voyage: sub-batch POST",
			"index", i+1,
			"of", len(batches),
			"inputs", len(batch),
			"est_tokens", sumEstimateTokens(batch),
		)
		part, err := p.embedWithAdaptiveSplit(ctx, batch, inputType)
		if err != nil {
			return nil, fmt.Errorf("voyage: sub-batch %d/%d (offset=%d, inputs=%d, ~%d tokens): %w",
				i+1, len(batches), offset, len(batch), sumEstimateTokens(batch), err)
		}
		allVecs = append(allVecs, part...)
		offset += len(batch)
	}

	// Phase 3: reassemble — average sub-window vectors back to one
	// vector per original input. Vectors that came through alone
	// (one-window inputs) pass through unchanged.
	if totalSplits == 0 {
		// Fast path: no oversize inputs, allVecs already maps 1:1
		// to texts.
		return allVecs, nil
	}
	result := make([][]float32, len(texts))
	for i, sp := range spans {
		if sp.length == 1 {
			result[i] = allVecs[sp.start]
			continue
		}
		dim := len(allVecs[sp.start])
		avg := make([]float32, dim)
		for k := 0; k < sp.length; k++ {
			v := allVecs[sp.start+k]
			for d := range avg {
				avg[d] += v[d]
			}
		}
		n := float32(sp.length)
		for d := range avg {
			avg[d] /= n
		}
		result[i] = avg
	}
	return result, nil
}

// embedWithAdaptiveSplit wraps embed() with a defensive bisect-on-400
// loop. Our byte→token estimator (see bytesPerToken) cannot match
// Voyage's real tokenizer exactly; pathological inputs may still
// overflow the per-batch cap. On a "batch too large" 400 we split
// the batch in half and retry both halves recursively. When the
// batch is already a single input and STILL too large there's
// nothing to bisect — we return a clear error pointing the operator
// at the chunker upstream (each chunk should fit voyage's per-input
// limits; if it doesn't, the chunker let through an over-long unit).
func (p *Provider) embedWithAdaptiveSplit(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	vecs, err := p.embed(ctx, texts, inputType)
	if err == nil {
		return vecs, nil
	}
	cap, actual, ok := parseBatchTooLarge(err.Error())
	if !ok {
		// Different error class (auth, network, rate-limit, …) —
		// surface as-is, retry would not help.
		return nil, err
	}
	if len(texts) <= 1 {
		// A single chunk on its own exceeds Voyage's hard cap.
		// We CAN'T split the text — that would corrupt the
		// semantic unit the indexer chose. Tell the operator
		// where to fix it instead.
		return nil, fmt.Errorf(
			"voyage: a single chunk produced %d tokens (cap %d). Reduce the indexer's max chunk size or switch to a model with a higher per-request cap: %w",
			actual, cap, err,
		)
	}
	// Bisect — the caller's batch had multiple inputs whose real
	// token sum exceeded the cap. Logging deliberately captures
	// the cap+actual so the operator can spot a pattern (e.g.
	// estimator consistently off by 1.5x) without grepping the
	// raw error.
	mid := len(texts) / 2
	p.logger.Warn("voyage: batch too large — bisecting and retrying",
		"inputs", len(texts),
		"est_tokens", sumEstimateTokens(texts),
		"voyage_actual_tokens", actual,
		"voyage_cap", cap,
		"left_half", mid,
		"right_half", len(texts)-mid,
	)
	left, err := p.embedWithAdaptiveSplit(ctx, texts[:mid], inputType)
	if err != nil {
		return nil, err
	}
	right, err := p.embedWithAdaptiveSplit(ctx, texts[mid:], inputType)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
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
