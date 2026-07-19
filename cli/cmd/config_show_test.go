package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/cli/internal/config"
)

// TestRenderConfigShow_Snapshot pins the human-readable layout of `cix
// config show`. The expected output is the contract — if a future field
// addition shifts the format, this test forces an intentional update
// instead of silent drift.
//
// CodeQL note: the API key value below ("cix_secret123") never appears in
// the expected output — it MUST render as "(set)" because ServerEntry.Key
// is sensitive. Verifying that absence is the whole point of this test.
func TestRenderConfigShow_Snapshot(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerEntry{
			{Name: "default", URL: "http://localhost:21847", Key: "cix_secret123"},
			{Name: "corporate", URL: "https://cix.corp.internal", Key: ""},
		},
		DefaultServer: "default",
		Watcher: config.WatcherConfig{
			Enabled:          true,
			DebounceMS:       5000,
			ExcludePatterns:  []string{"node_modules", ".git"},
			SyncIntervalMins: 5,
		},
		Indexing: config.IndexingConfig{
			BatchSize:               20,
			StreamingIdleTimeoutSec: 30,
		},
		Projects: []config.ProjectEntry{
			{Path: "/home/u/proj", AutoWatch: true},
		},
	}

	var buf bytes.Buffer
	if err := renderConfigShow(&buf, cfg, "/home/u/.cix/config.yaml"); err != nil {
		t.Fatalf("renderConfigShow: %v", err)
	}

	want := `servers (2):
* default          url=http://localhost:21847 key=(set)
  corporate        url=https://cix.corp.internal key=(not set)
default_server                       = default

watcher.enabled                      = true
watcher.debounce_ms                  = 5000
watcher.exclude                      = [node_modules .git]
watcher.sync_interval_mins           = 5

indexing.batch_size                  = 20
indexing.streaming_idle_timeout_sec  = 30

projects (1):
  - /home/u/proj (auto-watch: true)

config file: /home/u/.cix/config.yaml
`

	got := buf.String()
	if got != want {
		t.Errorf("renderConfigShow output mismatch\n--- want ---\n%s\n--- got ----\n%s", want, got)
	}

	// Explicit safety belt: the sensitive value MUST NOT leak even via
	// substring (no length disclosure, no prefix, no mask).
	if strings.Contains(got, "cix_secret123") {
		t.Errorf("sensitive value leaked into output")
	}
	if strings.Contains(got, "secret") {
		t.Errorf("substring of sensitive value leaked into output")
	}
}

func TestRenderConfigShow_EmptyProjects(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerEntry{
			{Name: "default", URL: "http://localhost:21847"},
		},
		DefaultServer: "default",
		Watcher:       config.WatcherConfig{},
		Indexing:      config.IndexingConfig{},
	}
	var buf bytes.Buffer
	if err := renderConfigShow(&buf, cfg, "/tmp/cfg.yaml"); err != nil {
		t.Fatalf("renderConfigShow: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "projects (") {
		t.Errorf("projects block rendered for empty list:\n%s", out)
	}
}
