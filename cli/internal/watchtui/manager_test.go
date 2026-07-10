package watchtui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/code-index/cli/internal/client"
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

// isolateHomeDir points HOME at a temp dir and resets the config singleton
// so DaemonManager tests never touch the real ~/.cix.
func isolateHomeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	return dir
}

// TestStartAllAuto_SingleHealthProbe pins the batch-preflight contract: one
// /health probe for the whole run, not one per pending project — with the
// server down each probe can hang for the full client timeout, so probing
// per project would multiply that wait.
func TestStartAllAuto_SingleHealthProbe(t *testing.T) {
	isolateHomeDir(t)

	var healthHits, otherHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			healthHits.Add(1)
		} else {
			otherHits.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	for _, p := range []string{"/srv/auto-a", "/srv/auto-b", "/srv/auto-c"} {
		if err := config.AddProject(p, true); err != nil {
			t.Fatal(err)
		}
	}

	d := NewDaemonManager(client.New(srv.URL, "test-key"))
	n, err := d.StartAllAuto()
	if err == nil {
		t.Fatal("expected an error while the server is unhealthy")
	}
	if n != 0 {
		t.Errorf("started = %d, want 0", n)
	}
	if got := healthHits.Load(); got != 1 {
		t.Errorf("health probes = %d, want exactly 1 for the whole batch", got)
	}
	if got := otherHits.Load(); got != 0 {
		t.Errorf("no per-project requests may fire when the probe fails; got %d", got)
	}
}

func TestStartAllAuto_NothingPending_NoServerCalls(t *testing.T) {
	isolateHomeDir(t)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	if err := config.AddProject("/srv/manual", false); err != nil { // not auto_watch
		t.Fatal(err)
	}
	d := NewDaemonManager(client.New(srv.URL, "test-key"))
	n, err := d.StartAllAuto()
	if err != nil || n != 0 {
		t.Fatalf("StartAllAuto = (%d, %v), want (0, nil)", n, err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("no server calls expected with nothing to start; got %d", got)
	}
}

func writeLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "watcher.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadTail_LastNLines(t *testing.T) {
	p := writeLog(t, "l1\nl2\nl3\nl4\n")
	got := readTail(p, 2)
	want := []string{"l3", "l4"}
	if len(got) != len(want) {
		t.Fatalf("readTail = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadTail_MissingFile(t *testing.T) {
	got := readTail(filepath.Join(t.TempDir(), "absent.log"), 5)
	if len(got) != 1 || !strings.HasPrefix(got[0], "(no log:") {
		t.Errorf("readTail = %v, want a single (no log: …) diagnostic", got)
	}
}

func TestReadTail_EmptyFile(t *testing.T) {
	p := writeLog(t, "")
	got := readTail(p, 5)
	if len(got) != 1 || got[0] != "(log is empty)" {
		t.Errorf("readTail = %v, want [(log is empty)]", got)
	}
}

// TestReadTailN_SeeksTailAndDropsPartialLine pins the bounded-read contract:
// only the last `budget` bytes are read (the logs are append-only and never
// rotated, so they can be huge), and the partial line at the cut is dropped
// so every returned line is complete.
func TestReadTailN_SeeksTailAndDropsPartialLine(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "line-%02d\n", i) // 8 bytes per line, 800 total
	}
	p := writeLog(t, b.String())

	got := readTailN(p, 3, 60) // budget cuts mid-line inside line-92
	want := []string{"line-97", "line-98", "line-99"}
	if len(got) != len(want) {
		t.Fatalf("readTailN = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q (partial first line must be dropped)", i, got[i], want[i])
		}
	}
}

func TestReadTailN_BudgetLandsMidLine_OnlyCompleteLines(t *testing.T) {
	p := writeLog(t, "aaaa\nbbbb\ncccc\n")
	got := readTailN(p, 10, 7) // tail bytes: "b\ncccc\n" → partial "b" dropped
	if len(got) != 1 || got[0] != "cccc" {
		t.Errorf("readTailN = %v, want [cccc]", got)
	}
}
