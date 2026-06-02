package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoaderParity asserts loadWithKoanf produces an identical *Config to
// the legacy Load() path for every scenario the legacy loader covers. Once
// this is green, step 5 of the refactor (flipping Load to call koanf) is
// purely mechanical.
//
// Each scenario runs in a fresh HOME so the legacy loader's "re-save on
// migration" side effect does not leak into the koanf run, and vice versa.
func TestLoaderParity(t *testing.T) {
	scenarios := []struct {
		name string
		file string // empty = no file on disk
	}{
		{
			name: "no_file",
			file: "",
		},
		{
			name: "partial_file_only_debounce",
			file: "watcher:\n  debounce_ms: 1234\n",
		},
		{
			name: "full_file_new_format",
			file: `servers:
  - name: default
    url: http://localhost:21847
    key: cix_abc123
  - name: corporate
    url: https://cix.corp.internal
    key: cix_xyz789
default_server: corporate
watcher:
  enabled: false
  debounce_ms: 2000
  sync_interval_mins: 10
  exclude:
    - vendor
    - tmp
indexing:
  batchsize: 50
  streaming_idle_timeout_sec: 45
projects:
  - path: /home/user/proj
    auto_watch: true
`,
		},
		{
			name: "legacy_api_only",
			file: "api:\n  url: http://legacy.example\n  key: cix_legacy\n",
		},
		{
			name: "legacy_lowercase_viper_keys",
			file: `watcher:
  debouncems: 9999
  excludepatterns:
    - foo
    - bar
  sync_interval_mins: 7
server:
  cachettl: 600
projects:
  - path: /p
    autowatch: true
`,
		},
		{
			name: "servers_no_default",
			file: `servers:
  - name: alpha
    url: http://alpha
    key: a
  - name: beta
    url: http://beta
    key: b
`,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			legacyCfg := runLoaderInTempHome(t, sc.file, false)
			koanfCfg := runLoaderInTempHome(t, sc.file, true)
			if !configsEqual(legacyCfg, koanfCfg) {
				t.Errorf("legacy != koanf\n  legacy: %s\n  koanf:  %s",
					dumpConfig(legacyCfg), dumpConfig(koanfCfg))
			}
		})
	}
}

// runLoaderInTempHome sets HOME to a fresh tempdir, optionally writes a
// config file there, resets the legacy singleton, and runs the requested
// loader. Both loaders use os.UserHomeDir() → $HOME on Unix.
func runLoaderInTempHome(t *testing.T, file string, useKoanf bool) *Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	cixDir := filepath.Join(home, ".cix")
	if err := os.MkdirAll(cixDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if file != "" {
		if err := os.WriteFile(filepath.Join(cixDir, "config.yaml"), []byte(file), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	ResetForTesting()

	if useKoanf {
		cfg, _, err := loadWithKoanf()
		if err != nil {
			t.Fatalf("loadWithKoanf: %v", err)
		}
		return cfg
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// configsEqual compares two *Config values after normalizing nil vs
// empty-slice for fields the two loaders may zero-init differently
// (Projects, ExcludePatterns, Servers — none should diverge but
// reflect.DeepEqual treats nil != []T{}).
func configsEqual(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(normalizeForCompare(*a), normalizeForCompare(*b))
}

func normalizeForCompare(c Config) Config {
	if c.Projects == nil {
		c.Projects = []ProjectEntry{}
	}
	if c.Servers == nil {
		c.Servers = []ServerEntry{}
	}
	if c.Watcher.ExcludePatterns == nil {
		c.Watcher.ExcludePatterns = []string{}
	}
	return c
}

func dumpConfig(c *Config) string {
	return fmt.Sprintf("Servers=%+v DefaultServer=%q API=%+v Watcher=%+v Indexing=%+v Projects=%+v",
		c.Servers, c.DefaultServer, c.API, c.Watcher, c.Indexing, c.Projects)
}
