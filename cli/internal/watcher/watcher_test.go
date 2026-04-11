package watcher

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/rjeczalik/notify"
)

// projectHash mirrors client.encodeProjectPath.
func projectHash(path string) string {
	h := sha1.Sum([]byte(path))
	return fmt.Sprintf("%x", h)[:16]
}

// mockEventInfo implements notify.EventInfo for testing.
type mockEventInfo struct {
	path  string
	event notify.Event
}

func (m mockEventInfo) Path() string        { return m.path }
func (m mockEventInfo) Event() notify.Event { return m.event }
func (m mockEventInfo) Sys() interface{}    { return nil }

// newTestWatcher creates a Watcher ready for unit testing.
func newTestWatcher(t *testing.T, projectPath, apiURL string) *Watcher {
	t.Helper()

	return &Watcher{
		projectPath:      projectPath,
		apiClient:        client.New(apiURL, "test-key"),
		debounceMS:       50, // short for tests
		syncIntervalMins: 5,
		excludeDirs:      map[string]bool{"node_modules": true, ".git": true},
		excludeExts:      map[string]bool{".jpg": true, ".png": true, ".pyc": true},
		eventCh:          make(chan notify.EventInfo, 256),
		logger:           log.New(io.Discard, "", 0),
		stopCh:           make(chan struct{}),
		pendingChanges:   make(map[string]bool),
		// New fields initialized to zero values
	}
}

// newIndexServer sets up a minimal mock that handles the three-phase index
// protocol and counts how many times each phase was called.
type serverCalls struct {
	mu     sync.Mutex
	Begin  int
	Files  int
	Finish int
}

func newIndexServer(t *testing.T, dir string) (*httptest.Server, *serverCalls) {
	t.Helper()
	calls := &serverCalls{}
	hash := projectHash(dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		calls.mu.Lock()
		defer calls.mu.Unlock()

		switch {
		case strings.Contains(p, hash+"/index/begin"):
			calls.Begin++
			json.NewEncoder(w).Encode(map[string]any{
				"run_id":        "run-watch",
				"stored_hashes": map[string]string{},
			})
		case strings.Contains(p, hash+"/index/files"):
			calls.Files++
			io.ReadAll(r.Body) //nolint
			json.NewEncoder(w).Encode(map[string]any{
				"files_accepted": 0, "chunks_created": 0, "files_processed_total": 0,
			})
		case strings.Contains(p, hash+"/index/finish"):
			calls.Finish++
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "files_processed": 0, "chunks_created": 0,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// waitForCalls polls the mock server counters until BeginIndex has been called
// at least target times, or the deadline is reached.
func waitForCalls(calls *serverCalls, target int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls.mu.Lock()
		count := calls.Begin
		calls.mu.Unlock()
		if count >= target {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// isExcluded / isExcludedExt — pure unit tests, no I/O or API needed
// ---------------------------------------------------------------------------

func TestIsExcluded_ExcludedDir(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	if !w.isExcluded("/project/node_modules/lodash/index.js") {
		t.Error("expected node_modules path to be excluded")
	}
}

func TestIsExcluded_NormalPath(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	if w.isExcluded("/project/src/main.go") {
		t.Error("expected normal source path not to be excluded")
	}
}

func TestIsExcluded_NestedExcludedDir(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	if !w.isExcluded("/project/packages/app/node_modules/react/index.js") {
		t.Error("expected deeply nested node_modules to be excluded")
	}
}

func TestIsExcluded_GitHEAD(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	// .git/HEAD must pass through for branch switch detection.
	if w.isExcluded("/project/.git/HEAD") {
		t.Error(".git/HEAD should not be excluded")
	}

	// All other .git/ paths must still be excluded.
	for _, p := range []string{
		"/project/.git/config",
		"/project/.git/COMMIT_EDITMSG",
		"/project/.git/objects/ab/cdef123",
		"/project/.git/refs/heads/main",
	} {
		if !w.isExcluded(p) {
			t.Errorf("%q should be excluded", p)
		}
	}
}

func TestIsExcludedExt_BinaryFile(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	for _, name := range []string{"photo.jpg", "icon.PNG", "cache.pyc"} {
		if !w.isExcludedExt("/project/" + name) {
			t.Errorf("expected %q to be excluded by extension", name)
		}
	}
}

func TestIsExcludedExt_SourceFile(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	for _, name := range []string{"main.go", "app.py", "index.ts"} {
		if w.isExcludedExt("/project/" + name) {
			t.Errorf("expected %q not to be excluded by extension", name)
		}
	}
}

// ---------------------------------------------------------------------------
// trackChange — debounce accumulation
// ---------------------------------------------------------------------------

func TestTrackChange_AccumulatesFiles(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	w.trackChange("/project/a.go")
	w.trackChange("/project/b.go")
	w.trackChange("/project/c.go")

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, f := range []string{"/project/a.go", "/project/b.go", "/project/c.go"} {
		if !w.pendingChanges[f] {
			t.Errorf("expected %q in pendingChanges", f)
		}
	}
}

func TestTrackChange_DeduplicatesSamePath(t *testing.T) {
	w := newTestWatcher(t, "/project", "http://localhost")

	w.trackChange("/project/a.go")
	w.trackChange("/project/a.go")
	w.trackChange("/project/a.go")

	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pendingChanges) != 1 {
		t.Errorf("expected 1 unique path, got %d", len(w.pendingChanges))
	}
}

// ---------------------------------------------------------------------------
// handleEvent — event routing (no API calls, debounce timer not fired)
// ---------------------------------------------------------------------------

func TestHandleEvent_CreateFile_Tracked(t *testing.T) {
	dir := t.TempDir()
	srv, _ := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	path := filepath.Join(dir, "new.go")
	os.WriteFile(path, []byte("package main\n"), 0644)

	w.handleEvent(mockEventInfo{path: path, event: notify.Create})

	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if !tracked {
		t.Errorf("expected %q in pendingChanges after Create event", path)
	}
}

func TestHandleEvent_WriteFile_Tracked(t *testing.T) {
	dir := t.TempDir()
	srv, _ := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	path := filepath.Join(dir, "existing.go")
	os.WriteFile(path, []byte("package main\n"), 0644)

	w.handleEvent(mockEventInfo{path: path, event: notify.Write})

	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if !tracked {
		t.Errorf("expected %q in pendingChanges after Write event", path)
	}
}

func TestHandleEvent_RemoveFile_Tracked(t *testing.T) {
	dir := t.TempDir()
	srv, _ := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	// For Remove events the file is gone from disk; path is still the event name.
	path := filepath.Join(dir, "deleted.go")

	w.handleEvent(mockEventInfo{path: path, event: notify.Remove})

	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if !tracked {
		t.Errorf("expected %q in pendingChanges after Remove event", path)
	}
}

func TestHandleEvent_RenameFile_Tracked(t *testing.T) {
	dir := t.TempDir()
	srv, _ := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)
	path := filepath.Join(dir, "renamed.go")

	w.handleEvent(mockEventInfo{path: path, event: notify.Rename})

	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if !tracked {
		t.Errorf("expected %q in pendingChanges after Rename event", path)
	}
}

func TestHandleEvent_ExcludedExtension_NotTracked(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir, "http://localhost")

	path := filepath.Join(dir, "photo.jpg")
	os.WriteFile(path, []byte("data"), 0644)

	w.handleEvent(mockEventInfo{path: path, event: notify.Write})

	w.mu.Lock()
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if tracked {
		t.Errorf("expected .jpg to NOT be tracked")
	}
}

func TestHandleEvent_ExcludedDir_NotTracked(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir, "http://localhost")

	path := filepath.Join(dir, "node_modules", "lib.js")

	w.handleEvent(mockEventInfo{path: path, event: notify.Write})

	w.mu.Lock()
	tracked := w.pendingChanges[path]
	w.mu.Unlock()

	if tracked {
		t.Errorf("expected node_modules path to NOT be tracked")
	}
}

// ---------------------------------------------------------------------------
// flushChanges — calls indexer.Run via real HTTP mock
// ---------------------------------------------------------------------------

func TestFlushChanges_TriggersIncrementalReindex(t *testing.T) {
	dir := t.TempDir()
	// Create a real file so discovery finds something.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	// Inject a pending change and flush immediately.
	w.pendingChanges[filepath.Join(dir, "main.go")] = true
	w.flushChanges()

	// Wait for indexing to complete (it runs in a goroutine now)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls.mu.Lock()
		count := calls.Begin
		calls.mu.Unlock()
		if count >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected BeginIndex to be called")
	}
	if calls.Finish < 1 {
		t.Error("expected FinishIndex to be called")
	}
}

func TestFlushChanges_EmptyPending_NoAPICall(t *testing.T) {
	dir := t.TempDir()
	apiCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)

	w := newTestWatcher(t, dir, srv.URL)
	w.flushChanges() // pendingChanges is empty

	if apiCalled {
		t.Error("expected no API call when pendingChanges is empty")
	}
}

func TestFlushChanges_ServerUnavailable_NoPanic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	// Closed server → connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	w := newTestWatcher(t, dir, srv.URL)
	w.pendingChanges[filepath.Join(dir, "main.go")] = true

	// Should log the error and return without panicking.
	w.flushChanges()

	// pendingChanges is cleared before the API call, so it should be empty
	// even when the server is down.
	w.mu.Lock()
	remaining := len(w.pendingChanges)
	w.mu.Unlock()
	if remaining != 0 {
		t.Errorf("expected pendingChanges to be cleared after flush, got %d entries", remaining)
	}
}

// TestFlushChanges_RecoveryAfterServerDown demonstrates that pending changes
// accumulated while the server was down are picked up on the next successful
// flush, because indexer.Run always performs a full hash diff.
func TestFlushChanges_RecoveryAfterServerDown(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	os.WriteFile(filePath, []byte("package main\n"), 0644)

	// Round 1: server is down.
	downSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	downSrv.Close()

	w1 := newTestWatcher(t, dir, downSrv.URL)
	w1.pendingChanges[filePath] = true
	w1.flushChanges() // fails silently

	// Round 2: new event arrives on a restored server.
	srv, calls := newIndexServer(t, dir)
	w2 := newTestWatcher(t, dir, srv.URL)
	w2.pendingChanges[filePath] = true
	w2.flushChanges()

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 || calls.Finish < 1 {
		t.Errorf("expected successful reindex after recovery; Begin=%d Finish=%d", calls.Begin, calls.Finish)
	}
}

// ---------------------------------------------------------------------------
// triggerFullReindex — .gitignore changes
// ---------------------------------------------------------------------------

func TestTriggerFullReindex_CallsAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	w.triggerFullReindex()

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected BeginIndex to be called on full reindex")
	}
	if calls.Finish < 1 {
		t.Error("expected FinishIndex to be called on full reindex")
	}
}

func TestHandleEvent_GitignoreChange_FullReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	giPath := filepath.Join(dir, ".gitignore")
	os.WriteFile(giPath, []byte("*.log\n"), 0644)
	w.handleEvent(mockEventInfo{path: giPath, event: notify.Write})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected full reindex triggered by .gitignore change")
	}
}

func TestHandleEvent_CixignoreChange_FullReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	cixPath := filepath.Join(dir, ".cixignore")
	os.WriteFile(cixPath, []byte("vendor-ext/\n"), 0644)
	w.handleEvent(mockEventInfo{path: cixPath, event: notify.Write})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected full reindex triggered by .cixignore change")
	}
}

func TestHandleEvent_CixconfigChange_FullReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	cfgPath := filepath.Join(dir, ".cixconfig.yaml")
	os.WriteFile(cfgPath, []byte("ignore:\n  submodules: true\n"), 0644)
	w.handleEvent(mockEventInfo{path: cfgPath, event: notify.Write})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected full reindex triggered by .cixconfig.yaml change")
	}
}

func TestHandleEvent_CixignoreCreate_FullReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	cixPath := filepath.Join(dir, ".cixignore")
	os.WriteFile(cixPath, []byte("submodules/\n"), 0644)
	w.handleEvent(mockEventInfo{path: cixPath, event: notify.Create})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected full reindex triggered by .cixignore creation")
	}
}

func TestHandleEvent_CixignoreRemove_FullReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	cixPath := filepath.Join(dir, ".cixignore")
	w.handleEvent(mockEventInfo{path: cixPath, event: notify.Remove})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected full reindex triggered by .cixignore removal")
	}
}

func TestHandleEvent_GitHEADChange_TriggersReindex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	headPath := filepath.Join(dir, ".git", "HEAD")
	w.handleEvent(mockEventInfo{path: headPath, event: notify.Write})

	waitForCalls(calls, 1)

	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.Begin < 1 {
		t.Error("expected reindex triggered by .git/HEAD change (branch switch)")
	}
}

func TestHandleEvent_GitHEADChange_ClearsPendingChanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)

	// Simulate some pending changes accumulated before the branch switch.
	w.trackChange(filepath.Join(dir, "a.go"))
	w.trackChange(filepath.Join(dir, "b.go"))

	headPath := filepath.Join(dir, ".git", "HEAD")
	w.handleEvent(mockEventInfo{path: headPath, event: notify.Write})

	waitForCalls(calls, 1)

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pendingChanges) != 0 {
		t.Errorf("pendingChanges should be cleared after HEAD-triggered reindex, got %v", w.pendingChanges)
	}
}

// ---------------------------------------------------------------------------
// Debounce — multiple rapid events produce a single flush
// ---------------------------------------------------------------------------

func TestDebounce_MultipleEventsOnce(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package lib\n"), 0644)

	srv, calls := newIndexServer(t, dir)
	w := newTestWatcher(t, dir, srv.URL)
	w.debounceMS = 80

	// Fire several Write events in quick succession.
	for i := 0; i < 5; i++ {
		w.trackChange(filepath.Join(dir, "a.go"))
		w.trackChange(filepath.Join(dir, "b.go"))
	}

	// Poll until the debounce timer fires, with a generous deadline.
	deadline := time.Now().Add(time.Duration(w.debounceMS*10) * time.Millisecond)
	for time.Now().Before(deadline) {
		calls.mu.Lock()
		n := calls.Begin
		calls.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls.mu.Lock()
	defer calls.mu.Unlock()
	// All five events should have collapsed into exactly one flush.
	if calls.Begin != 1 {
		t.Errorf("expected 1 BeginIndex call, got %d", calls.Begin)
	}
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

func TestStop_ClosesStopChannel(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir, "http://localhost")

	w.Stop()

	select {
	case _, open := <-w.stopCh:
		if open {
			t.Error("stopCh should be closed, not just receiving a value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("stopCh was not closed after Stop()")
	}
}