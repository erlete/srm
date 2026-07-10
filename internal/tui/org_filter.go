package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// orgFilterModal is the centered org visibility picker (opened with o). It controls
// which configured orgs are shown this session - a display filter, not a reload
// scope: the fleet always loads every org, and the views hide rows whose org is not
// in the visible set. space toggles, a selects/clears all, enter applies, esc cancels.
type orgFilterModal struct {
	theme  Theme
	orgs   []string
	sel    map[string]bool
	cursor int
}

func newOrgFilterModal(t Theme, orgs []string, current map[string]bool) orgFilterModal {
	sel := map[string]bool{}
	if len(current) == 0 {
		for _, o := range orgs {
			sel[o] = true // empty visible set means "all"
		}
	} else {
		for o, v := range current {
			sel[o] = v
		}
	}
	return orgFilterModal{theme: t, orgs: orgs, sel: sel}
}

func (c *orgFilterModal) move(d int) {
	c.cursor += d
	if c.cursor < 0 {
		c.cursor = 0
	}
	if c.cursor >= len(c.orgs) {
		c.cursor = len(c.orgs) - 1
	}
}

func (c *orgFilterModal) toggle() {
	if c.cursor >= 0 && c.cursor < len(c.orgs) {
		o := c.orgs[c.cursor]
		c.sel[o] = !c.sel[o]
	}
}

// toggleAll selects all when any is unselected, else clears to none.
func (c *orgFilterModal) toggleAll() {
	all := true
	for _, o := range c.orgs {
		if !c.sel[o] {
			all = false
			break
		}
	}
	for _, o := range c.orgs {
		c.sel[o] = !all
	}
}

// result is the visible-org set to apply: nil when every org is selected ("all"),
// otherwise the explicit set. A selection of none also collapses to nil (showing
// nothing is never useful), so "all off" reads as "all".
func (c orgFilterModal) result() map[string]bool {
	out := map[string]bool{}
	for _, o := range c.orgs {
		if c.sel[o] {
			out[o] = true
		}
	}
	if len(out) == 0 || len(out) == len(c.orgs) {
		return nil
	}
	return out
}

func (c orgFilterModal) view(width, height int) string {
	t := c.theme
	rows := []string{t.ModalT.Render("Filter orgs"), t.Faint.Render("which orgs to show this session"), ""}
	for i, o := range c.orgs {
		mark := "[ ]"
		if c.sel[o] {
			mark = "[x]"
		}
		line := mark + " " + o
		if i == c.cursor {
			line = t.Title.Render("> " + line)
		} else {
			line = "  " + t.Crumb.Render(line)
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", t.Help.Render("space toggle · a all/none · enter apply · esc cancel"))
	box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// orgFilterKey handles a keypress while the org filter modal is open.
func (m Model) orgFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.orgPickOpen = false
		return m, nil
	case msg.String() == "enter":
		m.orgVisible = m.orgPick.result()
		m.orgPickOpen = false
		return m, m.reloadCurrent()
	case msg.String() == "up", msg.String() == "k":
		m.orgPick.move(-1)
	case msg.String() == "down", msg.String() == "j":
		m.orgPick.move(1)
	case msg.String() == " ", msg.String() == "space":
		m.orgPick.toggle()
	case msg.String() == "a":
		m.orgPick.toggleAll()
	}
	return m, nil
}
