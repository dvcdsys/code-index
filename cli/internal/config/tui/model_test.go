package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anthropics/code-index/cli/internal/config"
)

// freshCfg returns a minimal Config suitable for driving the TUI in tests
// — two servers, full watcher/indexing defaults. No HOME isolation,
// because we never call Load/Save here; the Model is exercised directly.
func freshCfg() *config.Config {
	return &config.Config{
		Servers: []config.ServerEntry{
			{Name: "default", URL: "http://localhost:21847", Key: "k"},
			{Name: "corp", URL: "https://corp", Key: ""},
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
	}
}

// makeKey produces a tea.KeyMsg that mimics a single-rune keypress.
// Workaround for bubbletea's typed messages — Update only switches on
// these, so synthesising them is enough to drive the Model.
func makeKey(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
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

// send is the test driver — push msgs through Update and return the
// final Model. Test bodies stay short.
func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	return m
}

func TestNavigation_DownMovesSectionSelection(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30

	m = send(m, makeKey("down"))
	if m.sectionIdx != int(secWatcher) {
		t.Errorf("after down: sectionIdx = %d, want %d (Watcher)", m.sectionIdx, secWatcher)
	}
}

func TestNavigation_TabSwitchesPanels(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30
	if m.active != panelLeft {
		t.Fatalf("initial panel = %v, want left", m.active)
	}
	m = send(m, makeKey("tab"))
	if m.active != panelRight {
		t.Errorf("after tab: active = %v, want right", m.active)
	}
	m = send(m, makeKey("tab"))
	if m.active != panelLeft {
		t.Errorf("after second tab: active = %v, want left", m.active)
	}
}

func TestNavigation_LeftRightForcePanel(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30

	m = send(m, makeKey("l"))
	if m.active != panelRight {
		t.Errorf("after l: active = %v, want right", m.active)
	}
	m = send(m, makeKey("h"))
	if m.active != panelLeft {
		t.Errorf("after h: active = %v, want left", m.active)
	}
}

func TestEnterOnLeftPanelMovesFocusRight(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30
	m = send(m, makeKey("enter"))
	if m.active != panelRight {
		t.Errorf("enter from left panel should focus right; got %v", m.active)
	}
}

func TestQuitSetsQuittingFlag(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30
	m = send(m, makeKey("q"))
	if !m.quitting {
		t.Error("q should set quitting=true")
	}
}

func TestHelpToggle(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30
	m = send(m, makeKey("?"))
	if !m.showHelp {
		t.Error("? should show help")
	}
	// Any key dismisses.
	m = send(m, makeKey("x"))
	if m.showHelp {
		t.Error("any key should dismiss help")
	}
}

func TestRowsFor_ServersIncludesDefaultServerRow(t *testing.T) {
	cfg := freshCfg()
	rows := rowsFor(cfg, secServers)
	if len(rows) != len(cfg.Servers)+1 {
		t.Errorf("len(rows) = %d, want %d (servers + default_server)", len(rows), len(cfg.Servers)+1)
	}
	last := rows[len(rows)-1]
	if last.label != "default_server" {
		t.Errorf("last row label = %q, want default_server", last.label)
	}
}

func TestRowsFor_WatcherListsAllScalarLeaves(t *testing.T) {
	cfg := freshCfg()
	rows := rowsFor(cfg, secWatcher)
	if len(rows) != 4 {
		t.Errorf("watcher rows = %d, want 4 (enabled, debounce, exclude, sync)", len(rows))
	}
	want := map[string]bool{
		"enabled":            false,
		"debounce_ms":        false,
		"exclude":            false,
		"sync_interval_mins": false,
	}
	for _, r := range rows {
		want[r.label] = true
	}
	for label, seen := range want {
		if !seen {
			t.Errorf("missing row %q", label)
		}
	}
}

func TestView_RendersBothPanels(t *testing.T) {
	m := NewModel(freshCfg())
	m.width, m.height = 100, 30
	out := m.View()
	for _, expect := range []string{"Servers", "Watcher", "Indexing", "Projects", "Misc", "default"} {
		if !strings.Contains(out, expect) {
			t.Errorf("View missing %q\noutput:\n%s", expect, out)
		}
	}
}

func TestView_SensitiveKeyNeverLeaks(t *testing.T) {
	cfg := freshCfg()
	cfg.Servers[0].Key = "cix_super_secret_xyz"
	m := NewModel(cfg)
	m.width, m.height = 100, 30
	m.sectionIdx = int(secServers)
	out := m.View()
	if strings.Contains(out, "super_secret") {
		t.Errorf("sensitive key leaked into View output")
	}
	if strings.Contains(out, "cix_super_secret_xyz") {
		t.Errorf("sensitive key leaked into View output")
	}
}

func TestPingServer_RejectsBlank(t *testing.T) {
	if err := PingServer("", "k"); err == nil {
		t.Error("empty URL should fail")
	}
	if err := PingServer("http://x", ""); err == nil {
		t.Error("empty key should fail")
	}
}

func TestClamp(t *testing.T) {
	cases := []struct{ n, lo, hi, want int }{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{99, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, c := range cases {
		if got := clamp(c.n, c.lo, c.hi); got != c.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", c.n, c.lo, c.hi, got, c.want)
		}
	}
}
