package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// Mouse support: the wheel scrolls the active list/table (and the Information panel
// and repo picker), a left click on the tab bar switches tabs, and a left click on a
// fleet table row selects it (a click in the multi-select gutter column toggles its
// checkbox). Reliable click->row mapping is possible because the fleet tables use the
// scrollTable wrapper, which owns its scroll offset (see scrolltable.go).

// wheelStep is how many rows one wheel notch moves a table cursor.
const wheelStep = 3

// selectGutter is the width of the leading multi-select gutter column (persistent +
// ephemeral tables); a click at x < this toggles the row's selection.
const selectGutter = 3

// onMouse routes a mouse event, mirroring onKey's overlay precedence. Blocking
// modals are keyboard-only and swallow the mouse; the Information panel scrolls
// natively (its viewport handles the wheel); the repo picker moves its cursor.
func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Overlays handled inside onKey that own the loop while open: ignore the mouse
	// so a stray wheel/click can't act on the tab behind them. (The huh forms are
	// intercepted before Update's switch, so they never reach here.)
	if m.creating || m.op.open || m.orgPickOpen || m.previewOpen || m.typedOpen || m.modalOpen || m.restoreOpen {
		return m, nil
	}
	if m.infoOpen {
		var cmd tea.Cmd
		m.info, cmd = m.info.update(msg) // bubbles/viewport scrolls on the wheel
		return m, cmd
	}
	if m.logOpen {
		var cmd tea.Cmd
		m.log, cmd = m.log.update(msg) // bubbles/viewport scrolls on the wheel
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
		if e.Button != tea.MouseLeft {
			return m, nil
		}
		if t, ok := m.tabAtX(e.X, e.Y); ok {
			if t != m.tab {
				m.tab = t
				// Capture the cmd before returning so enterTab's mutations
				// (loading/layout/info-close) land in the returned model - return
				// values are evaluated left to right, so `return m, m.enterTab()`
				// would snapshot m before enterTab ran.
				cmd := m.enterTab()
				return m, cmd
			}
			return m, nil // clicked the active tab's own label
		}
		// Per-row click on the active fleet table: map the screen Y to a row via the
		// scrollTable's owned offset, move the cursor there, and toggle selection when
		// the click lands in the multi-select gutter.
		if st := m.activeScrollTable(); st != nil {
			if row := st.rowAt(e.Y - m.fleetDataTop()); row >= 0 {
				st.setCursor(row)
				if e.X < selectGutter && m.selectableTab() {
					m.toggleSelect()
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// scrollActive moves the active tab's selectable by delta rows (negative = up). The
// fleet tables move their scrollTable cursor (the owned viewport follows); Settings
// steps its Lifecycle menu one item per notch; Health scrolls its host panel.
func (m *Model) scrollActive(delta int) {
	if st := m.activeScrollTable(); st != nil {
		if delta < 0 {
			st.moveUp(-delta)
		} else {
			st.moveDown(delta)
		}
		return
	}
	switch m.tab {
	case tabHealth:
		// The wheel scrolls the (often tall) host panel; ↑/↓ still move the org card.
		m.health.scrollHost(delta)
	case tabSettings:
		m.lifecycle.move(sign(delta))
	}
}

// activeScrollTable returns the active tab's fleet table wrapper, or nil for the
// non-table tabs (Health, Settings).
func (m *Model) activeScrollTable() *scrollTable {
	switch m.tab {
	case tabEphemeral:
		return &m.ephemeral.st
	case tabGroups:
		return &m.groups.st
	case tabDrift:
		return &m.drift.st
	case tabRuns:
		return &m.runs.st
	case tabPersistent:
		return &m.runners.st
	}
	return nil
}

// fleetDataTop is the screen row of the active fleet table's FIRST data row: the body
// origin (header + gap + tab bar), plus the per-tab lines rendered above the table
// (the newer-template banner, and Drift's class-strip + provenance lines), plus the
// table's frozen header. It is the anchor the click->row mapping subtracts.
func (m Model) fleetDataTop() int {
	top := lipgloss.Height(m.headerView()) + 1 + lipgloss.Height(m.tabBar())
	banner := 0
	if b := m.newerBanner(); b != "" {
		banner = lipgloss.Height(b)
	}
	switch m.tab {
	case tabDrift:
		top += banner + 2 // class-strip line + provenance line
	case tabGroups:
		// groupsView.view() renders the table first (no banner above it)
	default: // persistent, ephemeral (fleetBody prepends the banner)
		top += banner
	}
	if st := m.activeScrollTable(); st != nil {
		top += st.headerHeight()
	}
	return top
}

// tabBarRow is the screen row of the nav bar: the header height plus one blank
// spacer line (see View's JoinVertical order).
func (m Model) tabBarRow() int {
	return lipgloss.Height(m.headerView()) + 1
}

// tabAtX returns the tab whose segment spans column x on the tab-bar row. The tab
// bar is JoinHorizontal of the full-width segments starting at column 0, so the
// cumulative segment widths (tabWidths, shared with tabBar) give each hit range.
func (m Model) tabAtX(x, y int) (tab, bool) {
	if y != m.tabBarRow() {
		return 0, false
	}
	cx := 0
	for i, w := range m.tabWidths() {
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
