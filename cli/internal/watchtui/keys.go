package watchtui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// keymap groups every binding the manager responds to. Kept in one place so
// the help overlay and the Update switch agree on what's available.
type keymap struct {
	Up   key.Binding
	Down key.Binding

	Stop      key.Binding
	Restart   key.Binding
	Start     key.Binding
	StartAuto key.Binding
	StopAll   key.Binding
	Delete    key.Binding
	ToggleAut key.Binding

	Detail  key.Binding
	Refresh key.Binding

	Help key.Binding
	Quit key.Binding
}

func newKeymap() keymap {
	return keymap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Stop: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "stop"),
		),
		Restart: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "restart"),
		),
		Start: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "start"),
		),
		StartAuto: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "start all auto"),
		),
		StopAll: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "stop all"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete project"),
		),
		ToggleAut: key.NewBinding(
			key.WithKeys("a", " "),
			key.WithHelp("a/space", "toggle auto_watch"),
		),
		Detail: key.NewBinding(
			key.WithKeys("l", "enter"),
			key.WithHelp("l/enter", "log"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "refresh"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "esc", "ctrl+c"),
			key.WithHelp("q/esc", "quit"),
		),
	}
}

// isAction reports whether msg is one of the mutating action keys — the set
// gated by Model.busy while a background action is in flight.
func (k keymap) isAction(msg tea.KeyMsg) bool {
	return key.Matches(msg, k.Stop, k.Restart, k.Start, k.StartAuto, k.StopAll, k.Delete, k.ToggleAut)
}

// shortHelp returns the keys shown in the always-on status bar.
func (k keymap) shortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Stop, k.Restart, k.Start, k.Delete, k.Help, k.Quit}
}

// fullHelp returns all keys, grouped by purpose, for the ? overlay.
func (k keymap) fullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down},
		{k.Stop, k.Restart, k.Start},
		{k.StartAuto, k.StopAll},
		{k.ToggleAut, k.Delete},
		{k.Detail, k.Refresh},
		{k.Help, k.Quit},
	}
}
