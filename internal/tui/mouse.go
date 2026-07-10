package tui

import (
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// Mouse support: wheel scrolls the active list/table (and the Information panel
// and repo picker), and a left click on the tab bar switches tabs. Per-row click
// selection on the scrolled inventory tables is intentionally not wired here:
// bubbles/table hides its scroll offset (unexported start/end + viewport YOffset),
// so an external click->row mapping can't be made reliably correct. Wheel + arrow
// keys cover scrolling; space/enter cover selection.

// wheelStep is how many rows one wheel notch moves a table cursor.
const wheelStep = 3

// onMouse routes a mouse event, mirroring onKey's overlay precedence. Blocking
// modals are keyboard-only and swallow the mouse; the Information panel scrolls
// natively (its viewport handles the wheel); the repo picker moves its cursor.
func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays handled inside onKey that own the loop while open: ignore the mouse
	// so a stray wheel/click can't act on the tab behind them. (The huh forms are
	// intercepted before Update's switch, so they never reach here.)
	if m.creating || m.op.open || m.orgPickOpen || m.previewOpen || m.typedOpen || m.modalOpen {
		return m, nil
	}
	if m.infoOpen {
		var cmd tea.Cmd
		m.info, cmd = m.info.update(msg) // bubbles/viewport scrolls on the wheel
		return m, cmd
	}
	if m.pickerOpen {
		if w, ok := msg.(tea.MouseWheelMsg); ok {
			switch w.Button {
			case tea.MouseWheelUp:
				m.picker.moveCursor(-1)
			case tea.MouseWheelDown:
				m.picker.moveCursor(1)
			}
		}
		return m, nil
	}

	switch e := msg.(type) {
	case tea.MouseWheelMsg:
		switch e.Button {
		case tea.MouseWheelUp:
			m.scrollActive(-wheelStep)
		case tea.MouseWheelDown:
			m.scrollActive(wheelStep)
		}
		return m, nil
	case tea.MouseClickMsg:
		if e.Button == tea.MouseLeft {
			if t, ok := m.tabAtX(e.X, e.Y); ok && t != m.tab {
				m.tab = t
				// Capture the cmd before returning so enterTab's mutations
				// (loading/layout/info-close) land in the returned model - return
				// values are evaluated left to right, so `return m, m.enterTab()`
				// would snapshot m before enterTab ran.
				cmd := m.enterTab()
				return m, cmd
			}
		}
		return m, nil
	}
	return m, nil
}

// scrollActive moves the active tab's selectable by delta rows (negative = up).
// The multi-column inventory tables move their bubbles/table cursor; the Health
// and Settings tabs step their single-selection lists one item per notch.
func (m *Model) scrollActive(delta int) {
	move := func(t *table.Model) {
		if delta < 0 {
			t.MoveUp(-delta)
		} else {
			t.MoveDown(delta)
		}
	}
	switch m.tab {
	case tabEphemeral:
		move(&m.ephemeral.tbl)
	case tabGroups:
		move(&m.groups.tbl)
	case tabDrift:
		move(&m.drift.tbl)
	case tabHealth:
		m.health.move(sign(delta))
	case tabSettings:
		m.lifecycle.move(sign(delta))
	default:
		move(&m.runners.tbl)
	}
}

// tabBarRow is the screen row of the nav bar: the header height plus one blank
// spacer line (see View's JoinVertical order).
func (m Model) tabBarRow() int {
	return lipgloss.Height(m.headerView()) + 1
}

// tabAtX returns the tab whose label spans column x on the tab-bar row. The tab
// bar is JoinHorizontal of the styled labels starting at column 0, so cumulative
// rendered widths give each label's hit range.
func (m Model) tabAtX(x, y int) (tab, bool) {
	if y != m.tabBarRow() {
		return 0, false
	}
	cx := 0
	for i, name := range tabNames {
		w := lipgloss.Width(m.theme.TabOff.Render(name))
		if tab(i) == m.tab {
			w = lipgloss.Width(m.theme.TabOn.Render(name))
		}
		if x >= cx && x < cx+w {
			return tab(i), true
		}
		cx += w
	}
	return 0, false
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	return 1
}
