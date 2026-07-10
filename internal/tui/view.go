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

func (m Model) tabBar() string {
	tabs := make([]string, 0, len(tabNames))
	for i, name := range tabNames {
		if tab(i) == m.tab {
			tabs = append(tabs, m.theme.TabOn.Render(name))
		} else {
			tabs = append(tabs, m.theme.TabOff.Render(name))
		}
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
	switch m.tab {
	case tabHealth:
		return m.health.view()
	case tabEphemeral:
		return m.fleetBody(m.ephemeral.tableView(), m.ephemeral.bottomLine())
	case tabGroups:
		return m.groups.view()
	case tabDrift:
		return m.driftBody()
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
		return "↑/↓ org · i info · e retention · u upgrade · P provision · r refresh" + global
	case tabPersistent:
		return "↑/↓ move · space select · A all · i info · n new · e group · d destroy · u upgrade · b rollback · R refresh · c recreate · / filter" + global
	case tabEphemeral:
		return "↑/↓ move · space select · A all · i info · n new · d destroy · c recreate · / filter" + global
	case tabGroups:
		return "↑/↓ move · i info · n new · e edit · d delete · enter repo access · / filter" + global
	case tabDrift:
		return "↑/↓ move · i info · enter jump · f fix · g reap · r re-audit · / filter" + global
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
