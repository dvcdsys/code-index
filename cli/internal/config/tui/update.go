package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dvcdsys/code-index/cli/internal/config"
)

// Init satisfies tea.Model — nothing to fire at startup.
func (m Model) Init() tea.Cmd { return nil }

// Update is the central message handler. bubbletea calls it once per
// input event (key, resize, custom message). Every transition goes
// through here — the View() function is a pure read of the Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// In edit mode (scalar or server), most keys go to the text input.
	// Esc cancels, Enter commits.
	if m.editing {
		return m.handleEditKey(msg)
	}

	// "Add server" flow uses the same text input but a different commit path.
	if m.addingServer {
		return m.handleAddKey(msg)
	}

	// Help overlay traps every key — any input dismisses it.
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	m.clearStatus()

	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showHelp = true
		return m, nil
	case key.Matches(msg, m.keys.NextPanel):
		if m.active == panelLeft {
			m.active = panelRight
		} else {
			m.active = panelLeft
		}
		return m, nil
	case key.Matches(msg, m.keys.Left):
		m.active = panelLeft
		return m, nil
	case key.Matches(msg, m.keys.Right):
		m.active = panelRight
		return m, nil
	case key.Matches(msg, m.keys.Up):
		return m.moveSelection(-1), nil
	case key.Matches(msg, m.keys.Down):
		return m.moveSelection(+1), nil
	case key.Matches(msg, m.keys.Enter):
		return m.activateRow()
	case key.Matches(msg, m.keys.Toggle):
		return m.toggleRow()
	case key.Matches(msg, m.keys.Add):
		if sectionID(m.sectionIdx) == secServers {
			return m.beginAddServer()
		}
		return m, nil
	case key.Matches(msg, m.keys.Delete):
		if sectionID(m.sectionIdx) == secServers && m.active == panelRight {
			return m.deleteSelectedServer()
		}
		return m, nil
	case key.Matches(msg, m.keys.MarkDef):
		if sectionID(m.sectionIdx) == secServers && m.active == panelRight {
			return m.markDefaultServer()
		}
		return m, nil
	case key.Matches(msg, m.keys.Test):
		if sectionID(m.sectionIdx) == secServers && m.active == panelRight {
			return m.testSelectedServer()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) moveSelection(delta int) Model {
	if m.active == panelLeft {
		m.sectionIdx = clamp(m.sectionIdx+delta, 0, int(numSections)-1)
		m.rowIdx = 0
	} else {
		n := m.numRowsRight()
		if n == 0 {
			m.rowIdx = 0
		} else {
			m.rowIdx = clamp(m.rowIdx+delta, 0, n-1)
		}
	}
	return m
}

func (m Model) activateRow() (tea.Model, tea.Cmd) {
	// Enter on left panel == "focus right panel".
	if m.active == panelLeft {
		m.active = panelRight
		m.rowIdx = 0
		return m, nil
	}
	rows := rowsFor(m.cfg, sectionID(m.sectionIdx))
	if m.rowIdx >= len(rows) {
		return m, nil
	}
	r := rows[m.rowIdx]
	switch r.kind {
	case rowKindBoolToggle:
		return m.toggleBoolByPath(r.schemaKey)
	case rowKindScalarEdit:
		return m.beginEditScalar(r.schemaKey)
	case rowKindServerEdit:
		return m.beginEditServer(r.serverIdx, editPurposeServerURL)
	case rowKindDefaultPick:
		return m.cycleDefaultServer()
	}
	return m, nil
}

func (m Model) toggleRow() (tea.Model, tea.Cmd) {
	if m.active != panelRight {
		return m, nil
	}
	rows := rowsFor(m.cfg, sectionID(m.sectionIdx))
	if m.rowIdx >= len(rows) {
		return m, nil
	}
	if rows[m.rowIdx].kind == rowKindBoolToggle {
		return m.toggleBoolByPath(rows[m.rowIdx].schemaKey)
	}
	return m, nil
}

func (m Model) toggleBoolByPath(path string) (tea.Model, tea.Cmd) {
	leaf, ok := findLeaf(m.cfg, path)
	if !ok {
		m.setStatus(fmt.Sprintf("unknown key %q", path), false)
		return m, nil
	}
	newVal := !leaf.Value.Bool()
	if err := config.SetByPath(path, strconv.FormatBool(newVal)); err != nil {
		m.setStatus(err.Error(), false)
		return m, nil
	}
	m.setStatus(fmt.Sprintf("✓ %s = %v", path, newVal), true)
	return m, nil
}

func (m Model) beginEditScalar(path string) (tea.Model, tea.Cmd) {
	leaf, ok := findLeaf(m.cfg, path)
	if !ok {
		m.setStatus(fmt.Sprintf("unknown key %q", path), false)
		return m, nil
	}
	m.editing = true
	m.editPurpose = editPurposeScalar
	m.editKey = path
	m.editErr = ""
	m.editInput.SetValue(rawScalarValue(leaf))
	m.editInput.CursorEnd()
	if leaf.Sensitive() {
		m.editInput.EchoMode = textinput.EchoPassword
	} else {
		m.editInput.EchoMode = textinput.EchoNormal
	}
	m.editInput.Focus()
	return m, nil
}

func (m Model) beginEditServer(idx int, purpose editPurpose) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.cfg.Servers) {
		return m, nil
	}
	m.editing = true
	m.editPurpose = purpose
	m.editServer = idx
	m.editErr = ""
	current := ""
	if purpose == editPurposeServerURL {
		current = m.cfg.Servers[idx].URL
		m.editInput.EchoMode = textinput.EchoNormal
	} else {
		// Editing the key — never pre-fill, never echo.
		current = ""
		m.editInput.EchoMode = textinput.EchoPassword
	}
	m.editInput.SetValue(current)
	m.editInput.CursorEnd()
	m.editInput.Focus()
	return m, nil
}

func (m Model) cycleDefaultServer() (tea.Model, tea.Cmd) {
	if len(m.cfg.Servers) == 0 {
		return m, nil
	}
	// Find current and pick the next one.
	curIdx := 0
	for i, s := range m.cfg.Servers {
		if s.Name == m.cfg.DefaultServer {
			curIdx = i
			break
		}
	}
	next := m.cfg.Servers[(curIdx+1)%len(m.cfg.Servers)].Name
	if err := config.SetDefaultServer(next); err != nil {
		m.setStatus(err.Error(), false)
		return m, nil
	}
	m.setStatus(fmt.Sprintf("default_server = %s", next), true)
	return m, nil
}

func (m Model) markDefaultServer() (tea.Model, tea.Cmd) {
	if m.rowIdx >= len(m.cfg.Servers) {
		return m, nil
	}
	name := m.cfg.Servers[m.rowIdx].Name
	if err := config.SetDefaultServer(name); err != nil {
		m.setStatus(err.Error(), false)
		return m, nil
	}
	m.setStatus(fmt.Sprintf("default_server = %s", name), true)
	return m, nil
}

func (m Model) deleteSelectedServer() (tea.Model, tea.Cmd) {
	if m.rowIdx >= len(m.cfg.Servers) {
		return m, nil
	}
	name := m.cfg.Servers[m.rowIdx].Name
	reassigned, err := config.RemoveServer(name)
	if err != nil {
		m.setStatus(err.Error(), false)
		return m, nil
	}
	msg := fmt.Sprintf("removed server %q", name)
	if reassigned != "" {
		msg += fmt.Sprintf("; default → %q", reassigned)
	}
	m.setStatus(msg, true)
	m.clampRow()
	return m, nil
}

func (m Model) testSelectedServer() (tea.Model, tea.Cmd) {
	if m.rowIdx >= len(m.cfg.Servers) {
		return m, nil
	}
	s := m.cfg.Servers[m.rowIdx]
	if err := PingServer(s.URL, s.Key); err != nil {
		m.setStatus(fmt.Sprintf("✗ %s: %v", s.Name, err), false)
		return m, nil
	}
	m.setStatus(fmt.Sprintf("✓ %s reachable", s.Name), true)
	return m, nil
}

func (m Model) beginAddServer() (tea.Model, tea.Cmd) {
	m.addingServer = true
	m.addStep = 0
	m.addName = ""
	m.addURL = ""
	m.editInput.SetValue("")
	m.editInput.EchoMode = textinput.EchoNormal
	m.editInput.Focus()
	return m, nil
}

func (m Model) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.editErr = ""
		m.editInput.Blur()
		m.setStatus("edit cancelled", false)
		return m, nil
	case "enter":
		return m.commitEdit()
	}
	var cmd tea.Cmd
	m.editInput, cmd = m.editInput.Update(msg)
	return m, cmd
}

func (m Model) commitEdit() (tea.Model, tea.Cmd) {
	value := strings.TrimRight(m.editInput.Value(), "\r\n")
	switch m.editPurpose {
	case editPurposeScalar:
		if err := config.SetByPath(m.editKey, value); err != nil {
			m.editErr = err.Error()
			return m, nil
		}
		m.editing = false
		m.editInput.Blur()
		m.setStatus(fmt.Sprintf("✓ %s saved", m.editKey), true)
		return m, nil
	case editPurposeServerURL:
		if m.editServer < 0 || m.editServer >= len(m.cfg.Servers) {
			m.editing = false
			return m, nil
		}
		name := m.cfg.Servers[m.editServer].Name
		if err := config.SetServerURL(name, value); err != nil {
			m.editErr = err.Error()
			return m, nil
		}
		// Chain: after URL, prompt for key.
		m.editPurpose = editPurposeServerKey
		m.editInput.SetValue("")
		m.editInput.EchoMode = textinput.EchoPassword
		m.editErr = ""
		return m, nil
	case editPurposeServerKey:
		if m.editServer < 0 || m.editServer >= len(m.cfg.Servers) {
			m.editing = false
			return m, nil
		}
		name := m.cfg.Servers[m.editServer].Name
		if value == "" {
			// Empty input on the key step means "skip — keep current".
			m.editing = false
			m.editInput.Blur()
			m.setStatus(fmt.Sprintf("✓ %s URL updated", name), true)
			return m, nil
		}
		if err := config.SetServerKey(name, value); err != nil {
			m.editErr = err.Error()
			return m, nil
		}
		m.editing = false
		m.editInput.Blur()
		m.setStatus(fmt.Sprintf("✓ %s URL+key updated", name), true)
		return m, nil
	}
	m.editing = false
	return m, nil
}

func (m Model) handleAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.addingServer = false
		m.editInput.Blur()
		m.setStatus("add cancelled", false)
		return m, nil
	case "enter":
		return m.commitAddStep()
	}
	var cmd tea.Cmd
	m.editInput, cmd = m.editInput.Update(msg)
	return m, cmd
}

func (m Model) commitAddStep() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.editInput.Value())
	switch m.addStep {
	case 0: // name
		if value == "" {
			m.editErr = "name must not be empty"
			return m, nil
		}
		m.addName = value
		m.addStep = 1
		m.editInput.SetValue("")
		m.editInput.EchoMode = textinput.EchoNormal
		m.editErr = ""
		return m, nil
	case 1: // url
		if value == "" {
			m.editErr = "URL must not be empty"
			return m, nil
		}
		m.addURL = value
		m.addStep = 2
		m.editInput.SetValue("")
		m.editInput.EchoMode = textinput.EchoPassword
		m.editErr = ""
		return m, nil
	case 2: // key — optional; empty means "add server with no key"
		if err := config.SetServerURL(m.addName, m.addURL); err != nil {
			m.editErr = err.Error()
			return m, nil
		}
		if value != "" {
			if err := config.SetServerKey(m.addName, value); err != nil {
				m.editErr = err.Error()
				return m, nil
			}
		}
		m.addingServer = false
		m.editInput.Blur()
		m.setStatus(fmt.Sprintf("✓ added server %q", m.addName), true)
		return m, nil
	}
	return m, nil
}

// clamp keeps n inside [lo, hi]. Stdlib doesn't ship one yet.
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
