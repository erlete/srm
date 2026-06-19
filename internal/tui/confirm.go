package tui

import lipgloss "charm.land/lipgloss/v2"

// confirmModal is a yes/no modal. Focus defaults to "No" for safety on
// destructive actions.
type confirmModal struct {
	theme   Theme
	title   string
	message string
	yes     bool // true when the affirmative button is focused
}

func newConfirm(t Theme, title, message string) confirmModal {
	return confirmModal{theme: t, title: title, message: message, yes: false}
}

func (c *confirmModal) toggle() { c.yes = !c.yes }

// view centers the modal box over a width×height area.
func (c confirmModal) view(width, height int) string {
	t := c.theme
	yes, no := " Yes ", " No "
	var yesBtn, noBtn string
	if c.yes {
		yesBtn, noBtn = t.BtnOn.Render(yes), t.BtnOff.Render(no)
	} else {
		yesBtn, noBtn = t.BtnOff.Render(yes), t.BtnOn.Render(no)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, noBtn, "    ", yesBtn)

	body := lipgloss.JoinVertical(lipgloss.Left,
		t.ModalT.Render(c.title),
		"",
		c.message,
		"",
		lipgloss.PlaceHorizontal(lipgloss.Width(c.message), lipgloss.Center, buttons),
	)
	box := t.Modal.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
