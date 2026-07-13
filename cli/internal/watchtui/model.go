package watchtui

import (
	"path/filepath"
	"time"
)

// Model is the full manager state. bubbletea is Elm-style: every message
// goes through Update(model, msg) → (model, cmd); View() is a pure read.
type Model struct {
	mgr Manager

	items  []Item
	cursor int

	// Best-effort server enrichment (path → last-indexed), applied as an
	// overlay so a stale/absent value never blocks the local row list.
	enrich map[string]*time.Time

	// enrichInFlight is true while a LastIndexed load is running. The tick
	// skips re-firing until the previous load lands (single-flight), so a
	// stalled server can't accumulate one hung request per tick.
	enrichInFlight bool

	// busy names the network-bound action currently running as a background
	// command (e.g. "starting alpha"). While non-empty, mutating keys are
	// rejected so two actions can't interleave daemon/config state; cleared
	// by actionDoneMsg.
	busy string

	// Detail (log tail) overlay.
	detail     bool
	detailPath string
	logLines   []string

	// Delete confirmation. When confirmDelete is set, input is trapped:
	// 'y' deletes confirmPath, any other key cancels.
	confirmDelete bool
	confirmPath   string

	// Help overlay toggled with ?.
	showHelp bool

	// Transient status line; cleared on the next keypress.
	statusMsg string
	statusErr bool

	width, height int
	styles        styles
	keys          keymap

	quitting bool
}

// NewModel builds the initial Model and populates the row list from the
// local sources (config + daemons) synchronously, so the first frame is
// already correct before any async enrichment lands.
func NewModel(mgr Manager) Model {
	m := Model{
		mgr:    mgr,
		styles: newStyles(),
		keys:   newKeymap(),
		// Init fires the first enrichment load; mark it in flight here
		// because Init has a value receiver and can't set the flag itself.
		enrichInFlight: true,
	}
	m.refresh()
	return m
}

// refresh recomputes the local row backbone (config + running daemons) and
// re-applies any enrichment. Cheap and network-free — safe to call from
// Update and on every tick.
func (m *Model) refresh() {
	items, err := m.mgr.List()
	if err != nil {
		m.setStatus("list: "+err.Error(), false)
		return
	}
	m.items = items
	m.applyEnrich()
	m.clampCursor()
}

// applyEnrich overlays the last-indexed times onto the current items.
func (m *Model) applyEnrich() {
	if m.enrich == nil {
		return
	}
	for i := range m.items {
		if t, ok := m.enrich[m.items[i].Path]; ok {
			m.items[i].LastIndexedAt = t
		}
	}
}

// selected returns the item under the cursor, or ok=false when the list is
// empty.
func (m Model) selected() (Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return Item{}, false
	}
	return m.items[m.cursor], true
}

// clampCursor keeps cursor inside [0, len(items)).
func (m *Model) clampCursor() {
	n := len(m.items)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// setStatus replaces the transient status line. ok=false renders in red.
func (m *Model) setStatus(msg string, ok bool) {
	m.statusMsg = msg
	m.statusErr = !ok
}

func (m *Model) clearStatus() {
	m.statusMsg = ""
	m.statusErr = false
}

// label is the short name shown in status messages.
func label(path string) string {
	if b := filepath.Base(path); b != "" && b != "." && b != "/" {
		return b
	}
	return path
}
