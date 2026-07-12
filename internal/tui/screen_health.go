package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/viewport"
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
// related (capacity mode, aggregate slice, disks, caches, toolchain, manifest drift,
// rootless-DinD readiness). It is the right "Host information" panel on the Health
// tab; no host stats live elsewhere.
type healthHost struct {
	CapacityMode string
	Tools        []toolProbe
	Stats        service.HostHealth

	// Dependency-manifest drift (M3). ManifestSet is false when no host manifest is
	// configured; ManifestOK is false when the drift probe errored (render "unknown").
	ManifestSet     bool
	ManifestMissing []string
	ManifestOK      bool

	// Rootless-DinD host readiness (M3); DinD.Enabled is false unless
	// docker.rootlessDinD is on (or the host has no rootless-Docker analog).
	DinD service.DinDReport
}

// healthView renders one bordered card per org, with an incremental text filter
// over the org name.
type healthView struct {
	theme   Theme
	all     []healthReport // full set
	reports []healthReport // filtered (mirrors the rendered cards)
	host    healthHost
	width   int
	bodyH   int // total body-height budget (bounds the scrollable host panel)
	cursor  int // selected org card (arrows move it; e/u act on it)
	flt     filterState

	// hostVP scrolls the "Host information" panel when its content (capacity, disks,
	// caches, toolchain, manifest, rootless-DinD) is taller than the body. The wheel
	// and PgUp/PgDn drive it; the frozen title stays above it. hostContentH is the
	// content's rendered line count (drives the overflow scroll hint).
	hostVP       viewport.Model
	hostContentH int
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
	vp := viewport.New()
	vp.MouseWheelEnabled = false // the Model routes the wheel to scrollHost
	return healthView{theme: t, hostVP: vp, flt: newFilterState("type to filter by org…")}
}

func (v *healthView) setSize(w, h int) {
	v.width = w
	v.bodyH = h
	v.flt.setWidth(w - 4)
	v.syncHost()
}

func (v *healthView) setReports(r []healthReport, host healthHost) {
	v.all = r
	v.host = host
	v.applyFilter()
	v.syncHost()
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
	rightW := v.rightWidth()
	leftW := v.width - rightW - 3
	if leftW < 28 {
		leftW = 28
	}

	left := v.orgColumn(leftW)
	right := v.hostPanel(rightW)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	return head + body
}

// rightWidth is the "Host information" panel's outer width (the split view() uses).
func (v healthView) rightWidth() int {
	rightW := v.width * 42 / 100
	if rightW < 32 {
		rightW = 32
	}
	return rightW
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

// hostPanel renders the single "Host information" panel: a frozen title (with an
// overflow scroll hint) above a viewport over the host-wide stats. The panel body
// never exceeds the screen - the viewport caps at the available height and the wheel
// / PgUp / PgDn scroll it (see syncHost).
func (v healthView) hostPanel(w int) string {
	t := v.theme
	title := t.PanelTtl.Render("Host information")
	if hint := v.scrollHint(); hint != "" {
		title += "  " + hint
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, title, v.hostVP.View())
	return t.Panel.Width(w - 2).Render(inner)
}

// hostContent composes every host-info line BELOW the title (capacity mode, aggregate
// slice, disks, caches, toolchain, manifest drift, rootless-DinD readiness) into one
// string. It carries no title and no border - the panel freezes the title above and
// draws the border around the viewport. This is the scrollable region's content.
func (v healthView) hostContent() string {
	t := v.theme
	var lines []string
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

	lines = append(lines, v.manifestLines()...)
	lines = append(lines, v.dindLines()...)
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// syncHost rebuilds the host-panel viewport after a size or data change: it loads the
// content, sets the width to the panel's inner text area, and caps the height so the
// bordered panel fits the body. When the content fits, the viewport is exactly its
// content height (the panel looks unchanged); when it overflows, it caps and scrolls.
func (v *healthView) syncHost() {
	content := v.hostContent()
	v.hostContentH = lipgloss.Height(content)
	v.hostVP.SetContent(content)

	if w := v.rightWidth() - 4; w > 0 { // panel border(2) + horizontal padding(2)
		v.hostVP.SetWidth(w)
	}
	// Available viewport height = body - filter-line reserve(1) - border(2) - title(1).
	availH := v.bodyH - 4
	if availH < 3 {
		availH = 3
	}
	vpH := v.hostContentH
	if vpH > availH {
		vpH = availH
	}
	if vpH < 1 {
		vpH = 1
	}
	v.hostVP.SetHeight(vpH)
	v.setHostOffset(v.hostVP.YOffset()) // reclamp against the new content/height
}

// scrollHost scrolls the host panel by delta lines (negative = up); pageHost moves a
// near-full page. Both clamp via setHostOffset.
func (v *healthView) scrollHost(delta int) { v.setHostOffset(v.hostVP.YOffset() + delta) }
func (v *healthView) pageHost(dir int)     { v.scrollHost(dir * v.hostVP.Height()) }

// setHostOffset clamps the requested offset to [0, contentH-visibleH] and applies it.
func (v *healthView) setHostOffset(off int) {
	maxOff := v.hostContentH - v.hostVP.Height()
	if maxOff < 0 {
		maxOff = 0
	}
	if off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	v.hostVP.SetYOffset(off)
}

// scrollHint returns a faint "▲▼ N%" indicator when the host content overflows its
// viewport (empty when everything fits). The arrows show which directions have more.
func (v healthView) scrollHint() string {
	if v.hostContentH <= v.hostVP.Height() {
		return ""
	}
	off, maxOff := v.hostVP.YOffset(), v.hostContentH-v.hostVP.Height()
	up, down := " ", " "
	if off > 0 {
		up = "▲"
	}
	if off < maxOff {
		down = "▼"
	}
	pct := 0
	if maxOff > 0 {
		pct = off * 100 / maxOff
	}
	return v.theme.Faint.Render(fmt.Sprintf("%s%s %d%%", up, down, pct))
}

// manifestLines renders the dependency-manifest drift status (M3): nothing when no
// host manifest is configured, else satisfied / N-missing / unknown, listing the
// first few missing items. "P to provision" points at the host provision op.
func (v healthView) manifestLines() []string {
	if !v.host.ManifestSet {
		return nil
	}
	t := v.theme
	switch {
	case !v.host.ManifestOK:
		return []string{"", t.Crumb.Render("manifest") + "  " + t.Faint.Render("drift unknown (probe failed)")}
	case len(v.host.ManifestMissing) == 0:
		return []string{"", t.Crumb.Render("manifest") + "  " + t.Online.Render("satisfied")}
	}
	out := []string{"", t.Crumb.Render("manifest") + "  " + t.Busy.Render(fmt.Sprintf("%d missing (P to provision)", len(v.host.ManifestMissing)))}
	const showMax = 4
	for i, item := range v.host.ManifestMissing {
		if i == showMax {
			out = append(out, t.Faint.Render(fmt.Sprintf("  … and %d more", len(v.host.ManifestMissing)-showMax)))
			break
		}
		out = append(out, t.Faint.Render("  - "+item))
	}
	return out
}

// dindLines renders rootless-Docker host readiness (M3): nothing unless
// docker.rootlessDinD is on. Passing checks are terse (✓ name); failures also show
// the remediation detail. A cross-org caveat leads when perOrgUsers is off with >1 org.
func (v healthView) dindLines() []string {
	if !v.host.DinD.Enabled {
		return nil
	}
	t := v.theme
	out := []string{"", t.Crumb.Render("rootless docker")}
	if v.host.DinD.CrossOrgRisk {
		out = append(out, t.Busy.Render("  ! perOrgUsers off with >1 org - no cross-org boundary"))
	}
	for _, c := range v.host.DinD.Checks {
		if c.OK {
			out = append(out, "  "+t.Online.Render("✓ "+c.Name))
		} else {
			out = append(out, "  "+t.Offline.Render("✗ "+c.Name)+"  "+t.Faint.Render(c.Detail))
		}
	}
	return out
}
