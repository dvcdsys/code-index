// Package watchtui implements the interactive watcher manager used by
// `cix watch manage`.
//
// It is a full-screen bubbletea program — Elm-style state machine, lipgloss
// for styling — mirroring internal/config/tui. The list unites the local
// known-project set (~/.cix/config.yaml `projects:`) with the running
// watcher daemons, so the user can see every watcher at a glance and stop,
// restart, or start any of them from one screen.
//
// Actions on the daemon layer (stop/start/restart) are local and run
// synchronously inside Update; only the last-indexed enrichment touches the
// network, and it does so off the event loop via a tea.Cmd.
package watchtui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run boots the manager TUI against mgr and blocks until the user quits.
func Run(mgr Manager) error {
	p := tea.NewProgram(NewModel(mgr), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
