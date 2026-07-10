package watchtui

import (
	"testing"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/daemon"
)

func paths(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Path
	}
	return out
}

func find(items []Item, path string) (Item, bool) {
	for _, it := range items {
		if it.Path == path {
			return it, true
		}
	}
	return Item{}, false
}

func TestMergeItems_RunningFirstThenByPath(t *testing.T) {
	known := []config.ProjectEntry{
		{Path: "/b", AutoWatch: false},
		{Path: "/a", AutoWatch: true},
	}
	daemons := []daemon.Status{
		{Running: true, PID: 10, ProjectPath: "/a"},
	}

	got := paths(mergeItems(known, daemons))
	want := []string{"/a", "/b"} // /a running → first; /b stopped
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestMergeItems_DedupByPath(t *testing.T) {
	known := []config.ProjectEntry{{Path: "/proj", AutoWatch: true}}
	daemons := []daemon.Status{{Running: true, PID: 42, ProjectPath: "/proj"}}

	items := mergeItems(known, daemons)
	if len(items) != 1 {
		t.Fatalf("expected 1 merged row, got %d: %v", len(items), paths(items))
	}
	it := items[0]
	if !it.Running || it.PID != 42 {
		t.Errorf("running/pid = %v/%d, want true/42", it.Running, it.PID)
	}
	if !it.AutoWatch {
		t.Error("AutoWatch should be carried from the config entry")
	}
}

func TestMergeItems_DaemonNotInConfigStillShown(t *testing.T) {
	known := []config.ProjectEntry{{Path: "/known"}}
	daemons := []daemon.Status{{Running: true, PID: 7, ProjectPath: "/orphan"}}

	items := mergeItems(known, daemons)
	if _, ok := find(items, "/orphan"); !ok {
		t.Fatalf("orphan daemon row missing: %v", paths(items))
	}
	orphan, _ := find(items, "/orphan")
	if !orphan.Running || orphan.PID != 7 {
		t.Errorf("orphan running/pid = %v/%d, want true/7", orphan.Running, orphan.PID)
	}
	if orphan.AutoWatch {
		t.Error("orphan (no config entry) should not be auto_watch")
	}
}

func TestMergeItems_EmptyInputs(t *testing.T) {
	if items := mergeItems(nil, nil); len(items) != 0 {
		t.Errorf("expected no rows, got %v", paths(items))
	}
}
