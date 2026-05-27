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

	batches := planBatches(texts)
	if len(batches) < 2 {
		t.Fatalf("expected at least 2 batches, got %d", len(batches))
	}

	got := 0
	for _, b := range batches {
		got += len(b)
		if est := sumEstimateTokens(b); est > maxTokensPerBatch && len(b) > 1 {
			t.Errorf("batch with %d inputs exceeds token budget: ~%d tokens > %d",
				len(b), est, maxTokensPerBatch)
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
	batches := planBatches(texts)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches (128 + 72), got %d", len(batches))
	}
	if len(batches[0]) != maxBatchSize {
		t.Errorf("first batch has %d inputs, want %d", len(batches[0]), maxBatchSize)
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
