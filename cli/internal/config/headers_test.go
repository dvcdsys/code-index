package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetServerHeaderRoundTrip(t *testing.T) {
	withIsolatedHome(t)
	if err := SetServerURL("corp", "https://corp.example"); err != nil {
		t.Fatal(err)
	}
	if err := SetServerHeader("corp", "CF-Access-Client-Id", "abc.access"); err != nil {
		t.Fatalf("SetServerHeader: %v", err)
	}
	if err := SetServerHeader("corp", "CF-Access-Client-Secret", "${CIX_SECRET}"); err != nil {
		t.Fatalf("SetServerHeader: %v", err)
	}

	ResetForTesting()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	s, ok := cfg.GetServer("corp")
	if !ok {
		t.Fatal("server corp missing after reload")
	}
	if got := s.Headers["CF-Access-Client-Id"]; got != "abc.access" {
		t.Errorf("CF-Access-Client-Id = %q, want abc.access", got)
	}
	// Secrets are stored verbatim (the ${ENV} placeholder), never expanded on disk.
	if got := s.Headers["CF-Access-Client-Secret"]; got != "${CIX_SECRET}" {
		t.Errorf("secret header = %q, want literal ${CIX_SECRET}", got)
	}

	// And the placeholder must actually be on disk, not a resolved value.
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".cix", "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "${CIX_SECRET}") {
		t.Errorf("config file should retain the ${CIX_SECRET} placeholder:\n%s", raw)
	}
}

func TestUnsetServerHeader(t *testing.T) {
	withIsolatedHome(t)
	if err := SetServerURL("corp", "https://corp.example"); err != nil {
		t.Fatal(err)
	}
	if err := SetServerHeader("corp", "X-One", "1"); err != nil {
		t.Fatal(err)
	}
	if err := SetServerHeader("corp", "X-Two", "2"); err != nil {
		t.Fatal(err)
	}
	if err := UnsetServerHeader("corp", "X-One"); err != nil {
		t.Fatalf("UnsetServerHeader: %v", err)
	}
	cfg, _ := Load()
	s, _ := cfg.GetServer("corp")
	if _, ok := s.Headers["X-One"]; ok {
		t.Error("X-One should be removed")
	}
	if s.Headers["X-Two"] != "2" {
		t.Error("X-Two should remain")
	}

	// Removing the last header drops the map entirely (omitempty round-trip).
	if err := UnsetServerHeader("corp", "X-Two"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	s, _ = cfg.GetServer("corp")
	if s.Headers != nil {
		t.Errorf("Headers should be nil after last removal, got %v", s.Headers)
	}

	// Unsetting a missing header / server is a no-op / clean error.
	if err := UnsetServerHeader("corp", "X-Missing"); err != nil {
		t.Errorf("unset missing header should be no-op, got %v", err)
	}
	if err := UnsetServerHeader("nope", "X"); err == nil {
		t.Error("unset on missing server should error")
	}
}

func TestValidateHeader(t *testing.T) {
	cases := []struct {
		name, value string
		ok          bool
	}{
		{"CF-Access-Client-Id", "abc.access", true},
		{"X-Token", "${VAR}", true},
		{"Content.Type", "x", true},         // dot is a valid token char
		{"", "x", false},                    // empty name
		{"Bad Name", "x", false},            // space in name
		{"Bad:Name", "x", false},            // colon in name
		{"X-Inject", "a\r\nEvil: 1", false}, // CRLF in value
		{"X-LF", "a\nb", false},             // LF in value
	}
	for _, c := range cases {
		err := ValidateHeader(c.name, c.value)
		if c.ok && err != nil {
			t.Errorf("ValidateHeader(%q,…) unexpected error: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ValidateHeader(%q,…) expected error, got nil", c.name)
		}
	}
}

func TestSetServerHeaderRejectsInvalid(t *testing.T) {
	withIsolatedHome(t)
	if err := SetServerHeader("corp", "Bad Name", "v"); err == nil {
		t.Error("expected error for invalid header name")
	}
	if err := SetServerHeader("corp", "X", "a\r\nb"); err == nil {
		t.Error("expected error for CRLF in value")
	}
}
