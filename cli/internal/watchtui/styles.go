package watchtui

import "github.com/charmbracelet/lipgloss"

// Palette mirrors internal/config/tui — mid-tones that adapt to light and
// dark terminals via lipgloss's color detection.
var (
	colAccent    = lipgloss.AdaptiveColor{Light: "#005577", Dark: "#7dd3fc"}
	colMuted     = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}
	colActiveBdr = lipgloss.AdaptiveColor{Light: "#005577", Dark: "#7dd3fc"}
	colSel       = lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}
	colSelBg     = lipgloss.AdaptiveColor{Light: "#cce5f0", Dark: "#1e3a8a"}
	colOK        = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#86efac"}
	colErr       = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#fca5a5"}
	colWarn      = lipgloss.AdaptiveColor{Light: "#a16207", Dark: "#fcd34d"}
	colAuto      = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#c4b5fd"}
)

// styles is a flat bundle of every reusable lipgloss.Style, built once in
// newStyles() and stored on the Model so View() does no allocation.
type styles struct {
	panel     lipgloss.Style
	header    lipgloss.Style
	row       lipgloss.Style
	rowSel    lipgloss.Style
	muted     lipgloss.Style
	dot       lipgloss.Style
	dotDimmed lipgloss.Style
	autoTag   lipgloss.Style
	statusBar lipgloss.Style
	statusOK  lipgloss.Style
	statusErr lipgloss.Style
	statusKey lipgloss.Style
	confirm   lipgloss.Style
}

func newStyles() styles {
	return styles{
		panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(colActiveBdr).
			Padding(0, 1),
		header:    lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		row:       lipgloss.NewStyle(),
		rowSel:    lipgloss.NewStyle().Foreground(colSel).Background(colSelBg).Bold(true),
		muted:     lipgloss.NewStyle().Foreground(colMuted),
		dot:       lipgloss.NewStyle().Foreground(colOK),
		dotDimmed: lipgloss.NewStyle().Foreground(colMuted),
		autoTag:   lipgloss.NewStyle().Foreground(colAuto).Italic(true),
		statusBar: lipgloss.NewStyle().Foreground(colMuted),
		statusOK:  lipgloss.NewStyle().Foreground(colOK).Bold(true),
		statusErr: lipgloss.NewStyle().Foreground(colErr).Bold(true),
		statusKey: lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		confirm:   lipgloss.NewStyle().Foreground(colWarn).Bold(true),
	}
}
