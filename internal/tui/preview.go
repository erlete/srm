package tui

import (
	lipgloss "charm.land/lipgloss/v2"
)

// previewModal is the middle rung of the safety ladder: for host-wide ops (fix,
// reap, prune, provision) it shows the PLAN from a dry-run pass and asks Apply /
// Cancel before the real pass runs. Defaults to Cancel. The operator sees exactly
// what would change - safer than a blind yes/no for a fleet-wide hammer.
type previewModal struct {
	theme Theme
	title string
	note  string   // a one-line scope/safety caption
	lines []string // the plan (already styled)
	apply bool     // Apply focused (default false = Cancel)
}

func newPreview(t Theme, title, note string, lines []string) previewModal {
	return previewModal{theme: t, title: title, note: note, lines: lines}
}

func (c *previewModal) toggle() { c.apply = !c.apply }

func (c previewModal) view(width, height int) string {
	t := c.theme
	// Cap the modal width so a long plan line never pushes the border off-screen;
	// each line is clipped with an ellipsis to fit (ANSI-aware).
	inner := width - 10
	if inner > 100 {
		inner = 100
	}
	if inner < 24 {
		inner = 24
	}
	rows := []string{t.ModalT.Render(c.title)}
	if c.note != "" {
		rows = append(rows, t.Faint.Render(clip(c.note, inner)))
	}
	rows = append(rows, "")
	if len(c.lines) == 0 {
		rows = append(rows, t.Online.Render("nothing to do - no drift to repair"))
	} else {
		// Cap the visible plan so a fleet-wide preview never overflows.
		const limit = 14
		for i, l := range c.lines {
			if i >= limit {
				rows = append(rows, t.Faint.Render("  … and more"))
				break
			}
			rows = append(rows, clip(l, inner))
		}
	}
	rows = append(rows, "")

	apply, cancel := " Apply ", " Cancel "
	var applyBtn, cancelBtn string
	if c.apply {
		applyBtn, cancelBtn = t.BtnOn.Render(apply), t.BtnOff.Render(cancel)
	} else {
		applyBtn, cancelBtn = t.BtnOff.Render(apply), t.BtnOn.Render(cancel)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, cancelBtn, "    ", applyBtn)
	rows = append(rows, buttons)

	box := t.Modal.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
