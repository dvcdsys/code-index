package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/config"
)

// TestGetClient_ExpandsHeaderEnvVars is the end-to-end proof of issue #59:
// a configured header with a ${VAR} placeholder is expanded at request time
// and reaches the wire, while the on-disk config keeps the placeholder (no
// plaintext secret persisted).
func TestGetClient_ExpandsHeaderEnvVars(t *testing.T) {
	isolateConfig(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := config.SetServerURL(config.DefaultServerName, srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "k"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerHeader(config.DefaultServerName, "CF-Access-Client-Secret", "${CIX_TEST_SECRET}"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CIX_TEST_SECRET", "expanded-secret-123")
	withFlags(t, "", "", "")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if err := c.Health(); err != nil {
		t.Fatalf("Health: %v", err)
	}

	if got.Get("CF-Access-Client-Secret") != "expanded-secret-123" {
		t.Errorf("header on wire = %q, want expanded-secret-123", got.Get("CF-Access-Client-Secret"))
	}

	// The config file must still hold the placeholder, not the secret.
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".cix", "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "expanded-secret-123") {
		t.Errorf("secret leaked into config file:\n%s", raw)
	}
	if !strings.Contains(string(raw), "${CIX_TEST_SECRET}") {
		t.Errorf("config should keep the ${CIX_TEST_SECRET} placeholder:\n%s", raw)
	}
}

// TestGetClient_UnsetHeaderEnvVarErrors ensures a header referencing an unset
// env var fails getClient loudly (naming the var) instead of silently sending
// an empty header that would bounce at the proxy — finding #1.
func TestGetClient_UnsetHeaderEnvVarErrors(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "k"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerHeader(config.DefaultServerName, "CF-Access-Client-Secret", "${CIX_DEFINITELY_UNSET_VAR}"); err != nil {
		t.Fatal(err)
	}
	// Deliberately do NOT set CIX_DEFINITELY_UNSET_VAR.
	withFlags(t, "", "", "")

	_, err := getClient()
	if err == nil {
		t.Fatal("expected getClient to fail on an unset header env var")
	}
	if !strings.Contains(err.Error(), "CIX_DEFINITELY_UNSET_VAR") {
		t.Errorf("error should name the missing variable, got %v", err)
	}
}

// TestGetClient_InvalidHeaderErrors ensures a malformed header (after env
// expansion) fails getClient loudly and never echoes the value.
func TestGetClient_InvalidHeaderErrors(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "k"); err != nil {
		t.Fatal(err)
	}
	// Header value resolves to one containing CRLF via the env var (the config
	// setter itself would reject a literal CRLF, but expansion happens later).
	if err := config.SetServerHeader(config.DefaultServerName, "X-Bad", "${CIX_TEST_BAD}"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CIX_TEST_BAD", "line1\r\nInjected: 1")
	withFlags(t, "", "", "")

	_, err := getClient()
	if err == nil {
		t.Fatal("expected getClient to reject a CRLF-injected header value")
	}
	if strings.Contains(err.Error(), "Injected") {
		t.Errorf("error must not echo the header value: %v", err)
	}
}
