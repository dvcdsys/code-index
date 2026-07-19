package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/dvcdsys/code-index/cli/internal/config"
)

// sectionID enumerates the left-panel entries. Stable order — the View
// renders them in this sequence.
type sectionID int

const (
	secServers sectionID = iota
	secWatcher
	secIndexing
	secProjects
	secMisc
	numSections
)

// sectionLabel maps a sectionID to its display name.
func (s sectionID) Label() string {
	switch s {
	case secServers:
		return "Servers"
	case secWatcher:
		return "Watcher"
	case secIndexing:
		return "Indexing"
	case secProjects:
		return "Projects"
	case secMisc:
		return "Misc"
	}
	return "?"
}

// panel marks which of the two columns has focus. Keyboard navigation
// (Tab, h/l) flips this; the styled border highlights the active one.
type panel int

const (
	panelLeft panel = iota
	panelRight
)

// editPurpose distinguishes between editing an existing scalar (set by
// config.SetByPath) and editing a server entry's URL or key.
type editPurpose int

const (
	editPurposeScalar editPurpose = iota
	editPurposeServerURL
	editPurposeServerKey
	editPurposeServerName // only used during "add server" flow
)

// Model is the full TUI state. bubbletea is Elm-style: every keypress
// goes through Update(model, msg) → (model, cmd). View() reads model and
// returns a string.
type Model struct {
	cfg *config.Config

	// Navigation.
	active     panel
	sectionIdx int // 0..numSections-1
	rowIdx     int // selected row within the right panel

	// Edit mode: when true, all keys go to editInput except esc/enter.
	editing     bool
	editPurpose editPurpose
	editKey     string // schema key path being edited (scalar mode)
	editServer  int    // index into cfg.Servers (server-edit modes)
	editInput   textinput.Model
	editErr     string

	// "Add server" flow. Three sequential prompts: name → URL → key.
	addingServer bool
	addStep      int // 0=name, 1=url, 2=key
	addName      string
	addURL       string

	// Help overlay toggled with ?.
	showHelp bool

	// Transient status line (shown below status bar; fades on next action).
	statusMsg string
	statusErr bool

	// Layout.
	width, height int
	styles        styles
	keys          keymap

	quitting bool
}

// NewModel builds the initial Model with cfg loaded. cfg is mutated in
// place when edits land; the caller is the only owner.
func NewModel(cfg *config.Config) Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 200
	return Model{
		cfg:       cfg,
		active:    panelLeft,
		styles:    newStyles(),
		keys:      newKeymap(),
		editInput: ti,
	}
}

// numRowsRight returns how many rows the right panel renders for the
// current section. Edit-mode entry validates against this so the user
// can never select a non-existent row.
func (m Model) numRowsRight() int {
	switch sectionID(m.sectionIdx) {
	case secServers:
		// Each server contributes one row; "no servers" still shows a
		// hint row so the panel isn't empty.
		if len(m.cfg.Servers) == 0 {
			return 0
		}
		return len(m.cfg.Servers) + 1 // +1 for "default_server" select
	case secWatcher:
		return 4
	case secIndexing:
		return 2
	case secProjects:
		return len(m.cfg.Projects)
	case secMisc:
		return 1
	}
	return 0
}

// clampRow keeps rowIdx inside [0, numRowsRight). Called after any
// navigation or after a delete that shrinks the section.
func (m *Model) clampRow() {
	n := m.numRowsRight()
	if n == 0 {
		m.rowIdx = 0
		return
	}
	if m.rowIdx < 0 {
		m.rowIdx = 0
	}
	if m.rowIdx >= n {
		m.rowIdx = n - 1
	}
}

// setStatus replaces the transient status line. ok=false renders in red.
// The status survives until the next keypress (Update clears it).
func (m *Model) setStatus(msg string, ok bool) {
	m.statusMsg = msg
	m.statusErr = !ok
}

func (m *Model) clearStatus() {
	m.statusMsg = ""
	m.statusErr = false
}
