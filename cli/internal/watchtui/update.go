package watchtui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// refreshInterval is how often the manager re-lists watchers and re-fires
// the (best-effort) server enrichment.
const refreshInterval = 2 * time.Second

// tickMsg fires on the periodic refresh timer.
type tickMsg struct{}

// enrichMsg carries the result of the async server ListProjects call.
type enrichMsg struct {
	byPath map[string]*time.Time
	err    error
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// loadEnrichCmd fetches last-indexed times off the event loop. It only
// *returns* a message and never touches the Model, per the bubbletea
// contract — so it can never race a synchronous action.
func loadEnrichCmd(mgr Manager) tea.Cmd {
	return func() tea.Msg {
		byPath, err := mgr.LastIndexed()
		return enrichMsg{byPath: byPath, err: err}
	}
}

// Init kicks off the refresh ticker and the first enrichment load.
func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), loadEnrichCmd(m.mgr))
}

// Update is the central message handler. bubbletea serializes calls, so
// synchronous actions and the tick can never overlap.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refresh()
		return m, tea.Batch(tickCmd(), loadEnrichCmd(m.mgr))
	case enrichMsg:
		if msg.err == nil {
			m.enrich = msg.byPath
			m.applyEnrich()
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Overlays trap input: any key dismisses them.
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	if m.detail {
		if key.Matches(msg, m.keys.Quit) || key.Matches(msg, m.keys.Detail) {
			m.detail = false
		}
		return m, nil
	}
	// Delete confirmation traps all input: 'y' confirms, anything else cancels.
	if m.confirmDelete {
		m.confirmDelete = false
		if s := msg.String(); s == "y" || s == "Y" {
			m.doDelete()
		} else {
			m.setStatus("delete canceled", true)
		}
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
	case key.Matches(msg, m.keys.Up):
		m.cursor--
		m.clampCursor()
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.cursor++
		m.clampCursor()
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		m.refresh()
		m.setStatus("refreshed", true)
		return m, loadEnrichCmd(m.mgr)
	case key.Matches(msg, m.keys.Detail):
		m.openDetail()
		return m, nil
	case key.Matches(msg, m.keys.Stop):
		m.doStop()
		return m, nil
	case key.Matches(msg, m.keys.Start):
		m.doStart()
		return m, nil
	case key.Matches(msg, m.keys.Restart):
		m.doRestart()
		return m, nil
	case key.Matches(msg, m.keys.StartAuto):
		m.doStartAuto()
		return m, nil
	case key.Matches(msg, m.keys.StopAll):
		m.doStopAll()
		return m, nil
	case key.Matches(msg, m.keys.ToggleAut):
		m.doToggleAuto()
		return m, nil
	case key.Matches(msg, m.keys.Delete):
		if it, ok := m.selected(); ok {
			m.confirmDelete = true
			m.confirmPath = it.Path
		}
		return m, nil
	}
	return m, nil
}

// doToggleAuto flips the selected project's auto_watch flag in the local
// config. It does not start or stop anything — the flag only tells 'A'
// (start all auto) which projects to bring up.
func (m *Model) doToggleAuto() {
	it, ok := m.selected()
	if !ok {
		return
	}
	want := !it.AutoWatch
	if err := m.mgr.SetAutoWatch(it.Path, want); err != nil {
		m.setStatus("auto_watch: "+err.Error(), false)
		return
	}
	state := "off"
	if want {
		state = "on"
	}
	m.setStatus(fmt.Sprintf("auto_watch %s for %s", state, label(it.Path)), true)
	m.refresh()
}

// doDelete removes the confirmed project everywhere (watcher + server index +
// local config). Called only after the user answers 'y' to the prompt.
func (m *Model) doDelete() {
	if err := m.mgr.Delete(m.confirmPath); err != nil {
		m.setStatus("delete: "+err.Error(), false)
		m.refresh()
		return
	}
	m.setStatus("deleted "+label(m.confirmPath), true)
	m.refresh()
}

// doStop stops the selected watcher (if running).
func (m *Model) doStop() {
	it, ok := m.selected()
	if !ok {
		return
	}
	if !it.Running {
		m.setStatus(label(it.Path)+" is not running", false)
		return
	}
	if err := m.mgr.Stop(it.Path); err != nil {
		m.setStatus("stop: "+err.Error(), false)
		return
	}
	m.setStatus("stopped "+label(it.Path), true)
	m.refresh()
}

// doStart starts a watcher for the selected (stopped) row.
func (m *Model) doStart() {
	it, ok := m.selected()
	if !ok {
		return
	}
	if it.Running {
		m.setStatus(label(it.Path)+" already running", false)
		return
	}
	if err := m.mgr.Start(it.Path); err != nil {
		m.setStatus("start: "+err.Error(), false)
		return
	}
	m.setStatus("started "+label(it.Path), true)
	m.refresh()
}

// doRestart restarts the selected watcher.
func (m *Model) doRestart() {
	it, ok := m.selected()
	if !ok {
		return
	}
	if err := m.mgr.Restart(it.Path); err != nil {
		m.setStatus("restart: "+err.Error(), false)
		return
	}
	m.setStatus("restarted "+label(it.Path), true)
	m.refresh()
}

// doStartAuto starts every auto_watch project that isn't already running.
func (m *Model) doStartAuto() {
	n, err := m.mgr.StartAllAuto()
	if err != nil {
		if n > 0 {
			m.setStatus(fmt.Sprintf("started %d, then: %s", n, err.Error()), false)
		} else {
			m.setStatus("start-auto: "+err.Error(), false)
		}
		m.refresh()
		return
	}
	if n == 0 {
		m.setStatus("no auto_watch projects to start", true)
	} else {
		m.setStatus(fmt.Sprintf("started %d auto_watch watcher(s)", n), true)
	}
	m.refresh()
}

// doStopAll stops every running watcher.
func (m *Model) doStopAll() {
	n, err := m.mgr.StopAll()
	if err != nil {
		m.setStatus("stop-all: "+err.Error(), false)
		m.refresh()
		return
	}
	m.setStatus(fmt.Sprintf("stopped %d watcher(s)", n), true)
	m.refresh()
}

// openDetail loads the tail of the selected watcher's log into the detail
// overlay.
func (m *Model) openDetail() {
	it, ok := m.selected()
	if !ok {
		return
	}
	logPath, err := m.mgr.LogPath(it.Path)
	if err != nil {
		m.setStatus("log: "+err.Error(), false)
		return
	}
	m.detailPath = it.Path
	m.logLines = readTail(logPath, 200)
	m.detail = true
}
