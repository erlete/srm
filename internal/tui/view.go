package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// View renders the full-screen UI: header · tabs · body · footer.
func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("loading…")
	}
	bodyH := m.height - chromeHeight(m.help.ShowAll)
	if bodyH < 3 {
		bodyH = 3
	}
	// Fill the body to the full width and height so every screen spans the screen
	// and the footer is pinned to the bottom-left (short screens no longer let it
	// float up). Modals inside bodyView already self-center within this area.
	body := lipgloss.NewStyle().Width(m.width).Height(bodyH).Render(m.bodyView(bodyH))
	content := lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		"", // breathing room between the header and the nav bar
		m.tabBar(),
		body,
		m.footerView(),
	)
	v := tea.NewView(content)
	v.AltScreen = true
	// Capture mouse events (wheel scroll + clicks). Trade-off: while captured, the
	// terminal's native click-drag text selection is suppressed - hold Shift to
	// select/copy text in most terminals.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) headerView() string {
	title := m.theme.Title.Render("⬢ srm")
	scope := "all orgs"
	if n := len(m.orgVisible); n > 0 {
		total := len(m.mgr.OrgNames())
		if n == 1 {
			for o := range m.orgVisible {
				scope = o
			}
		} else {
			scope = fmt.Sprintf("%d of %d orgs", n, total)
		}
	}
	pill := m.theme.Pill.Render("orgs: " + scope)
	left := lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", pill)

	right := ""
	switch m.tab {
	case tabPersistent:
		right = m.runners.counts()
	case tabEphemeral:
		right = m.ephemeral.counts()
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// tabWidths returns each tab's TOTAL rendered width, distributing the slack between
// the sum of the labels' natural widths and the terminal width PROPORTIONALLY, so
// the nav bar spans the full screen (relative, never truncating a label). Shared by
// tabBar (render) and tabAtX (click hit-test) so both agree on the exact layout.
func (m Model) tabWidths() []int {
	ws := make([]int, len(tabNames))
	base := 0
	for i, name := range tabNames {
		style := m.theme.TabOff
		if tab(i) == m.tab {
			style = m.theme.TabOn
		}
		ws[i] = lipgloss.Width(style.Render(name))
		base += ws[i]
	}
	extra := m.width - base
	if extra <= 0 || base <= 0 {
		return ws
	}
	given := 0
	for i := range ws {
		share := extra * ws[i] / base
		ws[i] += share
		given += share
	}
	ws[len(ws)-1] += extra - given
	return ws
}

func (m Model) tabBar() string {
	ws := m.tabWidths()
	tabs := make([]string, 0, len(tabNames))
	for i, name := range tabNames {
		style := m.theme.TabOff
		if tab(i) == m.tab {
			style = m.theme.TabOn
		}
		// ws[i] is the TOTAL segment width. lipgloss Width() sets the width INCLUDING
		// padding (padding is applied before the align-to-width step) but BEFORE the
		// border is added, so subtract only the horizontal border (0 for these
		// bottom-border-only tabs). Center the label within its segment.
		content := ws[i] - style.GetHorizontalBorderSize()
		if content < 0 {
			content = 0
		}
		tabs = append(tabs, style.Width(content).Align(lipgloss.Center).Render(name))
	}
	return lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
}

func (m Model) bodyView(h int) string {
	// Modal precedence (the singleton invariant): create stream, then the op result
	// panel, then forms, then typed-confirm, then the yes/no confirm, then tab body.
	if m.creating {
		pct := 0.0
		if m.createTotal > 0 {
			pct = float64(m.createDone) / float64(m.createTotal)
		}
		label := fmt.Sprintf("%s  %d/%d - %s", m.spin.View(), m.createDone, m.createTotal, m.createMsg)
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Provisioning runners"),
			"",
			m.prog.ViewAs(pct),
			"",
			m.theme.Help.Render(label),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.op.open {
		return m.renderOpPanel(m.width, h)
	}
	if m.formOpen {
		title := "Provision runners"
		if m.form.ephemeral {
			title = "Add ephemeral slots"
		}
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render(title),
			"",
			m.form.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.setForm != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Edit capacity settings"),
			"",
			m.setForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.retForm != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Edit retention"),
			"",
			m.retForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.groupForm != nil {
		title := "Create runner group"
		if m.groupForm.editing {
			title = "Edit runner group"
		}
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render(title),
			"",
			m.groupForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.runnerEdit != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Edit runner"),
			"",
			m.runnerEdit.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.upgradeForm != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Upgrade options"),
			"",
			m.upgradeForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.uninstallForm != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Uninstall options"),
			"",
			m.uninstallForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.onboardForm != nil {
		title := "Onboard a new org"
		if m.onboardForm.edit {
			title = "Edit org"
		}
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render(title),
			"",
			m.onboardForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.manifestForm != nil {
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Edit host manifest"),
			"",
			m.manifestForm.form.View(),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
	}
	if m.restoreOpen {
		return m.restore.view(m.width, h)
	}
	if m.orgPickOpen {
		return m.orgPick.view(m.width, h)
	}
	if m.pickerOpen {
		return m.picker.view(m.width, h)
	}
	if m.previewOpen {
		return m.preview.view(m.width, h)
	}
	if m.typedOpen {
		return m.typed.view(m.width, h)
	}
	if m.modalOpen {
		return m.modal.view(m.width, h)
	}
	if m.infoOpen {
		return m.info.view()
	}
	if m.logOpen {
		return m.log.view()
	}
	switch m.tab {
	case tabHealth:
		return m.health.view()
	case tabEphemeral:
		return m.fleetBody(m.ephemeral.tableView(), m.ephemeral.bottomLine())
	case tabGroups:
		return m.groups.view()
	case tabDrift:
		return m.driftBody()
	case tabRuns:
		return lipgloss.JoinVertical(lipgloss.Left, m.runs.tableView(), m.runs.bottomLine())
	case tabSettings:
		return m.settingsBody()
	default: // tabPersistent
		return m.fleetBody(m.runners.tableView(), m.runners.bottomLine())
	}
}

// settingsBody renders the Settings tab as a vertical split: the capacity policy
// card on the LEFT, the Lifecycle operations menu on the RIGHT. Folding Lifecycle
// in here keeps host setup/teardown beside the host capacity policy it relates to.
func (m Model) settingsBody() string {
	rightW := m.width * 42 / 100
	if rightW < 30 {
		rightW = 30
	}
	leftW := m.width - rightW - 3
	if leftW < 30 {
		leftW = 30
	}
	left := m.settings.card(leftW)
	right := m.lifecyclePanel(rightW)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
}

// fleetBody composes a fleet list tab: the cross-tab newer-template banner, the
// table, then the one-line detail (or selection chip). The full read-out lives in
// the Information panel (i). Host-wide stats live on the Health tab, not here.
func (m Model) fleetBody(tableStr, bottomStr string) string {
	parts := make([]string, 0, 3)
	if b := m.newerBanner(); b != "" {
		parts = append(parts, b)
	}
	parts = append(parts, tableStr, m.selectionLine(bottomStr))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// selectionLine prefixes the bottom detail line with a "selected: N" chip when a
// multi-selection is active on this tab.
func (m Model) selectionLine(bottomStr string) string {
	if n := m.selectionCount(); n > 0 {
		return m.theme.Pill.Render(fmt.Sprintf("selected: %d", n)) + "  " + bottomStr
	}
	return bottomStr
}

func (m Model) footerView() string {
	status := m.statusText()
	var help string
	if m.help.ShowAll {
		help = m.theme.Help.Render(m.help.View(m.keys))
	} else {
		// Per-tab controls live ONLY here (no inline hints in the bodies); ? expands.
		help = clip(m.theme.Help.Render(m.contextHelp()), m.width)
	}
	if status == "" {
		return help
	}
	return lipgloss.JoinVertical(lipgloss.Left, status, help)
}

// contextHelp is the one-line, screen-specific control bar shown at the bottom.
// Only keys that make sense on the active tab appear (e.g. no "new" on Health).
func (m Model) contextHelp() string {
	global := "  ·  o orgs · tab views · a auto · ? help · q quit"
	switch m.tab {
	case tabHealth:
		return "↑/↓ org · PgUp/PgDn scroll host · i info · e retention · u upgrade · P provision · r refresh" + global
	case tabPersistent:
		return "↑/↓ move · space select · A all · i info · n new · e group · d destroy · u upgrade · b rollback · R refresh · c recreate · / filter" + global
	case tabEphemeral:
		return "↑/↓ move · space select · A all · i info · n new · d destroy · c recreate · / filter" + global
	case tabGroups:
		return "↑/↓ move · i info · n new · e edit · d delete · enter repo access · / filter" + global
	case tabDrift:
		return "↑/↓ move · i info · enter jump · f fix · g reap · r re-audit · / filter" + global
	case tabRuns:
		return "↑/↓ move · i info · L log · / filter · r refresh" + global
	case tabSettings:
		return "↑/↓ lifecycle · enter run · e edit capacity" + global
	}
	return "tab views · ? help · q quit"
}

func (m Model) statusText() string {
	suffix := ""
	if fleetTab(m.tab) && m.fleetLoaded {
		suffix = "   " + m.fleetStatusSuffix()
	}
	if m.loading {
		return m.spin.View() + " " + m.theme.StatusInfo.Render(orEllipsis(m.status)) + suffix
	}
	if m.status == "" {
		return strings.TrimSpace(suffix)
	}
	if m.stErr {
		return m.theme.StatusErr.Render("✗ "+m.status) + suffix
	}
	return m.theme.StatusOK.Render("✓ "+m.status) + suffix
}

func orEllipsis(s string) string {
	if s == "" {
		return "loading…"
	}
	return s
}
