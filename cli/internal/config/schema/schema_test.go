package schema_test

import (
	"reflect"
	"testing"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/config/schema"
)

// expectedKeys is the contract: the exact dotted-key set Walk yields over
// the current Config struct. Any new annotated field MUST update this list.
// Bare `expectedKeys` (not regex/contains) is intentional — silent drift in
// the key surface is exactly what this snapshot is here to catch.
var expectedKeys = []string{
	"servers",
	"default_server",
	"watcher.enabled",
	"watcher.debounce_ms",
	"watcher.exclude",
	"watcher.sync_interval_mins",
	"indexing.batch_size",
	"indexing.streaming_idle_timeout_sec",
	"projects",
}

func TestKeys_Snapshot(t *testing.T) {
	got, err := schema.Keys(&config.Config{})
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if !reflect.DeepEqual(got, expectedKeys) {
		t.Errorf("key snapshot drift:\nwant: %v\ngot:  %v", expectedKeys, got)
	}
}

func TestWalk_YieldsTagMetadata(t *testing.T) {
	// Spot-check that the LeafField carries enough metadata for downstream
	// consumers (show, set, keys, TUI) — desc, default, validate, env.
	want := map[string]map[string]string{
		"watcher.debounce_ms": {
			"desc":     "Debounce delay (ms)",
			"default":  "5000",
			"validate": "min=100,max=60000",
		},
		"default_server": {
			"env":  "CIX_SERVER",
			"desc": "Alias of the server used when --server is omitted",
		},
		"indexing.batch_size": {
			"default":  "20",
			"validate": "min=1",
		},
	}

	seen := map[string]map[string]string{}
	err := schema.Walk(&config.Config{}, func(l schema.LeafField) {
		if _, target := want[l.Path]; !target {
			return
		}
		seen[l.Path] = map[string]string{
			"desc":     l.Tag("desc"),
			"default":  l.Tag("default"),
			"validate": l.Tag("validate"),
			"env":      l.Tag("env"),
		}
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	for path, expect := range want {
		got, ok := seen[path]
		if !ok {
			t.Errorf("%s: not yielded by Walk", path)
			continue
		}
		for tag, val := range expect {
			if got[tag] != val {
				t.Errorf("%s tag %q: want %q, got %q", path, tag, val, got[tag])
			}
		}
	}
}

func TestWalk_RejectsNonStruct(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"int", 42},
		{"nil-ptr", (*config.Config)(nil)},
		{"string", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := schema.Walk(tc.v, func(schema.LeafField) {})
			if err == nil {
				t.Errorf("expected error for %v, got nil", tc.v)
			}
		})
	}
}

func TestWalk_SkipsLegacyAPIBlock(t *testing.T) {
	// The legacy API field has no `key:` tag, so the walker must not yield
	// `api.url` / `api.key` even though APIConfig has exported fields.
	got, err := schema.Keys(&config.Config{})
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	for _, k := range got {
		if k == "api.url" || k == "api.key" || k == "api" {
			t.Errorf("legacy API field leaked into walker output: %q", k)
		}
	}
}
