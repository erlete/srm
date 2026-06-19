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
	return v
}

func (m Model) headerView() string {
	title := m.theme.Title.Render("⬢ srm")
	scope := "all orgs"
	if m.orgFilter != "" {
		scope = m.orgFilter
	}
	pill := m.theme.Pill.Render("org: " + scope)
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
	if m.creating {
		pct := 0.0
		if m.createTotal > 0 {
			pct = float64(m.createDone) / float64(m.createTotal)
		}
		label := fmt.Sprintf("%s  %d/%d — %s", m.spin.View(), m.createDone, m.createTotal, m.createMsg)
		box := m.theme.Modal.Render(lipgloss.JoinVertical(lipgloss.Left,
			m.theme.ModalT.Render("Provisioning runners"),
			"",
			m.prog.ViewAs(pct),
			"",
			m.theme.Help.Render(label),
		))
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
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
	if m.modalOpen {
		return m.modal.view(m.width, h)
	}
	switch m.tab {
	case tabEphemeral:
		return m.ephemeral.view()
	case tabGroups:
		return m.groups.view()
	case tabHealth:
		return m.health.view()
	case tabSettings:
		return m.settings.view()
	default:
		return m.runners.view()
	}
}

func (m Model) footerView() string {
	status := m.statusText()
	help := m.theme.Help.Render(m.help.View(m.keys))
	if status == "" {
		return help
	}
	return lipgloss.JoinVertical(lipgloss.Left, status, help)
}

func (m Model) statusText() string {
	if m.loading {
		return m.spin.View() + " " + m.theme.StatusInfo.Render(orEllipsis(m.status))
	}
	if m.status == "" {
		return ""
	}
	if m.stErr {
		return m.theme.StatusErr.Render("✗ " + m.status)
	}
	return m.theme.StatusOK.Render("✓ " + m.status)
}

func orEllipsis(s string) string {
	if s == "" {
		return "loading…"
	}
	return s
}
