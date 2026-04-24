package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// llamaClient talks to the llama-server over either a unix socket or TCP.
// It exposes three methods: Health (readiness probe), Embeddings (text→vector
// batch RPC), Tokenize (text→token IDs), and EmbedBatchTokenIDs (pre-tokenized
// sequences→vector batch RPC). The JSON shapes follow llama.cpp's OpenAI-like
// API surface.
type llamaClient struct {
	http    *http.Client
	baseURL string // http://unix (for unix transport) or http://host:port (tcp)
}

// newUnixClient wires an *http.Client whose Dial ignores the host:port passed
// in the URL and always connects to sockPath. The dummy http://unix/ host is
// still required by net/http's URL parsing. Timeout is intentionally generous
// (120s) because the first embed request after startup can be slow while the
// model warms up on Metal.
func newUnixClient(sockPath string) *llamaClient {
	return &llamaClient{
		http: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sockPath)
				},
				// No keep-alive is needed because cpp-httplib terminates the
				// connection after each response; explicit setting keeps the
				// behaviour obvious to reviewers.
				DisableKeepAlives: true,
			},
		},
		baseURL: "http://unix",
	}
}

// newTCPClient wires a conventional TCP client. Used when the unix socket path
// would exceed the platform limit (macOS sun_path = 104 bytes) or when the
// operator overrides via CIX_LLAMA_TRANSPORT=tcp.
func newTCPClient(host string, port int) *llamaClient {
	return &llamaClient{
		http: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
	}
}

// Health issues a GET /health and returns nil only when the sidecar reports
// itself as fully ready. Used by the supervisor's readiness probe loop and
// for debug endpoints.
//
// n3 — llama.cpp's /health returns HTTP 200 with `{"status": "loading model"}`
// during warm-up. A byte-only probe would consider that "ready" and race
// against the first real embed call, which then fails with an opaque error.
// We therefore parse the body and require status == "ok" (or empty, for
// older llama.cpp versions that did not emit the field).
func (c *llamaClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain to let the connection be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("health: status %d", resp.StatusCode)
	}
	// Cap body read at 1 KiB — /health payloads are tiny and we do not want
	// to amplify a misbehaving sidecar's large response.
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body); err != nil {
		// Older versions served an empty body; treat a decode failure as OK.
		return nil
	}
	switch body.Status {
	case "", "ok":
		return nil
	default:
		// "loading model" / "no slot available" etc — not ready yet.
		return fmt.Errorf("health: status=%q", body.Status)
	}
}

// embedRequest / embedResponse mirror the llama.cpp /v1/embeddings contract.
// `input` accepts string, []string, []int (token IDs), or [][]int (batch of
// pre-tokenized sequences) — all per the OpenAI embeddings spec.
type embedRequest struct {
	Input any    `json:"input"` // string | []string | []int | [][]int
	Model string `json:"model,omitempty"`
}

// tokenizeRequest / tokenizeResponse mirror llama.cpp POST /tokenize.
type tokenizeRequest struct {
	Content    string `json:"content"`
	AddSpecial bool   `json:"add_special"`
}

type tokenizeResponse struct {
	Tokens []int `json:"tokens"`
}

type embedResponseItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
	Object    string    `json:"object"`
}

type embedResponse struct {
	Data  []embedResponseItem `json:"data"`
	Model string              `json:"model"`
}

// Embeddings POSTs /v1/embeddings with the given input slice and returns the
// vectors in the order they appeared in the request. Any HTTP error status
// from llama-server is surfaced as a plain error — the caller (Service) is
// responsible for mapping it to a typed error.
func (c *llamaClient) Embeddings(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	// Always send an array even for one item so the response shape is stable.
	body, err := json.Marshal(embedRequest{Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read a bounded slice of the body so the error message stays useful
		// but a misbehaving server cannot balloon memory.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("embed: status %d: %s", resp.StatusCode, string(snippet))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embed: got %d vectors for %d inputs", len(er.Data), len(texts))
	}
	// llama.cpp does not guarantee response order matches request order, but
	// OpenAI spec (which llama.cpp follows) sets `index` on each item. Sort by
	// index before returning so callers can rely on positional mapping.
	out := make([][]float32, len(er.Data))
	for _, item := range er.Data {
		if item.Index < 0 || item.Index >= len(out) {
			return nil, fmt.Errorf("embed: out-of-range index %d", item.Index)
		}
		out[item.Index] = item.Embedding
	}
	for i, vec := range out {
		if vec == nil {
			return nil, fmt.Errorf("embed: missing vector at index %d", i)
		}
	}
	return out, nil
}

// Tokenize calls POST /tokenize and returns the token ID slice for text.
// add_special=true instructs llama-server to prepend CLS and append SEP so
// the returned IDs are ready to feed directly into EmbedBatchTokenIDs.
func (c *llamaClient) Tokenize(ctx context.Context, text string) ([]int, error) {
	body, err := json.Marshal(tokenizeRequest{Content: text, AddSpecial: true})
	if err != nil {
		return nil, fmt.Errorf("marshal tokenize request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/tokenize", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build tokenize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("tokenize: status %d: %s", resp.StatusCode, string(snippet))
	}

	var tr tokenizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode tokenize response: %w", err)
	}
	return tr.Tokens, nil
}

// EmbedBatchTokenIDs calls POST /v1/embeddings with a batch of pre-tokenized
// sequences ([][]int). Returns one vector per input sequence in input order.
// This avoids re-tokenizing text that was already tokenized by Tokenize().
func (c *llamaClient) EmbedBatchTokenIDs(ctx context.Context, sequences [][]int) ([][]float32, error) {
	if len(sequences) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Input: sequences})
	if err != nil {
		return nil, fmt.Errorf("marshal embed token-ids request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed token-ids request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed token-ids: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("embed token-ids: status %d: %s", resp.StatusCode, string(snippet))
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode embed token-ids response: %w", err)
	}
	if len(er.Data) != len(sequences) {
		return nil, fmt.Errorf("embed token-ids: got %d vectors for %d sequences", len(er.Data), len(sequences))
	}
	out := make([][]float32, len(er.Data))
	for _, item := range er.Data {
		if item.Index < 0 || item.Index >= len(out) {
			return nil, fmt.Errorf("embed token-ids: out-of-range index %d", item.Index)
		}
		out[item.Index] = item.Embedding
	}
	for i, vec := range out {
		if vec == nil {
			return nil, fmt.Errorf("embed token-ids: missing vector at index %d", i)
		}
	}
	return out, nil
}
