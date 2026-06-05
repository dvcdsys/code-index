package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDoSendsCustomHeaders verifies the authenticated request path attaches
// configured custom headers in addition to the cix Bearer.
func TestDoSendsCustomHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "realkey")
	c.SetCustomHeaders(map[string]string{
		"CF-Access-Client-Id":     "abc.access",
		"CF-Access-Client-Secret": "s3cr3t",
	})

	resp, err := c.do("GET", "/api/v1/status", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	if got.Get("CF-Access-Client-Id") != "abc.access" {
		t.Errorf("CF-Access-Client-Id = %q, want abc.access", got.Get("CF-Access-Client-Id"))
	}
	if got.Get("CF-Access-Client-Secret") != "s3cr3t" {
		t.Errorf("CF-Access-Client-Secret = %q, want s3cr3t", got.Get("CF-Access-Client-Secret"))
	}
	if got.Get("Authorization") != "Bearer realkey" {
		t.Errorf("Authorization = %q, want Bearer realkey", got.Get("Authorization"))
	}
}

// TestHealthSendsCustomHeaders verifies the /health probe also carries custom
// headers — otherwise it would be bounced at an authenticating proxy.
func TestHealthSendsCustomHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	c.SetCustomHeaders(map[string]string{"CF-Access-Client-Id": "abc.access"})

	if err := c.Health(); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Get("CF-Access-Client-Id") != "abc.access" {
		t.Errorf("/health missing custom header; got %q", got.Get("CF-Access-Client-Id"))
	}
}

// TestCustomHeadersDoNotOverrideAuthorization ensures a stray custom
// "Authorization" cannot clobber the cix Bearer (cix-managed headers win).
func TestCustomHeadersDoNotOverrideAuthorization(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "realkey")
	c.SetCustomHeaders(map[string]string{"Authorization": "Bearer EVIL"})

	resp, err := c.do("GET", "/api/v1/status", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()

	if got.Get("Authorization") != "Bearer realkey" {
		t.Errorf("Authorization = %q, want Bearer realkey (custom must not win)", got.Get("Authorization"))
	}
}

// TestNoCustomHeadersIsCleanRequest confirms the opt-in nature: with none set,
// only the cix-managed headers go out.
func TestNoCustomHeadersIsCleanRequest(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	resp, err := c.do("GET", "/api/v1/status", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if got.Get("Authorization") != "Bearer k" {
		t.Errorf("Authorization = %q, want Bearer k", got.Get("Authorization"))
	}
}
