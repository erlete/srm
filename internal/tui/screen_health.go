package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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

// healthView renders one bordered card per org, with an incremental text filter
// over the org name.
type healthView struct {
	theme   Theme
	all     []healthReport // full set
	reports []healthReport // filtered (mirrors the rendered cards)
	width   int
	flt     filterState
}

func newHealthView(t Theme) healthView {
	return healthView{theme: t, flt: newFilterState("type to filter by org…")}
}

func (v *healthView) setSize(w, _ int) {
	v.width = w
	v.flt.setWidth(w - 4)
}

func (v *healthView) setReports(r []healthReport) {
	v.all = r
	v.applyFilter()
}

// applyFilter recomputes the visible org cards from the filter query.
func (v *healthView) applyFilter() {
	q := v.flt.query()
	v.reports = v.reports[:0]
	for _, rep := range v.all {
		if q == "" || strings.Contains(strings.ToLower(rep.Org), q) {
			v.reports = append(v.reports, rep)
		}
	}
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *healthView) startFilter() tea.Cmd { return v.flt.start() }
func (v *healthView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}

func (v healthView) filtering() bool { return v.flt.filtering() }

func (v healthView) updateFilter(msg tea.Msg) (healthView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v healthView) view() string {
	var head string
	if v.flt.shown() {
		head = v.flt.line(v.theme) + "\n"
	}
	if len(v.reports) == 0 {
		if v.flt.shown() {
			return head + v.theme.Help.Render("no orgs match")
		}
		return v.theme.Help.Render("no data - press r to refresh")
	}
	cards := make([]string, 0, len(v.reports))
	for _, rep := range v.reports {
		var auth, ret string
		if rep.AuthErr != nil {
			auth = v.theme.Offline.Render("✗ auth/list - " + rep.AuthErr.Error())
		} else {
			auth = v.theme.Online.Render(fmt.Sprintf("✓ auth/list - OK (%d runners, %d online)", rep.Runners, rep.Online))
		}
		if rep.RetentionErr != nil {
			ret = v.theme.Busy.Render("• retention - unavailable (App lacks Actions-policy permission)")
		} else {
			ret = v.theme.StatusInfo.Render(fmt.Sprintf("• retention - %d days (max %d)", rep.RetentionDays, rep.RetentionMax))
		}
		card := lipgloss.JoinVertical(lipgloss.Left, v.theme.PanelTtl.Render(rep.Org), auth, ret)
		w := v.width - 4
		if w < 20 {
			w = 20
		}
		cards = append(cards, v.theme.Panel.Width(w).Render(card))
	}
	return head + strings.Join(cards, "\n")
}
