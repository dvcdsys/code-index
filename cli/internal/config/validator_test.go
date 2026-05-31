package config

import (
	"strings"
	"testing"
)

// TestValidate_OK exercises every default value to confirm the baseline
// config passes validation. Failure here means a `default:"…"` tag is
// outside its own `validate:"…"` range — an internal contradiction in the
// schema, not a user error.
func TestValidate_OK(t *testing.T) {
	cfg := &Config{
		Servers: []ServerEntry{
			{Name: "default", URL: "http://localhost:21847", Key: "k"},
		},
		DefaultServer: "default",
		Watcher: WatcherConfig{
			Enabled:          true,
			DebounceMS:       5000,
			ExcludePatterns:  []string{".git"},
			SyncIntervalMins: 5,
		},
		Indexing: IndexingConfig{
			BatchSize:               20,
			StreamingIdleTimeoutSec: 30,
		},
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("Validate on canonical defaults: %v", err)
	}
}

func TestValidate_Failures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		// expectKey is the dotted-key fragment we expect in the error
		// message; if absent we know goPathToKey is wrong.
		expectKey string
	}{
		{
			name:      "debounce too low",
			mut:       func(c *Config) { c.Watcher.DebounceMS = 50 },
			expectKey: "watcher.debounce_ms",
		},
		{
			name:      "debounce too high",
			mut:       func(c *Config) { c.Watcher.DebounceMS = 999999 },
			expectKey: "watcher.debounce_ms",
		},
		{
			name:      "sync interval zero",
			mut:       func(c *Config) { c.Watcher.SyncIntervalMins = 0 },
			expectKey: "watcher.sync_interval_mins",
		},
		{
			name:      "batch size zero",
			mut:       func(c *Config) { c.Indexing.BatchSize = 0 },
			expectKey: "indexing.batch_size",
		},
		{
			name:      "streaming idle negative",
			mut:       func(c *Config) { c.Indexing.StreamingIdleTimeoutSec = -1 },
			expectKey: "indexing.streaming_idle_timeout_sec",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseConfig()
			tc.mut(cfg)
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.expectKey) {
				t.Errorf("error %q does not reference key %q", err, tc.expectKey)
			}
		})
	}
}

func TestValidate_NilConfig(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Errorf("expected error on nil config")
	}
}

func validBaseConfig() *Config {
	return &Config{
		Servers: []ServerEntry{
			{Name: "default", URL: "http://localhost:21847"},
		},
		DefaultServer: "default",
		Watcher: WatcherConfig{
			Enabled:          true,
			DebounceMS:       5000,
			ExcludePatterns:  []string{".git"},
			SyncIntervalMins: 5,
		},
		Indexing: IndexingConfig{
			BatchSize:               20,
			StreamingIdleTimeoutSec: 30,
		},
	}
}
