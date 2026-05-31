package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — chosen so the TUI keeps decent contrast on both dark and
// light terminal themes. We avoid pure white/black and stick to mid-tones
// that get auto-adapted by lipgloss's terminal-color detection.
var (
	colAccent      = lipgloss.AdaptiveColor{Light: "#005577", Dark: "#7dd3fc"}
	colMuted       = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}
	colBorder      = lipgloss.AdaptiveColor{Light: "#cccccc", Dark: "#444444"}
	colActiveBdr   = lipgloss.AdaptiveColor{Light: "#005577", Dark: "#7dd3fc"}
	colSel         = lipgloss.AdaptiveColor{Light: "#000000", Dark: "#ffffff"}
	colSelBg       = lipgloss.AdaptiveColor{Light: "#cce5f0", Dark: "#1e3a8a"}
	colOK          = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#86efac"}
	colWarn        = lipgloss.AdaptiveColor{Light: "#a16207", Dark: "#fcd34d"}
	colErr         = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#fca5a5"}
	colSensitive   = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#c4b5fd"}
)

// styles is a flat bundle of every reusable lipgloss.Style. Built once in
// New() and stored on the Model so each View() call does no allocation.
type styles struct {
	leftPanel       lipgloss.Style
	leftPanelActive lipgloss.Style
	rightPanel      lipgloss.Style
	rightPanelActive lipgloss.Style

	sectionRow      lipgloss.Style
	sectionRowSel   lipgloss.Style
	sectionCount    lipgloss.Style

	rowKey     lipgloss.Style
	rowValue   lipgloss.Style
	rowKeySel  lipgloss.Style
	rowValSel  lipgloss.Style
	rowMuted   lipgloss.Style

	statusBar     lipgloss.Style
	statusOK      lipgloss.Style
	statusErr     lipgloss.Style
	statusKey     lipgloss.Style

	header        lipgloss.Style
	headerActive  lipgloss.Style

	editLabel  lipgloss.Style
	editError  lipgloss.Style

	dot           lipgloss.Style
	dotDimmed     lipgloss.Style
	sensitiveTag  lipgloss.Style
}

func newStyles() styles {
	border := lipgloss.RoundedBorder()
	return styles{
		leftPanel: lipgloss.NewStyle().
			Border(border).BorderForeground(colBorder).
			Padding(0, 1),
		leftPanelActive: lipgloss.NewStyle().
			Border(border).BorderForeground(colActiveBdr).
			Padding(0, 1),
		rightPanel: lipgloss.NewStyle().
			Border(border).BorderForeground(colBorder).
			Padding(0, 1),
		rightPanelActive: lipgloss.NewStyle().
			Border(border).BorderForeground(colActiveBdr).
			Padding(0, 1),

		sectionRow:    lipgloss.NewStyle().Padding(0, 1),
		sectionRowSel: lipgloss.NewStyle().Padding(0, 1).Background(colSelBg).Foreground(colSel).Bold(true),
		sectionCount:  lipgloss.NewStyle().Foreground(colMuted),

		rowKey:    lipgloss.NewStyle().Foreground(colAccent),
		rowValue:  lipgloss.NewStyle(),
		rowKeySel: lipgloss.NewStyle().Foreground(colSel).Background(colSelBg).Bold(true),
		rowValSel: lipgloss.NewStyle().Foreground(colSel).Background(colSelBg),
		rowMuted:  lipgloss.NewStyle().Foreground(colMuted),

		statusBar: lipgloss.NewStyle().Foreground(colMuted),
		statusOK:  lipgloss.NewStyle().Foreground(colOK).Bold(true),
		statusErr: lipgloss.NewStyle().Foreground(colErr).Bold(true),
		statusKey: lipgloss.NewStyle().Foreground(colAccent).Bold(true),

		header:       lipgloss.NewStyle().Foreground(colMuted).Bold(true),
		headerActive: lipgloss.NewStyle().Foreground(colAccent).Bold(true),

		editLabel: lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		editError: lipgloss.NewStyle().Foreground(colErr),

		dot:          lipgloss.NewStyle().Foreground(colOK),
		dotDimmed:    lipgloss.NewStyle().Foreground(colMuted),
		sensitiveTag: lipgloss.NewStyle().Foreground(colSensitive).Italic(true),
	}
}
