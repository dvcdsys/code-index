package watchtui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// autoCall records one SetAutoWatch invocation.
type autoCall struct {
	path string
	on   bool
}

// fakeManager records calls and returns canned data so the Model can be
// exercised without touching the daemon layer, the config, or a server.
type fakeManager struct {
	items   []Item
	listErr error

	stopCalls      []string
	startCalls     []string
	restartCalls   []string
	deleteCalls    []string
	autoWatchCalls []autoCall
	stopAllCalls   int
	autoCalls      int

	stopErr      error
	startErr     error
	restartErr   error
	deleteErr    error
	autoWatchErr error
	stopAllN     int
	autoN        int

	lastIndexed map[string]*time.Time
}

func (f *fakeManager) List() ([]Item, error) { return f.items, f.listErr }
func (f *fakeManager) LastIndexed() (map[string]*time.Time, error) {
	return f.lastIndexed, nil
}
func (f *fakeManager) Stop(p string) error {
	f.stopCalls = append(f.stopCalls, p)
	return f.stopErr
}
func (f *fakeManager) Start(p string) error {
	f.startCalls = append(f.startCalls, p)
	return f.startErr
}
func (f *fakeManager) Restart(p string) error {
	f.restartCalls = append(f.restartCalls, p)
	return f.restartErr
}
func (f *fakeManager) StartAllAuto() (int, error) { f.autoCalls++; return f.autoN, nil }
func (f *fakeManager) StopAll() (int, error)      { f.stopAllCalls++; return f.stopAllN, nil }
func (f *fakeManager) Delete(p string) error {
	f.deleteCalls = append(f.deleteCalls, p)
	return f.deleteErr
}
func (f *fakeManager) SetAutoWatch(p string, on bool) error {
	f.autoWatchCalls = append(f.autoWatchCalls, autoCall{path: p, on: on})
	return f.autoWatchErr
}
func (f *fakeManager) LogPath(p string) (string, error) {
	return "/nonexistent/watcher.log", nil
}

// makeKey mimics a single-rune keypress. Same helper shape as the config
// editor's model_test.go.
func makeKey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// send pushes msgs through Update and returns the final Model.
func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	return m
}

func twoRows() *fakeManager {
	return &fakeManager{items: []Item{
		{Path: "/proj/alpha", Running: true, PID: 111, AutoWatch: true},
		{Path: "/proj/beta", Running: false, AutoWatch: false},
	}}
}

func newTestModel(f *fakeManager) Model {
	m := NewModel(f)
	m.width, m.height = 100, 30
	return m
}

func TestNewModel_PopulatesRowsFromManager(t *testing.T) {
	m := newTestModel(twoRows())
	if len(m.items) != 2 {
		t.Fatalf("items = %d, want 2", len(m.items))
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
}

func TestNavigation_DownMovesCursor(t *testing.T) {
	m := newTestModel(twoRows())
	m = send(m, makeKey("down"))
	if m.cursor != 1 {
		t.Errorf("after down: cursor = %d, want 1", m.cursor)
	}
	// Clamped at the bottom.
	m = send(m, makeKey("down"))
	if m.cursor != 1 {
		t.Errorf("cursor should clamp at 1, got %d", m.cursor)
	}
}

func TestQuitSetsQuittingFlag(t *testing.T) {
	m := newTestModel(twoRows())
	m = send(m, makeKey("q"))
	if !m.quitting {
		t.Error("q should set quitting=true")
	}
}

func TestHelpToggle(t *testing.T) {
	m := newTestModel(twoRows())
	m = send(m, makeKey("?"))
	if !m.showHelp {
		t.Error("? should show help")
	}
	m = send(m, makeKey("x"))
	if m.showHelp {
		t.Error("any key should dismiss help")
	}
	// The dismissing key must not also be treated as an action.
	if len(m.mgr.(*fakeManager).stopCalls) != 0 || m.mgr.(*fakeManager).stopAllCalls != 0 {
		t.Error("dismissing help should not trigger an action")
	}
}

func TestStopKey_CallsManagerForSelectedRunningRow(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("s"))
	if len(f.stopCalls) != 1 || f.stopCalls[0] != "/proj/alpha" {
		t.Fatalf("stopCalls = %v, want [/proj/alpha]", f.stopCalls)
	}
	if m.statusErr {
		t.Errorf("status should be OK after a successful stop; msg=%q", m.statusMsg)
	}
}

func TestStopKey_NotRunning_NoCall(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("down"), makeKey("s")) // beta is stopped
	if len(f.stopCalls) != 0 {
		t.Errorf("stop should not be called on a stopped row; got %v", f.stopCalls)
	}
	if !m.statusErr {
		t.Error("stopping a non-running row should set an error status")
	}
}

func TestStartKey_OnStoppedRow(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("down"), makeKey("S")) // beta is stopped
	if len(f.startCalls) != 1 || f.startCalls[0] != "/proj/beta" {
		t.Fatalf("startCalls = %v, want [/proj/beta]", f.startCalls)
	}
}

func TestStartKey_AlreadyRunning_NoCall(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("S")) // alpha is running
	if len(f.startCalls) != 0 {
		t.Errorf("start should not be called on a running row; got %v", f.startCalls)
	}
	if !m.statusErr {
		t.Error("starting an already-running row should set an error status")
	}
}

func TestRestartKey_CallsManager(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("r"))
	if len(f.restartCalls) != 1 || f.restartCalls[0] != "/proj/alpha" {
		t.Fatalf("restartCalls = %v, want [/proj/alpha]", f.restartCalls)
	}
}

func TestStopAllKey_CallsManager(t *testing.T) {
	f := twoRows()
	f.stopAllN = 3
	m := newTestModel(f)
	m = send(m, makeKey("x"))
	if f.stopAllCalls != 1 {
		t.Fatalf("stopAllCalls = %d, want 1", f.stopAllCalls)
	}
	if !strings.Contains(m.statusMsg, "3") {
		t.Errorf("status should report the stopped count; msg=%q", m.statusMsg)
	}
}

func TestStartAutoKey_CallsManager(t *testing.T) {
	f := twoRows()
	f.autoN = 2
	m := newTestModel(f)
	m = send(m, makeKey("A"))
	if f.autoCalls != 1 {
		t.Fatalf("autoCalls = %d, want 1", f.autoCalls)
	}
	if m.statusErr {
		t.Errorf("start-auto should be OK; msg=%q", m.statusMsg)
	}
}

func TestManagerError_ShowsStatusErr(t *testing.T) {
	f := twoRows()
	f.stopErr = errors.New("boom")
	m := newTestModel(f)
	m = send(m, makeKey("s"))
	if !m.statusErr {
		t.Error("a manager error should set statusErr=true")
	}
	if !strings.Contains(m.statusMsg, "boom") {
		t.Errorf("status should surface the error; msg=%q", m.statusMsg)
	}
}

func TestToggleAuto_FlipsFlagForSelectedRow(t *testing.T) {
	f := twoRows() // alpha: auto=true, beta: auto=false
	m := newTestModel(f)

	m = send(m, makeKey("a")) // alpha true → false
	if len(f.autoWatchCalls) != 1 {
		t.Fatalf("autoWatchCalls = %v, want 1", f.autoWatchCalls)
	}
	if got := f.autoWatchCalls[0]; got.path != "/proj/alpha" || got.on {
		t.Errorf("call = %+v, want {/proj/alpha false}", got)
	}

	m = send(m, makeKey("down"), makeKey("a")) // beta false → true
	if got := f.autoWatchCalls[1]; got.path != "/proj/beta" || !got.on {
		t.Errorf("call = %+v, want {/proj/beta true}", got)
	}
	if m.statusErr {
		t.Errorf("toggle should be OK; msg=%q", m.statusMsg)
	}
}

func TestToggleAuto_SpaceIsTheSameBinding(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey(" "))
	if len(f.autoWatchCalls) != 1 || f.autoWatchCalls[0].path != "/proj/alpha" {
		t.Fatalf("space should toggle the selected row; got %v", f.autoWatchCalls)
	}
	_ = m
}

func TestToggleAuto_DoesNotStartOrStopAnything(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("a"))
	if len(f.startCalls) != 0 || len(f.stopCalls) != 0 || f.autoCalls != 0 {
		t.Error("toggling auto_watch must not start/stop watchers")
	}
	_ = m
}

func TestToggleAuto_ErrorShowsStatusErr(t *testing.T) {
	f := twoRows()
	f.autoWatchErr = errors.New("disk full")
	m := newTestModel(f)
	m = send(m, makeKey("a"))
	if !m.statusErr || !strings.Contains(m.statusMsg, "disk full") {
		t.Errorf("toggle error should surface; err=%v msg=%q", m.statusErr, m.statusMsg)
	}
}

func TestDeleteKey_RequiresConfirmation(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("d"))
	if !m.confirmDelete {
		t.Fatal("d should arm the delete confirmation")
	}
	if m.confirmPath != "/proj/alpha" {
		t.Errorf("confirmPath = %q, want /proj/alpha", m.confirmPath)
	}
	if len(f.deleteCalls) != 0 {
		t.Errorf("delete must not fire before confirmation; got %v", f.deleteCalls)
	}
	// The prompt should be visible in the view.
	if !strings.Contains(m.View(), "permanent") {
		t.Error("View() should show the delete confirmation prompt")
	}
}

func TestDeleteConfirm_YesDeletes(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("d"), makeKey("y"))
	if len(f.deleteCalls) != 1 || f.deleteCalls[0] != "/proj/alpha" {
		t.Fatalf("deleteCalls = %v, want [/proj/alpha]", f.deleteCalls)
	}
	if m.confirmDelete {
		t.Error("confirmation should be cleared after y")
	}
	if m.statusErr {
		t.Errorf("status should be OK after a successful delete; msg=%q", m.statusMsg)
	}
}

func TestDeleteConfirm_OtherKeyCancels(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("d"), makeKey("n"))
	if len(f.deleteCalls) != 0 {
		t.Errorf("n should cancel; got delete calls %v", f.deleteCalls)
	}
	if m.confirmDelete {
		t.Error("confirmation should be cleared after cancel")
	}
	// A stray cancel key must not fall through to an action or quit.
	if m.quitting {
		t.Error("cancel key should not quit")
	}
}

func TestDeleteConfirm_EscCancels(t *testing.T) {
	f := twoRows()
	m := newTestModel(f)
	m = send(m, makeKey("d"), makeKey("esc"))
	if len(f.deleteCalls) != 0 {
		t.Errorf("esc should cancel; got %v", f.deleteCalls)
	}
	if m.quitting {
		t.Error("esc during confirm should cancel, not quit")
	}
}

func TestDeleteError_ShowsStatusErr(t *testing.T) {
	f := twoRows()
	f.deleteErr = errors.New("kaboom")
	m := newTestModel(f)
	m = send(m, makeKey("d"), makeKey("y"))
	if !m.statusErr {
		t.Error("a delete error should set statusErr=true")
	}
	if !strings.Contains(m.statusMsg, "kaboom") {
		t.Errorf("status should surface the error; msg=%q", m.statusMsg)
	}
}

func TestDetailKey_OpensAndCloses(t *testing.T) {
	m := newTestModel(twoRows())
	m = send(m, makeKey("enter"))
	if !m.detail {
		t.Fatal("enter should open the log detail overlay")
	}
	m = send(m, makeKey("esc"))
	if m.detail {
		t.Error("esc should close the detail overlay")
	}
}

func TestEnrichMsg_AppliesLastIndexed(t *testing.T) {
	m := newTestModel(twoRows())
	ts := time.Now().Add(-3 * time.Minute)
	m = send(m, enrichMsg{byPath: map[string]*time.Time{"/proj/alpha": &ts}})
	it, ok := find(m.items, "/proj/alpha")
	if !ok || it.LastIndexedAt == nil {
		t.Fatalf("LastIndexedAt should be applied to /proj/alpha")
	}
}

func TestListWindow_KeepsCursorVisible(t *testing.T) {
	cases := []struct {
		n, cursor, size, wantStart, wantEnd int
	}{
		{n: 3, cursor: 0, size: 5, wantStart: 0, wantEnd: 3},   // fits: whole list
		{n: 10, cursor: 0, size: 4, wantStart: 0, wantEnd: 4},  // top pin
		{n: 10, cursor: 9, size: 4, wantStart: 6, wantEnd: 10}, // bottom pin
		{n: 10, cursor: 5, size: 4, wantStart: 3, wantEnd: 7},  // centered
	}
	for _, c := range cases {
		start, end := listWindow(c.n, c.cursor, c.size)
		if start != c.wantStart || end != c.wantEnd {
			t.Errorf("listWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.n, c.cursor, c.size, start, end, c.wantStart, c.wantEnd)
		}
		if c.cursor < start || c.cursor >= end {
			t.Errorf("cursor %d not visible in window [%d,%d)", c.cursor, start, end)
		}
	}
}

func TestView_RendersRowsAndStatus(t *testing.T) {
	m := newTestModel(twoRows())
	m = send(m, makeKey("s")) // sets a status message
	out := m.View()
	for _, want := range []string{"/proj/alpha", "/proj/beta", "Watchers"} {
		if !strings.Contains(out, want) {
			t.Errorf("View() missing %q\n---\n%s", want, out)
		}
	}
}
