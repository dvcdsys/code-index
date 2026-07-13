// Package watchtui implements the interactive watcher manager used by
// `cix watch manage`.
//
// It is a full-screen bubbletea program — Elm-style state machine, lipgloss
// for styling — mirroring internal/config/tui. The list unites the local
// known-project set (~/.cix/config.yaml `projects:`) with the running
// watcher daemons, so the user can see every watcher at a glance and stop,
// restart, or start any of them from one screen.
//
// Local actions (stop, stop-all, auto_watch toggle) run synchronously inside
// Update — they are filesystem-only and microsecond-fast. Anything that talks
// to the server (start/restart preflight, start-all-auto, delete, and the
// last-indexed enrichment) runs off the event loop as a tea.Cmd, so a slow or
// unreachable server can never freeze rendering or input. A busy flag keeps
// two background actions from interleaving, and the enrichment is
// single-flight so a stalled server can't pile up one hung request per tick.
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
