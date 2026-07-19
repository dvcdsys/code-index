// Package tui implements the interactive configuration editor used by
// `cix config edit` and `cix config init`.
//
// The editor is a full-screen bubbletea program — Elm-style state
// machine, lipgloss for styling, bubbles/textinput for inline edits.
// Layout is lazygit-inspired: section list on the left, content panel
// on the right, persistent help bar at the bottom.
//
// Every edit goes through the existing config.SetByPath / SetServerURL /
// SetServerKey paths — the TUI never touches the YAML directly, so
// validation, schema rules, and persistence are exactly what the CLI's
// `cix config set` would apply.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dvcdsys/code-index/cli/internal/client"
	"github.com/dvcdsys/code-index/cli/internal/config"
)

// RunEdit boots the TUI against the current config. cfg is mutated via
// the config package's CRUD helpers — every successful keystroke writes
// straight to disk, so the function never needs an explicit Save step.
func RunEdit(cfg *config.Config) error {
	model := NewModel(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// RunInit is the fresh-install variant. If no servers exist yet we seed
// the implicit localhost default so the user has something to edit on
// the first screen, then hand off to the standard editor.
func RunInit(cfg *config.Config) error {
	if len(cfg.Servers) == 0 {
		if err := config.SetServerURL(config.DefaultServerName, "http://localhost:21847"); err != nil {
			return fmt.Errorf("seed default server: %w", err)
		}
	}
	return RunEdit(cfg)
}

// PingServer is a thin wrapper around client.Health used by the "test
// connection" action ('t' on a server row). Exported so the test file
// can exercise the error-mapping path without spinning up bubbletea.
func PingServer(url, key string) error {
	if url == "" {
		return fmt.Errorf("URL is required")
	}
	if key == "" {
		return fmt.Errorf("API key is required")
	}
	return client.New(url, key).Health()
}
