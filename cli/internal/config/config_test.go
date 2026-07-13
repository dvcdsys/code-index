package config

import (
	"os"
	"path/filepath"
	"strings"
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

	// With no config file, the implicit localhost default server is seeded.
	if len(cfg.Servers) != 1 {
		t.Fatalf("Servers len = %d, want 1 (seeded default)", len(cfg.Servers))
	}
	if cfg.DefaultServer != DefaultServerName {
		t.Errorf("DefaultServer = %q, want %q", cfg.DefaultServer, DefaultServerName)
	}
	if cfg.Servers[0].Name != DefaultServerName || cfg.Servers[0].URL != "http://localhost:21847" {
		t.Errorf("default server = %+v, want {default, localhost:21847, <no key>}", cfg.Servers[0])
	}
	if cfg.Servers[0].Key != "" {
		t.Errorf("default server Key = %q, want empty", cfg.Servers[0].Key)
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
	if cfg.Indexing.BatchSize != 20 {
		t.Errorf("Indexing.BatchSize = %d, want 20", cfg.Indexing.BatchSize)
	}
	if cfg.Watcher.SyncIntervalMins != 5 {
		t.Errorf("Watcher.SyncIntervalMins = %d, want 5", cfg.Watcher.SyncIntervalMins)
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
  sync_interval_mins: 10
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

	// Legacy api: block migrates to a single "default" server.
	if len(cfg.Servers) != 1 {
		t.Fatalf("Servers len = %d, want 1 (migrated from api:)", len(cfg.Servers))
	}
	if cfg.DefaultServer != DefaultServerName {
		t.Errorf("DefaultServer = %q, want %q", cfg.DefaultServer, DefaultServerName)
	}
	if cfg.Servers[0].URL != "http://myserver:9000" {
		t.Errorf("default server URL = %q, want %q", cfg.Servers[0].URL, "http://myserver:9000")
	}
	if cfg.Servers[0].Key != "secret-key-123" {
		t.Errorf("default server Key = %q, want %q", cfg.Servers[0].Key, "secret-key-123")
	}
	// The legacy api block must be cleared after migration.
	if cfg.API.URL != "" || cfg.API.Key != "" {
		t.Errorf("API block = %+v, want cleared after migration", cfg.API)
	}
	if cfg.Watcher.Enabled {
		t.Error("Watcher.Enabled = true, want false")
	}
	if cfg.Watcher.DebounceMS != 2000 {
		t.Errorf("Watcher.DebounceMS = %d, want 2000", cfg.Watcher.DebounceMS)
	}
	if cfg.Watcher.SyncIntervalMins != 10 {
		t.Errorf("Watcher.SyncIntervalMins = %d, want 10", cfg.Watcher.SyncIntervalMins)
	}
	// server.port / server.cache_ttl removed (dead fields, step 13). The
	// `server:` block in the input file is parsed-then-dropped by koanf —
	// no assertion needed beyond "this load did not error".
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

	// api.key-only legacy file migrates to the default server, with the URL
	// falling back to the historical localhost default.
	if len(cfg.Servers) != 1 {
		t.Fatalf("Servers len = %d, want 1", len(cfg.Servers))
	}
	if cfg.Servers[0].Key != "partial-key" {
		t.Errorf("default server Key = %q, want %q", cfg.Servers[0].Key, "partial-key")
	}
	if cfg.Servers[0].URL != "http://localhost:21847" {
		t.Errorf("default server URL = %q, want default http://localhost:21847", cfg.Servers[0].URL)
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
		Servers: []ServerEntry{
			{Name: DefaultServerName, URL: "http://saved:8888", Key: "saved-key"},
		},
		DefaultServer: DefaultServerName,
		Watcher: WatcherConfig{
			Enabled:          false,
			DebounceMS:       1234,
			SyncIntervalMins: 15,
			ExcludePatterns:  []string{".git", "vendor"},
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

	if len(got.Servers) != 1 {
		t.Fatalf("Servers len = %d, want 1", len(got.Servers))
	}
	if got.Servers[0].URL != want.Servers[0].URL {
		t.Errorf("server URL = %q, want %q", got.Servers[0].URL, want.Servers[0].URL)
	}
	if got.Servers[0].Key != want.Servers[0].Key {
		t.Errorf("server Key = %q, want %q", got.Servers[0].Key, want.Servers[0].Key)
	}
	if got.Watcher.Enabled != want.Watcher.Enabled {
		t.Errorf("Watcher.Enabled = %v, want %v", got.Watcher.Enabled, want.Watcher.Enabled)
	}
	if got.Watcher.DebounceMS != want.Watcher.DebounceMS {
		t.Errorf("Watcher.DebounceMS = %d, want %d", got.Watcher.DebounceMS, want.Watcher.DebounceMS)
	}
	if got.Watcher.SyncIntervalMins != want.Watcher.SyncIntervalMins {
		t.Errorf("Watcher.SyncIntervalMins = %d, want %d", got.Watcher.SyncIntervalMins, want.Watcher.SyncIntervalMins)
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
	// server.port / server.cache_ttl removed (dead fields, step 13). The
	// legacy `server:` block must still parse without error, but its
	// values are dropped — koanf silently ignores them on unmarshal.
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

// TestAddProject_UpdatesAutoWatchOnExisting guards the upsert path: calling
// AddProject for a path already in the config must persist a changed
// auto_watch flag (it used to mutate a loop-variable copy and save nothing).
func TestAddProject_UpdatesAutoWatchOnExisting(t *testing.T) {
	isolateHome(t)

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if err := AddProject("/srv/proj", false); err != nil {
		t.Fatal(err)
	}
	if err := AddProject("/srv/proj", true); err != nil {
		t.Fatalf("AddProject update error = %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("Projects = %v, want exactly one entry", cfg.Projects)
	}
	if !cfg.Projects[0].AutoWatch {
		t.Error("AutoWatch = false, want true — the flag update was not persisted")
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

// TestMigrate_ReSavesNewFormat verifies a legacy api: file is rewritten to the
// servers: layout on disk (api: dropped) the first time it is loaded.
func TestMigrate_ReSavesNewFormat(t *testing.T) {
	home := isolateHome(t)

	cfgDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "api:\n  url: \"http://legacy:9000\"\n  key: \"legacy-key\"\n"
	path := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "servers:") {
		t.Errorf("re-saved config missing servers::\n%s", got)
	}
	if !strings.Contains(got, "default_server: default") {
		t.Errorf("re-saved config missing default_server:\n%s", got)
	}
	if strings.Contains(got, "api:") {
		t.Errorf("re-saved config should not contain api::\n%s", got)
	}
	if !strings.Contains(got, "http://legacy:9000") || !strings.Contains(got, "legacy-key") {
		t.Errorf("re-saved config lost url/key:\n%s", got)
	}
}

func TestResolveServer(t *testing.T) {
	c := &Config{
		Servers: []ServerEntry{
			{Name: "default", URL: "http://local", Key: "k1"},
			{Name: "corp", URL: "http://corp", Key: "k2"},
		},
		DefaultServer: "default",
	}

	// Empty name → default server.
	s, err := c.ResolveServer("")
	if err != nil || s.Name != "default" {
		t.Errorf("ResolveServer(\"\") = %v, %v; want default", s, err)
	}
	// Named alias.
	s, err = c.ResolveServer("corp")
	if err != nil || s.URL != "http://corp" {
		t.Errorf("ResolveServer(corp) = %v, %v; want corp URL", s, err)
	}
	// Unknown → error listing available names.
	_, err = c.ResolveServer("nope")
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
	for _, want := range []string{"nope", "default", "corp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestResolveServer_DanglingDefault falls back to the first server when
// DefaultServer points at a missing entry.
func TestResolveServer_DanglingDefault(t *testing.T) {
	c := &Config{
		Servers:       []ServerEntry{{Name: "only", URL: "http://only"}},
		DefaultServer: "ghost",
	}
	s, err := c.ResolveServer("")
	if err != nil || s.Name != "only" {
		t.Errorf("ResolveServer(\"\") with dangling default = %v, %v; want first server", s, err)
	}
}

func TestSetServer_UpsertAndDefault(t *testing.T) {
	isolateHome(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	// Adding a server's URL creates the entry. The seeded localhost default
	// already exists, so the new one does NOT become default.
	if err := SetServerURL("corp", "http://corp"); err != nil {
		t.Fatalf("SetServerURL: %v", err)
	}
	if err := SetServerKey("corp", "corp-key"); err != nil {
		t.Fatalf("SetServerKey: %v", err)
	}

	cfg, _ := Load()
	s, ok := cfg.GetServer("corp")
	if !ok || s.URL != "http://corp" || s.Key != "corp-key" {
		t.Fatalf("corp server = %+v, ok=%v", s, ok)
	}
	if cfg.DefaultServer != DefaultServerName {
		t.Errorf("DefaultServer = %q, want %q (unchanged)", cfg.DefaultServer, DefaultServerName)
	}

	// Updating url again must not duplicate the entry.
	if err := SetServerURL("corp", "http://corp2"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if n := len(cfg.Servers); n != 2 {
		t.Errorf("Servers len = %d, want 2 (default + corp)", n)
	}
}

func TestSetDefaultServer(t *testing.T) {
	isolateHome(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if err := SetServerURL("corp", "http://corp"); err != nil {
		t.Fatal(err)
	}

	// Unknown name rejected.
	if err := SetDefaultServer("ghost"); err == nil {
		t.Error("expected error setting default to unknown server")
	}
	// Known name switches default.
	if err := SetDefaultServer("corp"); err != nil {
		t.Fatalf("SetDefaultServer: %v", err)
	}
	cfg, _ := Load()
	if cfg.DefaultServer != "corp" {
		t.Errorf("DefaultServer = %q, want corp", cfg.DefaultServer)
	}
}

func TestRemoveServer_ReassignsDefault(t *testing.T) {
	isolateHome(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if err := SetServerURL("corp", "http://corp"); err != nil {
		t.Fatal(err)
	}
	// default is still "default"; remove it → reassign to remaining "corp".
	reassigned, err := RemoveServer(DefaultServerName)
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if reassigned != "corp" {
		t.Errorf("reassignedTo = %q, want corp", reassigned)
	}
	cfg, _ := Load()
	if cfg.DefaultServer != "corp" {
		t.Errorf("DefaultServer = %q, want corp", cfg.DefaultServer)
	}
	if _, ok := cfg.GetServer(DefaultServerName); ok {
		t.Error("default server should have been removed")
	}

	// Removing an unknown server errors.
	if _, err := RemoveServer("ghost"); err == nil {
		t.Error("expected error removing unknown server")
	}
}

func TestValidateServerName(t *testing.T) {
	isolateHome(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "has.dot", "has space"} {
		if err := SetServerURL(bad, "http://x"); err == nil {
			t.Errorf("SetServerURL(%q) expected validation error", bad)
		}
	}
	if err := SetServerURL("ok_name-1", "http://x"); err != nil {
		t.Errorf("SetServerURL(valid) unexpected error: %v", err)
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
