package tui

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
)

// healthReport is one org's auth + retention status (the TUI's `doctor`).
type healthReport struct {
	Org           string
	Runners       int
	Online        int
	AuthErr       error
	RetentionDays int
	RetentionMax  int
	RetentionErr  error
}

// healthView renders one bordered card per org.
type healthView struct {
	theme   Theme
	reports []healthReport
	width   int
}

func newHealthView(t Theme) healthView { return healthView{theme: t} }

func (v *healthView) setSize(w, _ int) { v.width = w }

func (v *healthView) setReports(r []healthReport) { v.reports = r }

func (v healthView) view() string {
	if len(v.reports) == 0 {
		return v.theme.Help.Render("no data — press r to refresh")
	}
	cards := make([]string, 0, len(v.reports))
	for _, rep := range v.reports {
		var auth, ret string
		if rep.AuthErr != nil {
			auth = v.theme.Offline.Render("✗ auth/list — " + rep.AuthErr.Error())
		} else {
			auth = v.theme.Online.Render(fmt.Sprintf("✓ auth/list — OK (%d runners, %d online)", rep.Runners, rep.Online))
		}
		if rep.RetentionErr != nil {
			ret = v.theme.Busy.Render("• retention — unavailable (App lacks Actions-policy permission)")
		} else {
			ret = v.theme.StatusInfo.Render(fmt.Sprintf("• retention — %d days (max %d)", rep.RetentionDays, rep.RetentionMax))
		}
		card := lipgloss.JoinVertical(lipgloss.Left, v.theme.PanelTtl.Render(rep.Org), auth, ret)
		w := v.width - 4
		if w < 20 {
			w = 20
		}
		cards = append(cards, v.theme.Panel.Width(w).Render(card))
	}
	return strings.Join(cards, "\n")
}
