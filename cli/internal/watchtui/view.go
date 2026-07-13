package watchtui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the full screen. Layout:
//
//	┌─ Watchers (3) ─────────────────────────────────┐
//	│ ● PID 24164  /path/to/proj      auto · 3m ago   │
//	│ ○  stopped   /path/to/other                     │
//	└─────────────────────────────────────────────────┘
//	 status line
//	 short-help bar (always visible)
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
	if m.detail {
		return m.renderDetail(bodyH)
	}

	body := m.renderList(m.width, bodyH)
	statusLine := m.renderStatus()
	helpLine := m.renderShortHelp()

	return lipgloss.JoinVertical(lipgloss.Left, body, statusLine, helpLine)
}

// renderList draws the bordered watcher list. The row set is windowed
// around the cursor so long lists (many known projects) stay navigable on
// short terminals, with a "N more" indicator when rows are hidden.
func (m Model) renderList(w, h int) string {
	running := 0
	for _, it := range m.items {
		if it.Running {
			running++
		}
	}
	title := m.styles.header.Render(fmt.Sprintf("Watchers — %d running / %d known", running, len(m.items)))

	contentH := h - 2 // inside the border
	lines := []string{title, ""}

	if len(m.items) == 0 {
		lines = append(lines, m.styles.muted.Render("no projects — register one with `cix init`"))
	} else {
		maxRows := contentH - 2 // title + blank
		if maxRows < 1 {
			maxRows = 1
		}
		start, end := listWindow(len(m.items), m.cursor, maxRows)
		if start > 0 || end < len(m.items) {
			// Spend one row on a scroll indicator.
			start, end = listWindow(len(m.items), m.cursor, maxRows-1)
			lines = append(lines, m.styles.muted.Render(scrollHint(start, end, len(m.items))))
		}
		pathW := m.pathColumnWidth()
		for i := start; i < end; i++ {
			lines = append(lines, m.renderRow(i, m.items[i], pathW))
		}
	}

	inner := w - 4 // border + padding
	if inner < 20 {
		inner = 20
	}
	return m.styles.panel.Width(inner).Height(h).Render(strings.Join(lines, "\n"))
}

// listWindow returns a [start,end) slice of size at most `size` that keeps
// `cursor` visible, centered when possible and pinned at the ends. Stateless
// so View stays a pure read of the Model.
func listWindow(n, cursor, size int) (int, int) {
	if size >= n {
		return 0, n
	}
	start := cursor - size/2
	if start < 0 {
		start = 0
	}
	if start > n-size {
		start = n - size
	}
	return start, start + size
}

func scrollHint(start, end, n int) string {
	above, below := start, n-end
	switch {
	case above > 0 && below > 0:
		return fmt.Sprintf("↑ %d more · ↓ %d more", above, below)
	case above > 0:
		return fmt.Sprintf("↑ %d more", above)
	default:
		return fmt.Sprintf("↓ %d more", below)
	}
}

// renderRow renders one watcher line.
func (m Model) renderRow(i int, it Item, pathW int) string {
	var dot, state string
	if it.Running {
		dot = m.styles.dot.Render("●")
		state = fmt.Sprintf("PID %-6d", it.PID)
	} else {
		dot = m.styles.dotDimmed.Render("○")
		state = "stopped   "
	}

	trailer := ""
	if it.AutoWatch {
		trailer += " " + m.styles.autoTag.Render("auto")
	}
	if it.LastIndexedAt != nil {
		trailer += " " + m.styles.muted.Render("· "+humanAge(*it.LastIndexedAt))
	}

	line := fmt.Sprintf("%s %s  %s%s", dot, state, padRight(it.Path, pathW), trailer)
	if i == m.cursor {
		return m.styles.rowSel.Render(line)
	}
	return m.styles.row.Render(line)
}

func (m Model) pathColumnWidth() int {
	w := 20
	for _, it := range m.items {
		if l := lipgloss.Width(it.Path); l > w {
			w = l
		}
	}
	if w > 60 {
		w = 60
	}
	return w
}

func padRight(s string, w int) string {
	if lipgloss.Width(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// renderStatus shows the delete-confirmation prompt, or the transient status
// line (OK green / error red).
func (m Model) renderStatus() string {
	if m.confirmDelete {
		return m.styles.confirm.Render(fmt.Sprintf(
			"delete %s and its server index? this is permanent  (y/N)", label(m.confirmPath)))
	}
	if m.statusMsg == "" {
		// Keep the in-flight action visible even after a keypress cleared
		// the transient status.
		if m.busy != "" {
			return m.styles.statusBar.Render(m.busy + "…")
		}
		return ""
	}
	style := m.styles.statusOK
	if m.statusErr {
		style = m.styles.statusErr
	}
	return style.Render(m.statusMsg)
}

// renderShortHelp is the always-on bottom hint bar.
func (m Model) renderShortHelp() string {
	var parts []string
	for _, b := range m.keys.shortHelp() {
		parts = append(parts, fmt.Sprintf("%s %s", b.Help().Key, b.Help().Desc))
	}
	return m.styles.statusBar.Render(strings.Join(parts, "  ·  "))
}

// renderHelp shows the full key table when ? is pressed.
func (m Model) renderHelp(bodyH int) string {
	var b strings.Builder
	b.WriteString(m.styles.header.Render("Keybindings") + "\n\n")
	for _, group := range m.keys.fullHelp() {
		for _, k := range group {
			b.WriteString(fmt.Sprintf("  %-14s  %s\n",
				m.styles.statusKey.Render(k.Help().Key), k.Help().Desc))
		}
		b.WriteString("\n")
	}
	b.WriteString(m.styles.statusBar.Render("press any key to dismiss"))
	return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, b.String())
}

// renderDetail shows the tail of the selected watcher's log.
func (m Model) renderDetail(bodyH int) string {
	title := m.styles.header.Render("Log — " + m.detailPath)

	visible := m.logLines
	if max := bodyH - 4; max > 0 && len(visible) > max {
		visible = visible[len(visible)-max:]
	}

	lines := []string{title, ""}
	lines = append(lines, visible...)

	inner := m.width - 4
	if inner < 20 {
		inner = 20
	}
	body := m.styles.panel.Width(inner).Height(bodyH).Render(strings.Join(lines, "\n"))
	help := m.styles.statusBar.Render("l/enter/esc close  ·  q quit")
	return lipgloss.JoinVertical(lipgloss.Left, body, "", help)
}
