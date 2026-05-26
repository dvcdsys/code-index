package voyage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
