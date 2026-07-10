package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// typedConfirmModal is the strongest gate in the safety ladder: a text-input the
// operator must fill with an EXACT token (a hostname, "PURGE", a fleet-wide
// "UPGRADE ALL") before the action arms. The plain yes/no confirmModal is too easy
// to fat-finger for fleet-wide or irreversible ops; here a wrong token simply
// leaves the action disabled. Gated in the same singleton precedence as the other
// modals - exactly one is ever open.
type typedConfirmModal struct {
	theme   Theme
	title   string
	message string
	want    string // the exact token the operator must type to arm
	input   textinput.Model
}

func newTypedConfirm(t Theme, title, message, want, placeholder string) typedConfirmModal {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = "> "
	ti.Focus()
	return typedConfirmModal{theme: t, title: title, message: message, want: want, input: ti}
}

// matched reports whether the typed text equals the required token exactly
// (trimmed). The affirmative action is refused until this is true.
func (c typedConfirmModal) matched() bool {
	return strings.TrimSpace(c.input.Value()) == c.want
}

func (c typedConfirmModal) update(msg tea.Msg) (typedConfirmModal, tea.Cmd) {
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}

func (c typedConfirmModal) view(width, height int) string {
	t := c.theme
	armed := t.StatusErr.Render("locked - type the token to arm")
	if c.matched() {
		armed = t.StatusOK.Render("armed - press enter to proceed")
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		t.ModalT.Render(c.title),
		"",
		c.message,
		"",
		c.input.View(),
		"",
		armed,
		t.Help.Render("esc to cancel"),
	)
	box := t.Modal.Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
