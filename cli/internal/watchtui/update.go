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

// actionDoneMsg reports the completion of a background action started by
// runAsync. status is ready-to-render text; isErr picks the style.
type actionDoneMsg struct {
	status string
	isErr  bool
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

// maybeEnrichCmd fires a LastIndexed load unless one is already in flight —
// single-flight, so a stalled server (the client timeout is minutes) can't
// pile up a hung goroutine per tick.
func (m *Model) maybeEnrichCmd() tea.Cmd {
	if m.enrichInFlight {
		return nil
	}
	m.enrichInFlight = true
	return loadEnrichCmd(m.mgr)
}

// runAsync marks the model busy and returns a command running fn off the
// event loop. Network-bound actions (start/restart/start-auto/delete) go
// through here: their server calls can block for the full client timeout,
// and running them inside Update would freeze rendering and input.
func (m *Model) runAsync(busy string, fn func() actionDoneMsg) tea.Cmd {
	m.busy = busy
	m.setStatus(busy+"…", true)
	return func() tea.Msg { return fn() }
}

// Init kicks off the refresh ticker and the first enrichment load (marked
// in flight by NewModel).
func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), loadEnrichCmd(m.mgr))
}

// Update is the central message handler. bubbletea serializes calls, so
// state transitions never overlap; background work (enrichment, network
// actions) only ever comes back as a message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refresh()
		return m, tea.Batch(tickCmd(), m.maybeEnrichCmd())
	case enrichMsg:
		m.enrichInFlight = false
		if msg.err == nil {
			m.enrich = msg.byPath
			m.applyEnrich()
		}
		return m, nil
	case actionDoneMsg:
		m.busy = ""
		m.setStatus(msg.status, !msg.isErr)
		m.refresh()
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
			return m, m.doDelete()
		}
		m.setStatus("delete canceled", true)
		return m, nil
	}

	m.clearStatus()

	// One background action at a time: while one is in flight, reject the
	// other mutating keys (navigation, refresh, and overlays stay available)
	// so daemon/config mutations can't interleave.
	if m.busy != "" && m.keys.isAction(msg) {
		m.setStatus("still "+m.busy+"…", false)
		return m, nil
	}

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
		return m, m.maybeEnrichCmd()
	case key.Matches(msg, m.keys.Detail):
		m.openDetail()
		return m, nil
	case key.Matches(msg, m.keys.Stop):
		m.doStop()
		return m, nil
	case key.Matches(msg, m.keys.Start):
		return m, m.doStart()
	case key.Matches(msg, m.keys.Restart):
		return m, m.doRestart()
	case key.Matches(msg, m.keys.StartAuto):
		return m, m.doStartAuto()
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
// local config). Called only after the user answers 'y' to the prompt. The
// server delete runs in the background — it's a network call.
func (m *Model) doDelete() tea.Cmd {
	mgr, path := m.mgr, m.confirmPath
	return m.runAsync("deleting "+label(path), func() actionDoneMsg {
		if err := mgr.Delete(path); err != nil {
			return actionDoneMsg{status: "delete: " + err.Error(), isErr: true}
		}
		return actionDoneMsg{status: "deleted " + label(path)}
	})
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

// doStart starts a watcher for the selected (stopped) row in the background
// — the preflight talks to the server.
func (m *Model) doStart() tea.Cmd {
	it, ok := m.selected()
	if !ok {
		return nil
	}
	if it.Running {
		m.setStatus(label(it.Path)+" already running", false)
		return nil
	}
	mgr, path := m.mgr, it.Path
	return m.runAsync("starting "+label(path), func() actionDoneMsg {
		if err := mgr.Start(path); err != nil {
			return actionDoneMsg{status: "start: " + err.Error(), isErr: true}
		}
		return actionDoneMsg{status: "started " + label(path)}
	})
}

// doRestart restarts the selected watcher in the background — the preflight
// talks to the server.
func (m *Model) doRestart() tea.Cmd {
	it, ok := m.selected()
	if !ok {
		return nil
	}
	mgr, path := m.mgr, it.Path
	return m.runAsync("restarting "+label(path), func() actionDoneMsg {
		if err := mgr.Restart(path); err != nil {
			return actionDoneMsg{status: "restart: " + err.Error(), isErr: true}
		}
		return actionDoneMsg{status: "restarted " + label(path)}
	})
}

// doStartAuto starts every auto_watch project that isn't already running,
// in the background — each start is preflighted against the server.
func (m *Model) doStartAuto() tea.Cmd {
	mgr := m.mgr
	return m.runAsync("starting auto_watch projects", func() actionDoneMsg {
		n, err := mgr.StartAllAuto()
		switch {
		case err != nil && n > 0:
			return actionDoneMsg{status: fmt.Sprintf("started %d, then: %s", n, err.Error()), isErr: true}
		case err != nil:
			return actionDoneMsg{status: "start-auto: " + err.Error(), isErr: true}
		case n == 0:
			return actionDoneMsg{status: "no auto_watch projects to start"}
		default:
			return actionDoneMsg{status: fmt.Sprintf("started %d auto_watch watcher(s)", n)}
		}
	})
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
