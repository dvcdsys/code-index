package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// fixedSecrets returns a SecretLookup that resolves a single
// (key, value) pair. Used by tests that don't want to touch
// os.Setenv (which would race other parallel tests).
func fixedSecrets(key, value string) provider.SecretLookup {
	return func(name string) (string, bool) {
		if name == key {
			return value, true
		}
		return "", false
	}
}

// stubServer returns an httptest.Server that responds to one POST
// /v1/embeddings hit. The recorded request body is sent on the
// returned channel so the test can assert on it.
func stubServer(t *testing.T, status int, body string) (*httptest.Server, <-chan []byte) {
	t.Helper()
	gotBody := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/embeddings") {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing Bearer auth header; got %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		select {
		case gotBody <- raw:
		default:
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, gotBody
}

func TestEmbedDocumentsBatch(t *testing.T) {
	srv, gotBody := stubServer(t, http.StatusOK, `{
		"data": [
			{"index": 1, "embedding": [0.2, 0.3]},
			{"index": 0, "embedding": [0.1, 0.4]}
		]
	}`)
	p := New(Config{
		BaseURL:   srv.URL,
		Model:     "text-embedding-3-small",
		APIKeyEnv: "TEST_KEY",
	}, fixedSecrets("TEST_KEY", "sk-test"), nil)

	vecs, err := p.EmbedDocuments(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	// Result must be in input order even though the server returned them swapped.
	if vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Fatalf("ordering wrong: got %v", vecs)
	}

	var req embedRequest
	if err := json.Unmarshal(<-gotBody, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Model != "text-embedding-3-small" {
		t.Errorf("model %q", req.Model)
	}
	if len(req.Input) != 2 || req.Input[0] != "first" {
		t.Errorf("input %v", req.Input)
	}
}

func TestEmbedDocumentsHTTPError(t *testing.T) {
	srv, _ := stubServer(t, http.StatusUnauthorized, `{"error":"bad key"}`)
	p := New(Config{
		BaseURL:   srv.URL,
		Model:     "m",
		APIKeyEnv: "K",
	}, fixedSecrets("K", "v"), nil)

	_, err := p.EmbedDocuments(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should surface status code: %v", err)
	}
}

func TestMissingAPIKey(t *testing.T) {
	p := New(Config{
		BaseURL:   "http://unused",
		Model:     "m",
		APIKeyEnv: "MISSING_VAR",
	}, fixedSecrets("OTHER", "v"), nil)

	_, err := p.EmbedDocuments(context.Background(), []string{"x"})
	if !errors.Is(err, provider.ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}

	st := p.Status()
	if st.State != provider.StateFailed {
		t.Errorf("Status state %q, expected failed", st.State)
	}
}

func TestIDFingerprint(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Model: "m"}, "openai:m"},
		{Config{Model: "m", Dimensions: 512}, "openai:m:512"},
	}
	for _, tc := range cases {
		p := New(tc.cfg, fixedSecrets("", ""), nil)
		if got := p.ID(); got != tc.want {
			t.Errorf("ID() = %q, want %q", got, tc.want)
		}
	}
}

func TestEmbedDocumentsSendsDimensions(t *testing.T) {
	srv, gotBody := stubServer(t, http.StatusOK, `{
		"data": [{"index": 0, "embedding": [0.1]}]
	}`)
	p := New(Config{
		BaseURL:    srv.URL,
		Model:      "m",
		APIKeyEnv:  "K",
		Dimensions: 256,
	}, fixedSecrets("K", "v"), nil)
	if _, err := p.EmbedDocuments(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	var req embedRequest
	_ = json.Unmarshal(<-gotBody, &req)
	if req.Dimensions != 256 {
		t.Errorf("dimensions should be 256, got %d", req.Dimensions)
	}
}
