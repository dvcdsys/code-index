package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/anthropics/code-index/cli/internal/config"
)

// View renders the full screen. bubbletea calls this once per Update.
//
// Layout:
//
//	┌─ sections ─┬─ <selected section> ─────────────┐
//	│ ▶ Servers  │  ...rows...                       │
//	│   Watcher  │                                   │
//	│   …        │                                   │
//	└────────────┴───────────────────────────────────┘
//	 status / edit prompt
//	 short-help bar (always visible)
//
// The help overlay (when shown) replaces the body but keeps the bars
// underneath for context.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "initializing…"
	}

	bodyH := m.height - 3 // 1 status, 1 short-help, 1 spacing
	if bodyH < 4 {
		bodyH = 4
	}

	if m.showHelp {
		return m.renderHelp(bodyH)
	}

	leftW := 22
	rightW := m.width - leftW - 4 // borders + padding
	if rightW < 20 {
		rightW = 20
	}

	left := m.renderSections(leftW, bodyH)
	right := m.renderRightPanel(rightW, bodyH)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	statusLine := m.renderStatusOrEdit(m.width)
	helpLine := m.renderShortHelp(m.width)

	return lipgloss.JoinVertical(lipgloss.Left, body, statusLine, helpLine)
}

// renderSections draws the left panel — the list of section names with
// the current selection highlighted. Width and height are the inner box
// size; borders add 2 each.
func (m Model) renderSections(w, h int) string {
	var lines []string
	for i := 0; i < int(numSections); i++ {
		s := sectionID(i)
		marker := "  "
		if i == m.sectionIdx {
			if m.active == panelLeft {
				marker = "▶ "
			} else {
				marker = "● "
			}
		}
		count := sectionCount(m.cfg, s)
		label := fmt.Sprintf("%s%-9s %s", marker, s.Label(), m.styles.sectionCount.Render(countLabel(count)))
		if i == m.sectionIdx {
			lines = append(lines, m.styles.sectionRowSel.Render(label))
		} else {
			lines = append(lines, m.styles.sectionRow.Render(label))
		}
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}

	style := m.styles.leftPanel
	if m.active == panelLeft {
		style = m.styles.leftPanelActive
	}
	return style.Width(w).Height(h).Render(strings.Join(lines, "\n"))
}

// sectionCount returns the number that appears as a small "n" badge next
// to the section label — total items for lists, total tunable fields for
// scalar groups. Cheap to recompute on every View() call.
func sectionCount(cfg *config.Config, s sectionID) int {
	switch s {
	case secServers:
		return len(cfg.Servers)
	case secProjects:
		return len(cfg.Projects)
	case secWatcher:
		return 4
	case secIndexing:
		return 2
	}
	return 0
}

func countLabel(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// renderRightPanel draws the content panel for the selected section.
func (m Model) renderRightPanel(w, h int) string {
	rows := rowsFor(m.cfg, sectionID(m.sectionIdx))

	title := m.styles.header.Render(sectionID(m.sectionIdx).Label())
	if m.active == panelRight {
		title = m.styles.headerActive.Render(sectionID(m.sectionIdx).Label())
	}

	var lines []string
	lines = append(lines, title, "")

	if len(rows) == 0 {
		lines = append(lines, m.styles.rowMuted.Render(m.emptySectionHint()))
	}

	keyW := keyColumnWidth(rows)
	for i, r := range rows {
		selected := i == m.rowIdx && m.active == panelRight

		keyText := r.label
		valText := r.value
		if r.sensitive && !selected {
			valText = m.styles.sensitiveTag.Render(valText)
		}

		var line string
		switch {
		case selected:
			line = m.styles.rowKeySel.Render(padRight(keyText, keyW)) +
				m.styles.rowValSel.Render("  "+valText)
		default:
			line = m.styles.rowKey.Render(padRight(keyText, keyW)) +
				"  " + m.styles.rowValue.Render(valText)
		}
		lines = append(lines, line)
	}

	// Section-specific footer (action hints).
	footer := m.sectionFooter()
	if footer != "" {
		// Pad up so the footer pins to the bottom.
		for len(lines) < h-3 {
			lines = append(lines, "")
		}
		lines = append(lines, m.styles.rowMuted.Render(footer))
	}

	style := m.styles.rightPanel
	if m.active == panelRight {
		style = m.styles.rightPanelActive
	}
	return style.Width(w).Height(h).Render(strings.Join(lines, "\n"))
}

func (m Model) emptySectionHint() string {
	switch sectionID(m.sectionIdx) {
	case secServers:
		return "no servers configured — press 'a' to add one"
	case secProjects:
		return "no projects — register one with `cix init`"
	}
	return ""
}

func (m Model) sectionFooter() string {
	switch sectionID(m.sectionIdx) {
	case secServers:
		return "enter edit · a add · d delete · m mark default · t test"
	case secWatcher, secIndexing:
		return "enter edit · space toggle bool"
	case secProjects:
		return "managed via `cix init` / dashboard"
	}
	return ""
}

func keyColumnWidth(rows []row) int {
	w := 12
	for _, r := range rows {
		if l := lipgloss.Width(r.label); l > w {
			w = l
		}
	}
	if w > 32 {
		w = 32
	}
	return w
}

func padRight(s string, w int) string {
	if lipgloss.Width(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// renderStatusOrEdit shows either the active edit input or the last
// status message. Edit takes precedence — there's no confusion about
// where keystrokes are going.
func (m Model) renderStatusOrEdit(w int) string {
	if m.editing {
		label := m.editLabel()
		line := m.styles.editLabel.Render(label+":") + " " + m.editInput.View()
		if m.editErr != "" {
			line += "  " + m.styles.editError.Render(m.editErr)
		}
		return line
	}
	if m.addingServer {
		label := []string{"name", "URL", "API key (optional)"}[m.addStep]
		line := m.styles.editLabel.Render("add server "+label+":") + " " + m.editInput.View()
		if m.editErr != "" {
			line += "  " + m.styles.editError.Render(m.editErr)
		}
		return line
	}
	if m.statusMsg != "" {
		style := m.styles.statusOK
		if m.statusErr {
			style = m.styles.statusErr
		}
		return style.Render(m.statusMsg)
	}
	return ""
}

func (m Model) editLabel() string {
	switch m.editPurpose {
	case editPurposeScalar:
		return m.editKey
	case editPurposeServerURL:
		if m.editServer < len(m.cfg.Servers) {
			return m.cfg.Servers[m.editServer].Name + " URL"
		}
	case editPurposeServerKey:
		if m.editServer < len(m.cfg.Servers) {
			return m.cfg.Servers[m.editServer].Name + " API key (empty=keep)"
		}
	}
	return "edit"
}

// renderShortHelp is the always-on bottom hint bar.
func (m Model) renderShortHelp(_ int) string {
	parts := []string{}
	for _, b := range m.keys.shortHelp() {
		parts = append(parts, fmt.Sprintf("%s %s", b.Help().Key, b.Help().Desc))
	}
	return m.styles.statusBar.Render(strings.Join(parts, "  ·  "))
}

// renderHelp shows the full key table when ? is pressed.
func (m Model) renderHelp(bodyH int) string {
	var b strings.Builder
	b.WriteString(m.styles.headerActive.Render("Keybindings") + "\n\n")
	for _, group := range m.keys.fullHelp() {
		for _, k := range group {
			b.WriteString(fmt.Sprintf("  %-12s  %s\n",
				m.styles.statusKey.Render(k.Help().Key), k.Help().Desc))
		}
		b.WriteString("\n")
	}
	b.WriteString(m.styles.statusBar.Render("press any key to dismiss"))
	return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, b.String())
}
