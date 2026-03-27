package config

import (
	"os"
	"path/filepath"
	"testing"
)

// resetState clears the package-level singleton so each test starts clean.
func resetState() {
	globalConfig = nil
	configPath = ""
}

// isolateHome sets HOME to a temp directory so Load() uses it instead of the
// real user home. Returns the temp dir path.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "") // prevent XDG override on some systems
	t.Cleanup(resetState)
	return dir
}

func TestLoad_Defaults(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.API.URL != "http://localhost:21847" {
		t.Errorf("API.URL = %q, want %q", cfg.API.URL, "http://localhost:21847")
	}
	if cfg.API.Key != "" {
		t.Errorf("API.Key = %q, want empty", cfg.API.Key)
	}
	if !cfg.Watcher.Enabled {
		t.Error("Watcher.Enabled = false, want true")
	}
	if cfg.Watcher.DebounceMS != 5000 {
		t.Errorf("Watcher.DebounceMS = %d, want 5000", cfg.Watcher.DebounceMS)
	}
	if len(cfg.Watcher.ExcludePatterns) == 0 {
		t.Error("Watcher.ExcludePatterns is empty, want default list")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.CacheTTL != 300 {
		t.Errorf("Server.CacheTTL = %d, want 300", cfg.Server.CacheTTL)
	}
	if cfg.Indexing.BatchSize != 20 {
		t.Errorf("Indexing.BatchSize = %d, want 20", cfg.Indexing.BatchSize)
	}
}

func TestLoad_FromFile(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `
api:
  url: "http://myserver:9000"
  key: "secret-key-123"
watcher:
  enabled: false
  debounce_ms: 2000
server:
  port: 3000
  cache_ttl: 60
indexing:
  batchsize: 5
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.API.URL != "http://myserver:9000" {
		t.Errorf("API.URL = %q, want %q", cfg.API.URL, "http://myserver:9000")
	}
	if cfg.API.Key != "secret-key-123" {
		t.Errorf("API.Key = %q, want %q", cfg.API.Key, "secret-key-123")
	}
	if cfg.Watcher.Enabled {
		t.Error("Watcher.Enabled = true, want false")
	}
	if cfg.Watcher.DebounceMS != 2000 {
		t.Errorf("Watcher.DebounceMS = %d, want 2000", cfg.Watcher.DebounceMS)
	}
	if cfg.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want 3000", cfg.Server.Port)
	}
	if cfg.Server.CacheTTL != 60 {
		t.Errorf("Server.CacheTTL = %d, want 60", cfg.Server.CacheTTL)
	}
	if cfg.Indexing.BatchSize != 5 {
		t.Errorf("Indexing.BatchSize = %d, want 5", cfg.Indexing.BatchSize)
	}
}

// TestLoad_PartialFile checks that fields absent from the config file fall back
// to their defaults — verifying that viper's Unmarshal merges correctly.
func TestLoad_PartialFile(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Only override api.key; everything else should use defaults.
	content := `
api:
  key: "partial-key"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.API.Key != "partial-key" {
		t.Errorf("API.Key = %q, want %q", cfg.API.Key, "partial-key")
	}
	// Default must still apply for the URL.
	if cfg.API.URL != "http://localhost:21847" {
		t.Errorf("API.URL = %q, want default http://localhost:21847", cfg.API.URL)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want default 8080", cfg.Server.Port)
	}
	if cfg.Indexing.BatchSize != 20 {
		t.Errorf("Indexing.BatchSize = %d, want default 20", cfg.Indexing.BatchSize)
	}
}

// TestLoad_NoConfigFile checks that a missing config file is silently ignored
// and does not return an error.
func TestLoad_NoConfigFile(t *testing.T) {
	isolateHome(t)
	// No config file is written — directory may not even exist yet.

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with no config file returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}
}

// TestLoad_InvalidYAML checks that a malformed config file returns an error.
func TestLoad_InvalidYAML(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(":\t: invalid:yaml:"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with invalid YAML expected error, got nil")
	}
}

// TestLoad_Singleton verifies that Load returns the same pointer on repeated
// calls without re-reading the file.
func TestLoad_Singleton(t *testing.T) {
	isolateHome(t)

	cfg1, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg1 != cfg2 {
		t.Error("expected Load() to return the same pointer on repeated calls")
	}
}

// TestSave_RoundTrip saves a config and reloads it, checking all fields survive.
func TestSave_RoundTrip(t *testing.T) {
	isolateHome(t)

	// First Load initialises viper with the config path.
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	want := &Config{
		API: APIConfig{
			URL: "http://saved:8888",
			Key: "saved-key",
		},
		Watcher: WatcherConfig{
			Enabled:         false,
			DebounceMS:      1234,
			ExcludePatterns: []string{".git", "vendor"},
		},
		Server: ServerConfig{
			Port:     4444,
			CacheTTL: 99,
		},
		Indexing: IndexingConfig{
			BatchSize: 7,
		},
	}

	if err := Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Reset singleton so Load() re-reads from disk.
	globalConfig = nil

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error = %v", err)
	}

	if got.API.URL != want.API.URL {
		t.Errorf("API.URL = %q, want %q", got.API.URL, want.API.URL)
	}
	if got.API.Key != want.API.Key {
		t.Errorf("API.Key = %q, want %q", got.API.Key, want.API.Key)
	}
	if got.Watcher.Enabled != want.Watcher.Enabled {
		t.Errorf("Watcher.Enabled = %v, want %v", got.Watcher.Enabled, want.Watcher.Enabled)
	}
	if got.Watcher.DebounceMS != want.Watcher.DebounceMS {
		t.Errorf("Watcher.DebounceMS = %d, want %d", got.Watcher.DebounceMS, want.Watcher.DebounceMS)
	}
	if got.Server.Port != want.Server.Port {
		t.Errorf("Server.Port = %d, want %d", got.Server.Port, want.Server.Port)
	}
	if got.Indexing.BatchSize != want.Indexing.BatchSize {
		t.Errorf("Indexing.BatchSize = %d, want %d", got.Indexing.BatchSize, want.Indexing.BatchSize)
	}
}

// TestUnmarshalMapstructureTags verifies that all mapstructure struct tags
// resolve correctly — this is the area most affected by viper's mapstructure
// library swap (mitchellh → go-viper) in v1.19/v1.20.
func TestUnmarshalMapstructureTags(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Use snake_case keys matching the mapstructure tags in the structs.
	content := `
watcher:
  debounce_ms: 7777
  exclude:
    - "a"
    - "b"
indexing:
  batchsize: 13
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Watcher.DebounceMS != 7777 {
		t.Errorf("DebounceMS = %d, want 7777 (mapstructure tag: debounce_ms)", cfg.Watcher.DebounceMS)
	}
	if len(cfg.Watcher.ExcludePatterns) != 2 || cfg.Watcher.ExcludePatterns[0] != "a" {
		t.Errorf("ExcludePatterns = %v, want [a b]", cfg.Watcher.ExcludePatterns)
	}
	if cfg.Indexing.BatchSize != 13 {
		t.Errorf("BatchSize = %d, want 13 (mapstructure tag: batchsize)", cfg.Indexing.BatchSize)
	}
}

// TestLoad_LegacyKeys verifies backward compatibility with viper-generated
// config files that use lowercased field names without underscores.
func TestLoad_LegacyKeys(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `
watcher:
  enabled: true
  debouncems: 2500
  excludepatterns:
    - node_modules
    - .git
server:
  port: 9090
  cachettl: 120
projects:
  - path: /srv/myproject
    autowatch: true
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Watcher.DebounceMS != 2500 {
		t.Errorf("DebounceMS = %d, want 2500 (legacy key: debouncems)", cfg.Watcher.DebounceMS)
	}
	if len(cfg.Watcher.ExcludePatterns) != 2 {
		t.Errorf("ExcludePatterns len = %d, want 2 (legacy key: excludepatterns)", len(cfg.Watcher.ExcludePatterns))
	}
	if cfg.Server.CacheTTL != 120 {
		t.Errorf("CacheTTL = %d, want 120 (legacy key: cachettl)", cfg.Server.CacheTTL)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Server.Port)
	}
	if len(cfg.Projects) != 1 || !cfg.Projects[0].AutoWatch {
		t.Errorf("Projects[0].AutoWatch = false, want true (legacy key: autowatch)")
	}
}

func TestAddProject(t *testing.T) {
	isolateHome(t)

	// Seed viper with a config path via initial Load.
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	if err := AddProject("/srv/proj", true); err != nil {
		t.Fatalf("AddProject error = %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Path != "/srv/proj" {
		t.Errorf("Projects = %v, want one entry with path /srv/proj", cfg.Projects)
	}
	if !cfg.Projects[0].AutoWatch {
		t.Error("AutoWatch = false, want true")
	}
}

// TestAddProject_NoDuplicate verifies that adding the same path twice keeps
// only one entry.
func TestAddProject_NoDuplicate(t *testing.T) {
	isolateHome(t)

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	if err := AddProject("/srv/proj", false); err != nil {
		t.Fatal(err)
	}
	if err := AddProject("/srv/proj", false); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(cfg.Projects))
	}
}

func TestRemoveProject(t *testing.T) {
	isolateHome(t)

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	if err := AddProject("/srv/a", false); err != nil {
		t.Fatal(err)
	}
	if err := AddProject("/srv/b", false); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProject("/srv/a"); err != nil {
		t.Fatalf("RemoveProject error = %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Path != "/srv/b" {
		t.Errorf("after remove, Projects = %v, want only /srv/b", cfg.Projects)
	}
}

func TestGetConfigPath(t *testing.T) {
	home := isolateHome(t)

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, ".cix", "config.yaml")
	if got := GetConfigPath(); got != want {
		t.Errorf("GetConfigPath() = %q, want %q", got, want)
	}
}