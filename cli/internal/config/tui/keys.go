package tui

import "github.com/charmbracelet/bubbles/key"

// keymap groups every binding the TUI responds to. Kept in one place so
// the help overlay and the Update switch agree on what's available.
type keymap struct {
	Up        key.Binding
	Down      key.Binding
	Left      key.Binding
	Right     key.Binding
	NextPanel key.Binding

	Enter   key.Binding
	Toggle  key.Binding
	Save    key.Binding
	Test    key.Binding
	Add     key.Binding
	Delete  key.Binding
	MarkDef key.Binding

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
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "left panel"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "right panel"),
		),
		NextPanel: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch panel"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "edit"),
		),
		Toggle: key.NewBinding(
			key.WithKeys(" ", "x"),
			key.WithHelp("space/x", "toggle bool"),
		),
		Save: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "save (no-op; sets save on edit)"),
		),
		Test: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "test connection"),
		),
		Add: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "add server"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete server"),
		),
		MarkDef: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "mark as default"),
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

// shortHelp returns the keys shown in the always-on status bar.
func (k keymap) shortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.NextPanel, k.Enter, k.Help, k.Quit}
}

// fullHelp returns all keys, grouped by purpose, for the ? overlay.
func (k keymap) fullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Left, k.Right, k.NextPanel},
		{k.Enter, k.Toggle, k.Save},
		{k.Add, k.Delete, k.MarkDef, k.Test},
		{k.Help, k.Quit},
	}
}
