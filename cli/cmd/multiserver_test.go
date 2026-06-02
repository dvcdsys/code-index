package cmd

import (
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/config"
)

// isolateConfig points config.Load() at a throwaway HOME and resets the
// singleton before and after the test. CIX_* env vars are unset so a
// developer with these set in their shell does not get spurious test
// failures — tests that need env overrides set them explicitly.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CIX_SERVER", "")
	t.Setenv("CIX_API_URL", "")
	t.Setenv("CIX_API_KEY", "")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// withFlags temporarily sets the global --server/--api-url/--api-key vars and
// restores them on cleanup.
func withFlags(t *testing.T, server, url, key string) {
	t.Helper()
	ps, pu, pk := serverName, apiURL, apiKey
	serverName, apiURL, apiKey = server, url, key
	t.Cleanup(func() { serverName, apiURL, apiKey = ps, pu, pk })
}

func TestGetClient_ServerFlagSelectsServer(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL("corp", "https://corp.example"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey("corp", "corp-key"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "corp", "", "")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "corp.example") {
		t.Errorf("BaseURL = %q, want corp.example", c.BaseURL())
	}
}

func TestGetClient_DefaultServerWhenNoFlag(t *testing.T) {
	isolateConfig(t)
	// Seed the default server with a key so resolution succeeds.
	if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "dk"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "", "", "")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "localhost:21847") {
		t.Errorf("BaseURL = %q, want default localhost", c.BaseURL())
	}
}

func TestGetClient_UnknownServerErrors(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerKey(config.DefaultServerName, "dk"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "ghost", "", "")

	_, err := getClient()
	if err == nil {
		t.Fatal("expected error for unknown --server")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should mention the unknown server", err.Error())
	}
}

func TestGetClient_ServerKeyMissingError(t *testing.T) {
	isolateConfig(t)
	// corp has a URL but no key, and no --api-key override.
	if err := config.SetServerURL("corp", "https://corp.example"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "corp", "", "")

	_, err := getClient()
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	if !strings.Contains(err.Error(), "corp") || !strings.Contains(err.Error(), "API key") {
		t.Errorf("error %q should name the server and mention API key", err.Error())
	}
}

func TestRunConfigSet_ServerKeys(t *testing.T) {
	isolateConfig(t)

	mustSet(t, "server.corp.url", "https://corp")
	mustSet(t, "server.corp.key", "ck")

	cfg, _ := config.Load()
	s, ok := cfg.GetServer("corp")
	if !ok || s.URL != "https://corp" || s.Key != "ck" {
		t.Fatalf("corp server = %+v, ok=%v", s, ok)
	}
}

func TestRunConfigSet_DefaultServer(t *testing.T) {
	isolateConfig(t)
	mustSet(t, "server.corp.url", "https://corp")
	mustSet(t, "default_server", "corp")

	cfg, _ := config.Load()
	if cfg.DefaultServer != "corp" {
		t.Errorf("DefaultServer = %q, want corp", cfg.DefaultServer)
	}

	// Unknown default is rejected.
	if _, err := captureOutput(func() error {
		return runConfigSet(nil, []string{"default_server", "ghost"})
	}); err == nil {
		t.Error("expected error setting default_server to unknown alias")
	}
}

func TestRunConfigSet_ApiAliasMapsToDefault(t *testing.T) {
	isolateConfig(t)
	mustSet(t, "api.url", "http://aliased:1234")
	mustSet(t, "api.key", "ak")

	cfg, _ := config.Load()
	s, ok := cfg.DefaultServerEntry()
	if !ok || s.URL != "http://aliased:1234" || s.Key != "ak" {
		t.Fatalf("default server = %+v, ok=%v", s, ok)
	}
	// The legacy api block must remain empty (migrated/never persisted).
	if cfg.API.URL != "" || cfg.API.Key != "" {
		t.Errorf("API block = %+v, want empty", cfg.API)
	}
}

func TestRunConfigUnset(t *testing.T) {
	isolateConfig(t)
	mustSet(t, "server.corp.url", "https://corp")
	mustSet(t, "server.corp.key", "ck")

	// Clear just the key.
	if _, err := captureOutput(func() error {
		return runConfigUnset(nil, []string{"server.corp.key"})
	}); err != nil {
		t.Fatalf("unset key: %v", err)
	}
	cfg, _ := config.Load()
	if s, ok := cfg.GetServer("corp"); !ok || s.Key != "" || s.URL != "https://corp" {
		t.Fatalf("after key unset corp = %+v, ok=%v", s, ok)
	}

	// Remove the whole server.
	if _, err := captureOutput(func() error {
		return runConfigUnset(nil, []string{"server.corp"})
	}); err != nil {
		t.Fatalf("unset server: %v", err)
	}
	cfg, _ = config.Load()
	if _, ok := cfg.GetServer("corp"); ok {
		t.Error("corp server should have been removed")
	}

	// Unknown key shape errors.
	if _, err := captureOutput(func() error {
		return runConfigUnset(nil, []string{"watcher.debounce_ms"})
	}); err == nil {
		t.Error("expected error for unsupported unset key")
	}
}

// --- Env-override tests (step 8) -------------------------------------------

func TestGetClient_EnvServerSelectsAlias(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL("corp", "https://corp.example"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey("corp", "corp-key"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "", "", "")
	t.Setenv("CIX_SERVER", "corp")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "corp.example") {
		t.Errorf("BaseURL = %q, want corp.example (selected via CIX_SERVER)", c.BaseURL())
	}
}

func TestGetClient_FlagBeatsCixServerEnv(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL("alpha", "https://alpha"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey("alpha", "ak"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerURL("beta", "https://beta"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey("beta", "bk"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "alpha", "", "")
	t.Setenv("CIX_SERVER", "beta")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "alpha") {
		t.Errorf("BaseURL = %q, want alpha (--server overrides CIX_SERVER)", c.BaseURL())
	}
}

func TestGetClient_EnvAPIURLOverridesResolvedURL(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "k"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "", "", "")
	t.Setenv("CIX_API_URL", "http://env-url:9999")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "env-url:9999") {
		t.Errorf("BaseURL = %q, want env-url:9999", c.BaseURL())
	}
}

func TestGetClient_FlagBeatsCixAPIURLEnv(t *testing.T) {
	isolateConfig(t)
	if err := config.SetServerURL(config.DefaultServerName, "http://file-url"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetServerKey(config.DefaultServerName, "k"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "", "http://flag-url:1234", "")
	t.Setenv("CIX_API_URL", "http://env-url:9999")

	c, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if !strings.Contains(c.BaseURL(), "flag-url:1234") {
		t.Errorf("BaseURL = %q, want flag-url:1234 (--api-url > env)", c.BaseURL())
	}
}

func TestGetClient_EnvAPIKeyFillsMissingKey(t *testing.T) {
	isolateConfig(t)
	// File has URL but no key — env supplies it.
	if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
		t.Fatal(err)
	}
	withFlags(t, "", "", "")
	t.Setenv("CIX_API_KEY", "env-key-secret")

	if _, err := getClient(); err != nil {
		t.Fatalf("getClient should succeed with CIX_API_KEY: %v", err)
	}
}

func mustSet(t *testing.T, key, value string) {
	t.Helper()
	if _, err := captureOutput(func() error {
		return runConfigSet(nil, []string{key, value})
	}); err != nil {
		t.Fatalf("config set %s %s: %v", key, value, err)
	}
}
