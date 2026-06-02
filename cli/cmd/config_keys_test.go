package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/config"
)

func TestRenderConfigKeys_Snapshot(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerEntry{
			{Name: "default", URL: "http://localhost:21847", Key: "cix_secret_xyz"},
		},
		DefaultServer: "default",
		Watcher: config.WatcherConfig{
			Enabled:          true,
			DebounceMS:       5000,
			ExcludePatterns:  []string{"node_modules"},
			SyncIntervalMins: 5,
		},
		Indexing: config.IndexingConfig{BatchSize: 20, StreamingIdleTimeoutSec: 30},
	}

	var buf bytes.Buffer
	if err := renderConfigKeys(&buf, cfg); err != nil {
		t.Fatalf("renderConfigKeys: %v", err)
	}

	got := buf.String()

	// Header must come first.
	if !strings.HasPrefix(got, "KEY") {
		t.Errorf("output should start with header row, got %q", firstLine(got))
	}

	// Every settable scalar must appear.
	mustContain := []string{
		"default_server",
		"watcher.enabled",
		"watcher.debounce_ms",
		"watcher.exclude",
		"watcher.sync_interval_mins",
		"indexing.batch_size",
		"indexing.streaming_idle_timeout_sec",
		"CIX_SERVER", // env tag on default_server
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}

	// Slice-of-struct leaves are skipped from the listing.
	for _, skip := range []string{"\nservers ", "\nprojects "} {
		if strings.Contains(got, skip) {
			t.Errorf("output should not list slice-of-struct leaf row %q", skip)
		}
	}

	// Sensitive value MUST NOT leak.
	if strings.Contains(got, "cix_secret_xyz") {
		t.Errorf("sensitive key value leaked into output")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
