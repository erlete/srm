package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	huh "charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// healthReport is one org's auth + retention + agent-freshness status (the TUI's
// `doctor`, per org).
type healthReport struct {
	Org           string
	Runners       int
	Online        int
	AuthErr       error
	RetentionDays int
	RetentionMax  int
	RetentionErr  error

	// Agent freshness (AgentVersionStatus): the published version, how many recorded
	// local runners are behind it, and whether the check resolved.
	AgentCurrent string
	AgentBehind  int
	AgentOK      bool
}

// toolProbe is one host toolchain check for the doctor host section.
type toolProbe struct {
	Name  string
	Path  string
	Found bool
}

// healthHost is the host-wide doctor block - the single home for everything host-
// related (capacity mode, aggregate slice, disks, caches, toolchain). It is the
// right "Host information" panel on the Health tab; no host stats live elsewhere.
type healthHost struct {
	CapacityMode string
	Tools        []toolProbe
	Stats        service.HostHealth
}

// healthView renders one bordered card per org, with an incremental text filter
// over the org name.
type healthView struct {
	theme   Theme
	all     []healthReport // full set
	reports []healthReport // filtered (mirrors the rendered cards)
	host    healthHost
	width   int
	cursor  int // selected org card (arrows move it; e/u act on it)
	flt     filterState
}

// move advances the org-card cursor, clamped to the visible set.
func (v *healthView) move(delta int) {
	v.cursor += delta
	if v.cursor < 0 {
		v.cursor = 0
	}
	if v.cursor >= len(v.reports) {
		v.cursor = len(v.reports) - 1
	}
}

// selectedOrg returns the org of the highlighted card ("" when none).
func (v healthView) selectedOrg() string {
	if v.cursor < 0 || v.cursor >= len(v.reports) {
		return ""
	}
	return v.reports[v.cursor].Org
}

// selected returns the highlighted org's health report (for the Information panel).
func (v healthView) selected() (healthReport, bool) {
	if v.cursor < 0 || v.cursor >= len(v.reports) {
		return healthReport{}, false
	}
	return v.reports[v.cursor], true
}

func newHealthView(t Theme) healthView {
	return healthView{theme: t, flt: newFilterState("type to filter by org…")}
}

func (v *healthView) setSize(w, _ int) {
	v.width = w
	v.flt.setWidth(w - 4)
}

func (v *healthView) setReports(r []healthReport, host healthHost) {
	v.all = r
	v.host = host
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
	if v.cursor >= len(v.reports) {
		v.cursor = len(v.reports) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
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

// retentionFor returns the loaded retention days + max for an org, ok=false when
// the org's retention is unavailable (not loaded, or the App lacks the permission).
func (v healthView) retentionFor(org string) (days, max int, ok bool) {
	for _, rep := range v.all {
		if rep.Org == org {
			if rep.RetentionErr != nil || rep.RetentionMax == 0 {
				return 0, 0, false
			}
			return rep.RetentionDays, rep.RetentionMax, true
		}
	}
	return 0, 0, false
}

// retentionForm edits an org's artifact-and-log retention (days, max-bounded).
type retentionForm struct {
	form *huh.Form
	org  string
	days string
}

func newRetentionForm(org string, cur, max int) *retentionForm {
	rf := &retentionForm{org: org, days: strconv.Itoa(cur)}
	rf.form = huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Edit retention").Description(fmt.Sprintf("%s · artifact + log retention in days (max %d). Not retroactive.", org, max)),
		huh.NewInput().Title("Days").Value(&rf.days).Validate(func(s string) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 1 {
				return fmt.Errorf("must be a positive integer")
			}
			if max > 0 && n > max {
				return fmt.Errorf("exceeds the org maximum of %d", max)
			}
			return nil
		}),
	)).WithWidth(56)
	return rf
}

func (rf *retentionForm) value() (string, int) {
	n, _ := strconv.Atoi(strings.TrimSpace(rf.days))
	return rf.org, n
}

func (v healthView) updateFilter(msg tea.Msg) (healthView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

// view renders the Health tab as a vertical split: org cards on the LEFT, a single
// "Host information" panel on the RIGHT that owns every host-wide stat (capacity,
// slice, disks, caches, toolchain). Host stats live here and nowhere else.
func (v healthView) view() string {
	t := v.theme
	var head string
	if v.flt.shown() {
		head = v.flt.line(t) + "\n"
	}

	// Right panel width ~ 42% of the body; left takes the rest.
	rightW := v.width * 42 / 100
	if rightW < 32 {
		rightW = 32
	}
	leftW := v.width - rightW - 3
	if leftW < 28 {
		leftW = 28
	}

	left := v.orgColumn(leftW)
	right := v.hostPanel(rightW)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	return head + body
}

// orgColumn renders the per-org doctor cards (auth, retention, agent freshness).
func (v healthView) orgColumn(w int) string {
	t := v.theme
	if len(v.reports) == 0 {
		msg := "no data - press r to refresh"
		if v.flt.shown() {
			msg = "no orgs match"
		}
		return lipgloss.NewStyle().Width(w).Render(t.Help.Render(msg))
	}
	cards := make([]string, 0, len(v.reports))
	for i, rep := range v.reports {
		var auth, ret, agent string
		if rep.AuthErr != nil {
			auth = t.Offline.Render("✗ auth/list - " + rep.AuthErr.Error())
		} else {
			auth = t.Online.Render(fmt.Sprintf("✓ auth/list - OK (%d runners, %d online)", rep.Runners, rep.Online))
		}
		if rep.RetentionErr != nil {
			ret = t.Busy.Render("• retention - unavailable (App lacks Administration permission)")
		} else {
			ret = t.StatusInfo.Render(fmt.Sprintf("• retention - %d days (max %d)", rep.RetentionDays, rep.RetentionMax))
		}
		switch {
		case !rep.AgentOK:
			agent = t.Faint.Render("• agent - published version unresolved")
		case rep.AgentBehind > 0:
			agent = t.Busy.Render(fmt.Sprintf("• agent - published %s · %d behind (u to upgrade)", rep.AgentCurrent, rep.AgentBehind))
		default:
			agent = t.Online.Render(fmt.Sprintf("• agent - published %s · current", rep.AgentCurrent))
		}
		title := rep.Org
		panel := t.Panel
		if i == v.cursor {
			title = "▸ " + title
			panel = t.Panel.BorderForeground(colPrimary) // highlight the selected org
		}
		card := lipgloss.JoinVertical(lipgloss.Left, t.PanelTtl.Render(title), auth, ret, agent)
		cards = append(cards, panel.Width(w-2).Render(card))
	}
	return lipgloss.JoinVertical(lipgloss.Left, cards...)
}

// hostPanel renders the single "Host information" panel: capacity mode, aggregate
// slice, disk usage, cache sizes, and the toolchain probe.
func (v healthView) hostPanel(w int) string {
	t := v.theme
	lines := []string{t.PanelTtl.Render("Host information")}
	if v.host.CapacityMode != "" {
		lines = append(lines, t.StatusInfo.Render("capacity  ")+t.Crumb.Render(v.host.CapacityMode))
	}

	h := v.host.Stats
	if !h.Loaded {
		lines = append(lines, "", t.Faint.Render("host stats unavailable"))
		lines = append(lines, t.Faint.Render("(run on the runner host as root)"))
	} else {
		// Aligned columns: KIND(6) NAME(17) VALUE(11, right) then a colored bar.
		row := func(kind, name, val string) string {
			return t.Faint.Render(fmt.Sprintf("%-6s %-17s %11s", kind, clipPlain(name, 17), val))
		}
		lines = append(lines, "")
		if h.SliceMax > 0 {
			frac := float64(h.SliceCurrent) / float64(h.SliceMax)
			lines = append(lines, row("slice", "aggregate", humanBytes(h.SliceCurrent)+"/"+humanBytes(h.SliceMax))+" "+bar(frac, 8, t))
		}
		for _, d := range h.Disks {
			lines = append(lines, row("disk", shortPath(d.Path), fmt.Sprintf("%d%%", d.UsePct))+" "+bar(float64(d.UsePct)/100, 8, t))
		}
		for _, c := range h.Caches {
			lines = append(lines, row("cache", shortCacheLabel(c.Label), humanBytes(c.Bytes)))
		}
	}

	if len(v.host.Tools) > 0 {
		var toolParts []string
		for _, tp := range v.host.Tools {
			if tp.Found {
				toolParts = append(toolParts, t.Online.Render("✓ "+tp.Name))
			} else {
				toolParts = append(toolParts, t.Offline.Render("✗ "+tp.Name))
			}
		}
		lines = append(lines, "", t.Crumb.Render("toolchain"), "  "+strings.Join(toolParts, "   "))
	}
	return t.Panel.Width(w - 2).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}
