package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"fyne.io/systray"
)

// The menu bar UI.
//
// systray.Run takes over the calling goroutine and must own the main one — on
// macOS the status item lives on the AppKit main thread. Everything else here
// runs in goroutines and only touches systray through its setters, which are
// safe to call from anywhere.

type menu struct {
	bundle bundle
	poll   *poller
	stop   chan struct{}

	statusItem     *systray.MenuItem
	embeddingsItem *systray.MenuItem
	modelItem      *systray.MenuItem
	startStopItem  *systray.MenuItem
	dashboardItem  *systray.MenuItem
}

func runMenu(b bundle) {
	m := &menu{bundle: b, poll: newPoller(), stop: make(chan struct{})}
	systray.Run(m.onReady, m.onExit)
}

func (m *menu) onReady() {
	if icon, err := os.ReadFile(filepath.Join(m.bundle.Resources, "cixTemplate@2x.png")); err == nil {
		// SetTemplateIcon, not SetIcon: macOS recolours a template image for
		// dark mode, for a tinted menu bar and for the pressed state, using
		// only its alpha channel. A coloured icon here is a smudge at 18 px and
		// unreadable in dark mode.
		systray.SetTemplateIcon(icon, icon)
	} else {
		systray.SetTitle("cix")
	}
	systray.SetTooltip("cix — semantic code search")

	m.statusItem = systray.AddMenuItem("cix-server: …", "")
	m.statusItem.Disable()
	m.embeddingsItem = systray.AddMenuItem("Embeddings: …", "")
	m.embeddingsItem.Disable()
	m.modelItem = systray.AddMenuItem("", "")
	m.modelItem.Disable()
	m.modelItem.Hide()

	systray.AddSeparator()
	m.startStopItem = systray.AddMenuItem("Start Server", "")
	m.dashboardItem = systray.AddMenuItem("Open Dashboard", "Open the cix dashboard in your browser")

	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit cix", "Quit the menu bar app; the server keeps running")

	go m.poll.run(m.stop)
	go m.watch()

	go func() {
		for {
			select {
			case <-m.startStopItem.ClickedCh:
				go m.toggleServer()
			case <-m.dashboardItem.ClickedCh:
				go m.openDashboard()
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (m *menu) onExit() {
	close(m.stop)
}

// watch redraws the menu whenever the poller reports a change.
func (m *menu) watch() {
	for {
		select {
		case <-m.stop:
			return
		case <-m.poll.changed:
			m.render(m.poll.snapshotNow())
		}
	}
}

func (m *menu) render(s snapshot) {
	m.statusItem.SetTitle(s.ServerLine())
	m.embeddingsItem.SetTitle(s.EmbeddingsLine())

	if line := s.ModelLine(); line != "" {
		m.modelItem.SetTitle(line)
		m.modelItem.Show()
	} else {
		m.modelItem.Hide()
	}

	switch {
	case !s.Managed:
		// Another installation owns the launchd label. Showing an enabled
		// Start button that would fight it — or worse, silently repoint it — is
		// the wrong behaviour; observing is useful, interfering is not.
		m.startStopItem.SetTitle("Start Server (managed externally)")
		m.startStopItem.Disable()
	case s.State == stateRunning:
		m.startStopItem.SetTitle("Stop Server")
		m.startStopItem.Enable()
	case s.State == stateStarting:
		m.startStopItem.SetTitle("Starting…")
		m.startStopItem.Disable()
	default:
		m.startStopItem.SetTitle("Start Server")
		m.startStopItem.Enable()
	}

	if s.State == stateRunning {
		m.dashboardItem.Enable()
	} else {
		m.dashboardItem.Disable()
	}
}

func (m *menu) toggleServer() {
	s := m.poll.snapshotNow()
	if !s.Managed {
		return
	}

	var err error
	if s.State == stateRunning {
		err = stopServer()
	} else {
		// Re-point the agent at this bundle before starting. The app may have
		// been moved or replaced by an update since the files were written.
		if err = writeLaunchdFiles(m.bundle, autostartEnabled()); err == nil {
			err = startServer()
		}
	}
	if err != nil {
		_ = alert("cix", fmt.Sprintf("Could not %s the server.\n\n%v",
			map[bool]string{true: "stop", false: "start"}[s.State == stateRunning], err))
	}
	m.poll.refresh()
}

func (m *menu) openDashboard() {
	vars, err := readServerEnv()
	if err != nil {
		_ = alert("cix", "cix is not set up yet.")
		return
	}
	if err := exec.Command("open", dashboardURL(vars)).Run(); err != nil {
		_ = alert("cix", fmt.Sprintf("Could not open the dashboard.\n\n%v", err))
	}
}
