package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/erlete/srm/internal/service"
)

// driftView is the Drift tab's table: every non-healthy fleet row (persistent +
// ephemeral) the reconcile host tier surfaced, with its class, detail, and a busy
// marker. enter jumps to the row on its home tab; f/g run the repair/reap ops. The
// class strip is composed around it by the Model; host-wide stats live on the
// Health tab. Monochrome cells (badges/colors live in the strip), same as the
// other tables.
type driftView struct {
	st    scrollTable
	cols  []table.Column
	all   []driftEntry // full set (every non-healthy row from the snapshot)
	rows  []driftEntry // filtered (mirrors the table rows)
	theme Theme
	width int
	flt   filterState
}

// driftEntry is one non-healthy fleet row shown on the Drift tab.
type driftEntry struct {
	org, name, class, detail string
	busy                     bool
	isSlot                   bool
}

func newDriftView(t Theme) driftView {
	cols := []table.Column{
		{Title: "CLASS", Width: 11},
		{Title: "ORG", Width: 14},
		{Title: "NAME", Width: 22},
		{Title: "FIX-PLAN", Width: 16},
		{Title: "DETAIL", Width: 30}, // last col absorbs slack (fillWidth), so keep DETAIL last
	}
	return driftView{st: newScrollTable(cols, t.Table), cols: cols, theme: t,
		flt: newFilterState("type to filter by class / org / name / detail…")}
}

func (v *driftView) setSize(w, h int) {
	v.width = w
	v.st.setWidth(w)
	v.st.setColumns(fillWidth(v.cols, w))
	v.flt.setWidth(w - 4)
	if h > 3 {
		v.st.setHeight(h)
	}
}

// setRows rebuilds the full drift set from the fused snapshot's non-healthy rows,
// then re-applies the active filter.
func (v *driftView) setRows(snap service.FleetSnapshot) {
	v.all = v.all[:0]
	for _, r := range snap.Runners {
		if r.HostKnown && r.DriftClass != "" && r.DriftClass != service.ClassHealthy {
			v.all = append(v.all, driftEntry{org: r.Org, name: r.Runner.Name, class: r.DriftClass, detail: r.DriftDetail, busy: r.Runner.Busy})
		}
	}
	for _, s := range snap.Slots {
		if s.DriftClass != "" && s.DriftClass != service.ClassEphemeralSlot {
			v.all = append(v.all, driftEntry{org: s.Org, name: "slot " + s.Slot, class: s.DriftClass, detail: s.DriftDetail, isSlot: true})
		}
	}
	sort.Slice(v.all, func(i, j int) bool {
		if v.all[i].class != v.all[j].class {
			return v.all[i].class < v.all[j].class
		}
		if v.all[i].org != v.all[j].org {
			return v.all[i].org < v.all[j].org
		}
		return v.all[i].name < v.all[j].name
	})
	v.applyFilter()
}

// applyFilter recomputes the visible drift rows from the filter query, preserving
// the cursor position.
func (v *driftView) applyFilter() {
	cursor := v.st.cursor()
	q := v.flt.query()
	v.rows = v.rows[:0]
	for _, e := range v.all {
		if q == "" || strings.Contains(v.haystack(e), q) {
			v.rows = append(v.rows, e)
		}
	}
	tr := make([]table.Row, 0, len(v.rows))
	for _, r := range v.rows {
		g, l := driftGlyph(r.class)
		detail := r.detail
		if r.busy {
			detail = "(busy - skipped) " + detail
		}
		tr = append(tr, table.Row{g + " " + l, r.org, r.name, fixPlan(r.class), detail})
	}
	v.st.setRows(tr)
	if cursor >= len(tr) {
		cursor = len(tr) - 1
	}
	if cursor >= 0 {
		v.st.setCursor(cursor)
	}
}

func (v driftView) haystack(e driftEntry) string {
	_, label := driftGlyph(e.class)
	return strings.ToLower(strings.Join([]string{e.org, e.name, e.class, label, fixPlan(e.class), e.detail}, " "))
}

// fixPlan is the operator-facing remediation for a drift class - a pure function of
// the class, shown in the FIX-PLAN column so the row states not just what is wrong
// but what resolves it. The auto-repairable classes (stale-dropin, stuck, orphan-
// unit) name the action `f` performs; the authoritative-skip classes point at the
// binary; ephemeral-stuck names the manual recreate; report-only classes say so.
func fixPlan(class string) string {
	switch class {
	case service.ClassStaleDropIn:
		return "refresh drop-in"
	case service.ClassStuck:
		return "restart"
	case service.ClassOrphanUnit:
		return "remove orphan unit"
	case service.ClassEphemeralStuck:
		return "recreate lane"
	case service.ClassDropInNewer, service.ClassEphemeralNewer:
		return "update srm binary"
	case service.ClassLegacyFlat:
		return "migrate (report)"
	case service.ClassOrphanGitHub:
		return "report only"
	case service.ClassUnknown:
		return "n/a (list failed)"
	default:
		return "-"
	}
}

// startFilter focuses the filter input. stopFilter blurs it, optionally clearing.
func (v *driftView) startFilter() tea.Cmd { return v.flt.start() }
func (v *driftView) stopFilter(clear bool) {
	v.flt.stop(clear)
	if clear {
		v.applyFilter()
	}
}

func (v driftView) filtering() bool { return v.flt.filtering() }

func (v driftView) updateFilter(msg tea.Msg) (driftView, tea.Cmd) {
	var cmd tea.Cmd
	v.flt, cmd = v.flt.update(msg)
	v.applyFilter()
	return v, cmd
}

func (v driftView) update(msg tea.Msg) (driftView, tea.Cmd) {
	var cmd tea.Cmd
	cmd = v.st.update(msg)
	return v, cmd
}

func (v driftView) selected() (driftEntry, bool) {
	i := v.st.cursor()
	if i < 0 || i >= len(v.rows) {
		return driftEntry{}, false
	}
	return v.rows[i], true
}

func (v driftView) tableView() string { return v.st.view() }

// driftBody composes the Drift tab: banner + class strip + table (or an
// availability note) + the ops help line. Host-wide stats live on the Health tab.
func (m Model) driftBody() string {
	t := m.theme
	if !m.snap.HostTierAvailable {
		reason := "host tier not loaded yet"
		if !m.hostCapable {
			reason = "drift can only be assessed as root on the runner host (this looks like a remote / non-elevated session)"
		}
		return t.Help.Render("⚠ drift unavailable - " + reason)
	}

	parts := make([]string, 0, 8)
	if b := m.newerBanner(); b != "" {
		parts = append(parts, b)
	}

	filtering := m.drift.flt.shown()
	classes := map[string]int{}
	for _, r := range m.drift.rows {
		classes[r.class]++
	}
	if len(classes) == 0 {
		if filtering && len(m.drift.all) > 0 {
			// Rows exist but none match the filter - don't claim "no drift".
			parts = append(parts, t.Help.Render("no drift rows match"), m.drift.flt.line(t))
			return lipgloss.JoinVertical(lipgloss.Left, parts...)
		}
		parts = append(parts, t.Online.Render("✓ no drift - every audited runner and slot is healthy"))
		parts = append(parts, "", t.Faint.Render("host-wide stats live on the Health tab"))
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	parts = append(parts, t.PanelTtl.Render("Drift")+t.Faint.Render("  "+classStrip(classes)))
	parts = append(parts, t.Faint.Render(driftProvenance(m.snap.HostTierAt)))
	parts = append(parts, m.drift.tableView())
	if filtering {
		parts = append(parts, m.drift.flt.line(t))
	} else if !m.hostCapable {
		parts = append(parts, t.Faint.Render("repair needs root on the host"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// driftProvenance renders when this drift assessment was audited (a read-only
// reconcile pass), so the operator can judge its staleness before acting. driftBody
// only reaches here once the host tier merged, so the timestamp is set; the IsZero
// guard is defensive.
func driftProvenance(at time.Time) string {
	if at.IsZero() {
		return "read-only audit"
	}
	return "audited " + humanAgo(time.Since(at)) + " · read-only pass"
}

// humanAgo renders a short "Ns/Nm/Nh ago" for a non-negative elapsed duration.
func humanAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// classStrip renders "label N · label N" ordered by class name.
func classStrip(classes map[string]int) string {
	keys := make([]string, 0, len(classes))
	for k := range classes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		_, label := driftGlyph(k)
		parts = append(parts, fmt.Sprintf("%s %d", label, classes[k]))
	}
	return strings.Join(parts, " · ")
}
