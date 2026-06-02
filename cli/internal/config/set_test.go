package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// withIsolatedHome points config.Load() at a throwaway HOME and resets the
// singleton. Mirrors cmd/multiserver_test.go's isolateConfig — duplicated
// because that helper is in the cmd package and import would be cyclic.
func withIsolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	ResetForTesting()
	t.Cleanup(ResetForTesting)
}

func TestSetByPath_Bool(t *testing.T) {
	withIsolatedHome(t)
	if err := SetByPath("watcher.enabled", "false"); err != nil {
		t.Fatalf("SetByPath: %v", err)
	}
	cfg, _ := Load()
	if cfg.Watcher.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	// And back.
	if err := SetByPath("watcher.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if !cfg.Watcher.Enabled {
		t.Errorf("Enabled = false, want true")
	}
}

func TestSetByPath_Int(t *testing.T) {
	withIsolatedHome(t)
	if err := SetByPath("watcher.debounce_ms", "2500"); err != nil {
		t.Fatalf("SetByPath: %v", err)
	}
	cfg, _ := Load()
	if cfg.Watcher.DebounceMS != 2500 {
		t.Errorf("DebounceMS = %d, want 2500", cfg.Watcher.DebounceMS)
	}
}

func TestSetByPath_Slice_ReplaceSemantics(t *testing.T) {
	withIsolatedHome(t)
	// Set then overwrite — replace, not append.
	if err := SetByPath("watcher.exclude", "vendor, tmp ,build"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	cfg, _ := Load()
	want := []string{"vendor", "tmp", "build"}
	if !reflect.DeepEqual(cfg.Watcher.ExcludePatterns, want) {
		t.Errorf("ExcludePatterns = %v, want %v", cfg.Watcher.ExcludePatterns, want)
	}

	if err := SetByPath("watcher.exclude", "only"); err != nil {
		t.Fatalf("second set: %v", err)
	}
	cfg, _ = Load()
	if !reflect.DeepEqual(cfg.Watcher.ExcludePatterns, []string{"only"}) {
		t.Errorf("after replace, ExcludePatterns = %v, want [only]", cfg.Watcher.ExcludePatterns)
	}
}

func TestSetByPath_StreamingIdleTimeout(t *testing.T) {
	// Was NEVER settable via the legacy switch — exposing it is one of the
	// concrete user-visible wins of the schema-driven setter.
	withIsolatedHome(t)
	if err := SetByPath("indexing.streaming_idle_timeout_sec", "60"); err != nil {
		t.Fatalf("SetByPath: %v", err)
	}
	cfg, _ := Load()
	if cfg.Indexing.StreamingIdleTimeoutSec != 60 {
		t.Errorf("StreamingIdleTimeoutSec = %d, want 60", cfg.Indexing.StreamingIdleTimeoutSec)
	}
}

func TestSetByPath_ValidationRejectsBadValue(t *testing.T) {
	withIsolatedHome(t)
	err := SetByPath("indexing.batch_size", "0")
	if err == nil {
		t.Fatal("expected validation error for batch_size=0")
	}
	// Bad value MUST NOT be persisted.
	cfg, _ := Load()
	if cfg.Indexing.BatchSize == 0 {
		t.Errorf("BatchSize = 0 — validation should have rolled back the in-memory mutation before Save")
	}
}

func TestSetByPath_UnknownKey(t *testing.T) {
	withIsolatedHome(t)
	err := SetByPath("nope.does.not.exist", "v")
	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("err = %v, want errors.Is(_, ErrUnknownKey)", err)
	}
}

func TestSetByPath_BadIntFormat(t *testing.T) {
	withIsolatedHome(t)
	err := SetByPath("watcher.debounce_ms", "abc")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSetByPath_BadBoolFormat(t *testing.T) {
	withIsolatedHome(t)
	err := SetByPath("watcher.enabled", "maybe")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// TestSetByPath_PersistsToDisk verifies the mutation actually reaches the
// YAML file (not just the in-memory singleton). Without this, a downstream
// reader on a fresh process would see the old value.
func TestSetByPath_PersistsToDisk(t *testing.T) {
	withIsolatedHome(t)
	if err := SetByPath("watcher.debounce_ms", "7777"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".cix", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !contains(string(data), "debounce_ms: 7777") {
		t.Errorf("config.yaml does not contain the new value:\n%s", string(data))
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
