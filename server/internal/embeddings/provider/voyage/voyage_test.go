package voyage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

func fixedSecrets(key, value string) provider.SecretLookup {
	return func(name string) (string, bool) {
		if name == key {
			return value, true
		}
		return "", false
	}
}

func stubServer(t *testing.T, status int, body string) (*httptest.Server, <-chan []byte) {
	t.Helper()
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/embeddings") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		select {
		case got <- raw:
		default:
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestEmbedQuerySendsInputTypeQuery(t *testing.T) {
	srv, gotBody := stubServer(t, http.StatusOK, `{
		"data": [{"index": 0, "embedding": [0.1, 0.2]}],
		"model": "voyage-code-3",
		"usage": {"total_tokens": 3}
	}`)
	p := New(Config{
		BaseURL:         srv.URL,
		APIKeyEnv:       "K",
		Model:           "voyage-code-3",
		OutputDimension: 1024,
		OutputDtype:     DtypeFloat,
	}, fixedSecrets("K", "v"), nil)

	if _, err := p.EmbedQuery(context.Background(), "where is X"); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	var req embedRequest
	_ = json.Unmarshal(<-gotBody, &req)
	if req.InputType != "query" {
		t.Errorf("input_type %q; expected query", req.InputType)
	}
	if req.OutputDimension != 1024 {
		t.Errorf("output_dimension %d", req.OutputDimension)
	}
}

func TestEmbedDocumentsSendsInputTypeDocument(t *testing.T) {
	srv, gotBody := stubServer(t, http.StatusOK, `{
		"data": [{"index": 0, "embedding": [0.1]}],
		"usage": {"total_tokens": 1}
	}`)
	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "m", OutputDtype: DtypeFloat,
	}, fixedSecrets("K", "v"), nil)
	_, _ = p.EmbedDocuments(context.Background(), []string{"x"})
	var req embedRequest
	_ = json.Unmarshal(<-gotBody, &req)
	if req.InputType != "document" {
		t.Errorf("input_type %q; expected document", req.InputType)
	}
}

func TestInt8Dequantize(t *testing.T) {
	// int8 vector [127, -127, 0, 64] dequantized to float ~ [1.0, -1.0, 0.0, ~0.504]
	srv, _ := stubServer(t, http.StatusOK, `{
		"data": [{"index": 0, "embedding": [127, -127, 0, 64]}],
		"usage": {"total_tokens": 1}
	}`)
	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "m", OutputDtype: DtypeInt8,
	}, fixedSecrets("K", "v"), nil)
	vecs, err := p.EmbedDocuments(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 4 {
		t.Fatalf("shape wrong: %v", vecs)
	}
	v := vecs[0]
	if v[0] < 0.999 || v[1] > -0.999 || v[2] != 0 || v[3] < 0.50 || v[3] > 0.51 {
		t.Errorf("dequantized values out of range: %v", v)
	}
}

func TestIDFingerprintIncludesAll(t *testing.T) {
	p := New(Config{
		Model: "voyage-code-3", APIKeyEnv: "K",
		OutputDimension: 1024, OutputDtype: DtypeInt8,
	}, fixedSecrets("K", "v"), nil)
	want := "voyage:voyage-code-3:1024:int8"
	if got := p.ID(); got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
}

// TestEmbedDocumentsSplitsOversizeBatch covers the transparent
// per-provider split: Voyage's voyage-code-* models cap at 128
// inputs/request, so a 200-item EmbedDocuments call must produce
// TWO POSTs (128 + 72) and return all 200 vectors in input order.
func TestEmbedDocumentsSplitsOversizeBatch(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		// Echo back as many embeddings as the request contained so
		// the caller's input ↔ vector mapping is verifiable.
		raw, _ := io.ReadAll(r.Body)
		var req embedRequest
		_ = json.Unmarshal(raw, &req)
		if len(req.Input) > 128 {
			t.Errorf("POST #%d carried %d inputs, expected <= 128", posts, len(req.Input))
		}
		items := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			items[i] = map[string]any{"index": i, "embedding": []float32{float32(i)}}
		}
		body, _ := json.Marshal(map[string]any{
			"data":  items,
			"model": req.Model,
			"usage": map[string]int{"total_tokens": 1},
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-code-3",
		OutputDimension: 0, OutputDtype: DtypeFloat,
	}, fixedSecrets("K", "v"), nil)

	texts := make([]string, 200)
	for i := range texts {
		texts[i] = "chunk"
	}
	vecs, err := p.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if got := len(vecs); got != 200 {
		t.Fatalf("got %d vectors, want 200", got)
	}
	if posts != 2 {
		t.Errorf("expected 2 POSTs (128 + 72), got %d", posts)
	}
}

// TestPlanBatches_SplitsByTokenBudget covers the second cap on per-
// request batch size: even when input count is under maxBatchSize,
// Voyage hard-limits the request to 120K tokens. Our estimator uses
// 3 bytes/token so a 300_000-byte text estimates to 100_000 tokens,
// hitting the budget exactly. Mixing one huge text with several
// smaller ones should produce multiple batches.
func TestPlanBatches_SplitsByTokenBudget(t *testing.T) {
	big := strings.Repeat("x", 300_000) // ~100_000 est tokens
	small := "tiny"
	texts := []string{big, small, small, small, small, small}

	batches := planBatches(texts, defaultMaxBatchSize, defaultMaxTokensPerBatch)
	if len(batches) < 2 {
		t.Fatalf("expected at least 2 batches, got %d", len(batches))
	}

	got := 0
	for _, b := range batches {
		got += len(b)
		if est := sumEstimateTokens(b); est > defaultMaxTokensPerBatch && len(b) > 1 {
			t.Errorf("batch with %d inputs exceeds token budget: ~%d tokens > %d",
				len(b), est, defaultMaxTokensPerBatch)
		}
	}
	if got != len(texts) {
		t.Errorf("inputs lost across batches: got %d, want %d", got, len(texts))
	}
}

// TestPlanBatches_RespectsCountCap verifies the legacy 128-input
// cap is still enforced when token estimates wouldn't trigger a
// split. 200 small texts → at least 2 batches (128 + 72).
func TestPlanBatches_RespectsCountCap(t *testing.T) {
	texts := make([]string, 200)
	for i := range texts {
		texts[i] = "chunk"
	}
	batches := planBatches(texts, defaultMaxBatchSize, defaultMaxTokensPerBatch)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches (128 + 72), got %d", len(batches))
	}
	if len(batches[0]) != defaultMaxBatchSize {
		t.Errorf("first batch has %d inputs, want %d", len(batches[0]), defaultMaxBatchSize)
	}
	if len(batches[1]) != 72 {
		t.Errorf("second batch has %d inputs, want 72", len(batches[1]))
	}
}

// TestEmbedDocumentsSplitsByTokenBudget exercises the end-to-end
// flow: an oversize batch should turn into multiple POSTs to the
// upstream server, even when the input count alone wouldn't trigger
// the count-based split.
func TestEmbedDocumentsSplitsByTokenBudget(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		raw, _ := io.ReadAll(r.Body)
		var req embedRequest
		_ = json.Unmarshal(raw, &req)
		items := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			items[i] = map[string]any{"index": i, "embedding": []float32{0.1}}
		}
		body, _ := json.Marshal(map[string]any{
			"data":  items,
			"model": req.Model,
			"usage": map[string]int{"total_tokens": 1},
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	// Two big texts ~100K est tokens each — should produce >= 2 POSTs.
	big := strings.Repeat("x", 300_000)
	texts := []string{big, big}
	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-code-3", OutputDtype: DtypeFloat,
	}, fixedSecrets("K", "v"), nil)
	vecs, err := p.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if got := len(vecs); got != 2 {
		t.Fatalf("got %d vectors, want 2", got)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Errorf("expected at least 2 POSTs due to token-budget split, got %d", hits)
	}
}

// TestEmbedDocuments_BisectsOnBatchTooLarge covers the adaptive
// recovery path: when Voyage returns 400 "max allowed tokens per
// submitted batch is 120000. Your batch has N tokens" the provider
// splits the batch in half and retries. The stub here rejects any
// POST with more than 1 input on the first hit; the provider must
// bisect down to single-input POSTs and succeed.
func TestEmbedDocuments_BisectsOnBatchTooLarge(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		raw, _ := io.ReadAll(r.Body)
		var req embedRequest
		_ = json.Unmarshal(raw, &req)
		// Reject any batch with > 1 input — forces the provider to
		// bisect all the way to singletons.
		if len(req.Input) > 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"Request failed. The max allowed tokens per submitted batch is 120000. Your batch has 200000 tokens after truncation."}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-code-3", OutputDtype: DtypeFloat,
		// Pretend our config-time cap is high enough to NOT split via
		// planBatches; we want to exercise the runtime 400 bisect path.
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 10_000_000,
	}, fixedSecrets("K", "v"), nil)

	vecs, err := p.EmbedDocuments(context.Background(), []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != 4 {
		t.Fatalf("got %d vectors, want 4", len(vecs))
	}
	// One initial rejected POST (4 inputs), two more halves rejected
	// (2 + 2), four singleton POSTs that succeed. Total = 7.
	if got := atomic.LoadInt32(&posts); got < 7 {
		t.Errorf("expected at least 7 POSTs (rejected + bisected + singletons), got %d", got)
	}
}

// TestEmbedDocuments_SingleInputTooLargeFailsClean covers the
// "no split possible" case: when a SINGLE chunk produces more
// tokens than the upstream cap, we cannot bisect further (would
// corrupt the chunk). The provider returns a clear error that
// points the operator at the chunker rather than retrying forever.
func TestEmbedDocuments_SingleInputTooLargeFailsClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"Request failed. The max allowed tokens per submitted batch is 120000. Your batch has 187609 tokens after truncation."}`))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-code-3", OutputDtype: DtypeFloat,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 10_000_000,
	}, fixedSecrets("K", "v"), nil)

	_, err := p.EmbedDocuments(context.Background(), []string{"one big chunk"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "single chunk") || !strings.Contains(msg, "187609 tokens") {
		t.Errorf("error should mention single-chunk + actual token count: %q", msg)
	}
	if !strings.Contains(msg, "Reduce the indexer's max chunk size") {
		t.Errorf("error should hint at upstream chunker fix: %q", msg)
	}
}

func TestParseBatchTooLarge(t *testing.T) {
	cases := []struct {
		msg                string
		wantCap, wantAct   int
		wantOK             bool
	}{
		{
			"voyage: status 400: {\"detail\":\"Request failed. The max allowed tokens per submitted batch is 120000. Your batch has 187609 tokens after truncation.\"}",
			120000, 187609, true,
		},
		{
			"voyage: status 429: rate limited",
			0, 0, false,
		},
		{
			"voyage: status 400: model not found",
			0, 0, false,
		},
	}
	for _, tc := range cases {
		gotCap, gotAct, ok := parseBatchTooLarge(tc.msg)
		if ok != tc.wantOK || gotCap != tc.wantCap || gotAct != tc.wantAct {
			t.Errorf("parseBatchTooLarge(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.msg, gotCap, gotAct, ok, tc.wantCap, tc.wantAct, tc.wantOK)
		}
	}
}

// TestRateLimitRPMThrottlesRequests verifies that when the operator
// configures RateLimitRPM, the provider actually waits between
// requests. 120 RPM = 1 request per 500ms (burst of 1), so 2
// sequential requests on a fresh limiter take ~500ms total.
func TestRateLimitRPMThrottlesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	// 120 RPM → 2 req/s → second call must wait ~500ms (after the
	// burst-1 bucket drained on the first call).
	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-3", OutputDtype: DtypeFloat,
		RateLimitRPM: 120,
	}, fixedSecrets("K", "v"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call: instant (burst available).
	if _, err := p.EmbedQuery(ctx, "first"); err != nil {
		t.Fatalf("first EmbedQuery: %v", err)
	}
	// Second call: must wait. We don't measure precisely because rate
	// limiter has sub-millisecond timing variance; ≥ 300ms is enough
	// to know the throttle fired (well above any test-runtime noise).
	start := time.Now()
	if _, err := p.EmbedQuery(ctx, "second"); err != nil {
		t.Fatalf("second EmbedQuery: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Errorf("expected second call to wait for RPM limiter (>= 300ms); elapsed=%s", elapsed)
	}
}

// TestRateLimitTPMThrottlesTokens verifies the token-budget bucket
// also forces a wait when consumption exceeds the per-minute rate.
// 600K TPM = 10K tokens/s, burst = maxTokensPerBatch (100K). Sending
// two batches of 60K tokens each should make the second wait while
// the bucket refills.
func TestRateLimitTPMThrottlesTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "voyage-3", OutputDtype: DtypeFloat,
		// burst = maxTokensPerBatch (100K), refill rate 600K/min = 10K/s.
		RateLimitTPM: 600_000,
	}, fixedSecrets("K", "v"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 180K bytes ≈ 60K est tokens — half the burst budget.
	big := strings.Repeat("x", 180_000)

	// First call drains 60K of the 100K-burst bucket: instant.
	if _, err := p.EmbedQuery(ctx, big); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second call wants another 60K but only 40K is left; needs to
	// wait for 20K to refill at 10K/s = ~2s.
	start := time.Now()
	if _, err := p.EmbedQuery(ctx, big); err != nil {
		t.Fatalf("second: %v", err)
	}
	elapsed := time.Since(start)
	// Lower bound 1.5s — leaves margin for rate-limiter clock granularity.
	if elapsed < 1500*time.Millisecond {
		t.Errorf("expected second call to wait for TPM limiter (>= 1.5s); elapsed=%s", elapsed)
	}
}

func TestUsageDecodesWithoutPromptTokens(t *testing.T) {
	// Voyage's usage object lacks prompt_tokens — make sure decode doesn't error.
	srv, _ := stubServer(t, http.StatusOK, `{
		"data": [{"index": 0, "embedding": [0.1]}],
		"model": "voyage-3",
		"usage": {"total_tokens": 7}
	}`)
	p := New(Config{
		BaseURL: srv.URL, APIKeyEnv: "K", Model: "m", OutputDtype: DtypeFloat,
	}, fixedSecrets("K", "v"), nil)
	if _, err := p.EmbedDocuments(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
